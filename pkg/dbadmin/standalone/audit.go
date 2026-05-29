package standalone

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// GenesisPrevHash is the all-zero SHA-256 value used as the PrevEventHash
// of the very first event in a fresh audit log. See doc.go decision #4.
const GenesisPrevHash = "0000000000000000000000000000000000000000000000000000000000000000"

// FileAuditSink implements dbadmin.AuditSink against a local NDJSON file
// with SHA-256 hash chain and optional HMAC-SHA256 signed chain heads.
//
// Concurrency: Record is non-blocking (sends to a buffered queue); a
// single drain goroutine performs the file write under a mutex. Close
// flushes and stops the drain.
type FileAuditSink struct {
	Path         string
	SigningKey   []byte
	SigningEvery int
	SigningClock time.Duration
	Forwarder    Forwarder
	Clock        Clock
	Logger       *slog.Logger
	QueueSize    int // default 4096
	Durability   string // "loose" (default) or "strict" (fsync per event)

	mu       sync.Mutex
	f        *os.File
	prevHash string
	counter  int
	lastSign time.Time

	queue   chan dbadmin.Event
	quit    chan struct{}
	done    chan struct{}
	drops   atomic.Int64
	started atomic.Bool

	// shipCtx cancels per-event forwarder goroutines on Close. shipWG
	// blocks Close until all in-flight shipments return so we don't
	// leak goroutines past process shutdown (C4).
	shipCtx    context.Context
	shipCancel context.CancelFunc
	shipWG     sync.WaitGroup
	// shipTimeout bounds an individual Ship() call so a wedged forwarder
	// can't pile up goroutines under sustained outage.
	shipTimeout time.Duration
}

// chainHeadLine is the JSON record emitted alongside events when signing
// is enabled.
type chainHeadLine struct {
	Type      string `json:"_type"`
	Head      string `json:"head"`
	Timestamp string `json:"ts"`
	Signature string `json:"sig"`
}

// OpenFileAuditSink opens (or creates) Path, scans the tail to recover
// the last event's hash, and starts the drain goroutine.
func OpenFileAuditSink(path string) (*FileAuditSink, error) {
	s := &FileAuditSink{Path: path}
	if err := s.Start(); err != nil {
		return nil, err
	}
	return s, nil
}

// Start opens the file and launches the drain goroutine. Safe to call
// once. Subsequent calls return nil.
func (s *FileAuditSink) Start() error {
	if !s.started.CompareAndSwap(false, true) {
		return nil
	}
	if s.Clock == nil {
		s.Clock = systemClock
	}
	if s.QueueSize <= 0 {
		s.QueueSize = 4096
	}
	if s.Logger == nil {
		s.Logger = slog.Default()
	}
	if s.SigningEvery <= 0 {
		s.SigningEvery = 1000
	}
	if s.SigningClock <= 0 {
		s.SigningClock = 5 * time.Minute
	}
	if s.Forwarder == nil {
		s.Forwarder = NopForwarder{}
	}
	if s.Path == "" {
		s.started.Store(false)
		return errors.New("standalone: empty audit log path")
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o750); err != nil {
		s.started.Store(false)
		return fmt.Errorf("standalone: mkdir audit dir: %w", err)
	}

	if st, err := os.Stat(s.Path); err == nil {
		if st.Mode().Perm()&^0o640 != 0 {
			s.started.Store(false)
			return fmt.Errorf("standalone: audit log %q mode %o broader than 0640", s.Path, st.Mode().Perm())
		}
	}

	f, err := os.OpenFile(s.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		s.started.Store(false)
		return fmt.Errorf("standalone: open audit log: %w", err)
	}
	// Re-chmod to handle the case where umask widened things.
	if err := os.Chmod(s.Path, 0o640); err != nil {
		_ = f.Close()
		s.started.Store(false)
		return fmt.Errorf("standalone: chmod audit log: %w", err)
	}

	s.f = f
	s.prevHash, err = recoverPrevHash(s.Path)
	if err != nil {
		_ = f.Close()
		s.started.Store(false)
		return err
	}
	s.lastSign = s.Clock()

	s.queue = make(chan dbadmin.Event, s.QueueSize)
	s.quit = make(chan struct{})
	s.done = make(chan struct{})
	s.shipCtx, s.shipCancel = context.WithCancel(context.Background())
	if s.shipTimeout <= 0 {
		s.shipTimeout = 2 * time.Second
	}
	go s.drain()
	return nil
}

// Record implements dbadmin.AuditSink. Non-blocking.
func (s *FileAuditSink) Record(ctx context.Context, e dbadmin.Event) {
	if !s.started.Load() {
		return
	}
	select {
	case s.queue <- e:
	default:
		// Queue full. Wait briefly, then drop with a metric.
		select {
		case s.queue <- e:
		case <-time.After(50 * time.Millisecond):
			s.drops.Add(1)
			s.Logger.Warn("standalone: audit queue full, event dropped",
				"event_id", e.EventID, "drops_total", s.drops.Load())
		}
	}
}

// Drops returns the cumulative count of dropped events.
func (s *FileAuditSink) Drops() int64 { return s.drops.Load() }

// Reopen closes and reopens the audit file (for SIGHUP / logrotate).
func (s *FileAuditSink) Reopen() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f != nil {
		_ = s.f.Sync()
		_ = s.f.Close()
		s.f = nil
	}
	f, err := os.OpenFile(s.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	s.f = f
	// We deliberately do NOT reset prevHash — the in-memory chain
	// continues across rotation. A fresh file with no prior events
	// will simply pick up the latest hash we already hold.
	return nil
}

// Close drains the queue and closes the file. Safe to call once.
//
// Order: signal drain to stop → wait for drain to finish writing buffered
// events → cancel ship context to unblock any pending Ship() calls → wait
// for ship goroutines to return (bounded, since each Ship inherits a
// per-event timeout) → close the file. This guarantees no forwarder
// goroutine outlives the process (C4).
func (s *FileAuditSink) Close() error {
	if !s.started.Load() {
		return nil
	}
	select {
	case <-s.quit:
		// already closed
		return nil
	default:
	}
	close(s.quit)
	<-s.done
	if s.shipCancel != nil {
		s.shipCancel()
	}
	// Bounded wait: each ship goroutine has its own timeout so this
	// returns within s.shipTimeout in the worst case.
	doneShip := make(chan struct{})
	go func() {
		s.shipWG.Wait()
		close(doneShip)
	}()
	select {
	case <-doneShip:
	case <-time.After(s.shipTimeout + time.Second):
		// Defensive — log and proceed; should never trip in practice.
		if s.Logger != nil {
			s.Logger.Warn("standalone: forwarder goroutines did not finish in time")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f != nil {
		_ = s.f.Sync()
		err := s.f.Close()
		s.f = nil
		return err
	}
	return nil
}

func (s *FileAuditSink) drain() {
	defer close(s.done)
	tick := time.NewTicker(s.SigningClock)
	defer tick.Stop()
	for {
		select {
		case e := <-s.queue:
			s.write(e)
		case <-tick.C:
			s.mu.Lock()
			if s.SigningKey != nil && time.Since(s.lastSign) >= s.SigningClock && s.counter > 0 {
				s.signAndShipHeadLocked()
			}
			s.mu.Unlock()
		case <-s.quit:
			// Drain remaining events.
			for {
				select {
				case e := <-s.queue:
					s.write(e)
				default:
					return
				}
			}
		}
	}
}

func (s *FileAuditSink) write(e dbadmin.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return
	}
	e.PrevEventHash = s.prevHash
	line, err := marshalEventCanonical(&e)
	if err != nil {
		s.Logger.Error("standalone: audit marshal failed", "err", err)
		return
	}
	if _, err := s.f.Write(append(line, '\n')); err != nil {
		s.Logger.Error("standalone: audit write failed", "err", err)
		return
	}
	if s.Durability == "strict" {
		_ = s.f.Sync()
	}
	sum := sha256.Sum256(line)
	s.prevHash = hex.EncodeToString(sum[:])
	s.counter++
	if s.Forwarder != nil {
		s.shipWG.Add(1)
		go func(l []byte) {
			defer s.shipWG.Done()
			ctx, cancel := context.WithTimeout(s.shipCtx, s.shipTimeout)
			defer cancel()
			_ = s.Forwarder.Ship(ctx, l)
		}(append([]byte(nil), line...))
	}
	if s.SigningKey != nil && s.counter >= s.SigningEvery {
		s.signAndShipHeadLocked()
	}
}

func (s *FileAuditSink) signAndShipHeadLocked() {
	if s.SigningKey == nil {
		return
	}
	now := s.Clock().UTC()
	payload := s.prevHash + "|" + now.Format(time.RFC3339Nano)
	mac := hmac.New(sha256.New, s.SigningKey)
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	head := chainHeadLine{
		Type:      "chainhead",
		Head:      s.prevHash,
		Timestamp: now.Format(time.RFC3339Nano),
		Signature: sig,
	}
	b, err := json.Marshal(&head)
	if err != nil {
		return
	}
	if _, err := s.f.Write(append(b, '\n')); err != nil {
		s.Logger.Error("standalone: chainhead write failed", "err", err)
		return
	}
	s.counter = 0
	s.lastSign = now
	if s.Forwarder != nil {
		go func(l []byte) {
			_ = s.Forwarder.Ship(context.Background(), l)
		}(append([]byte(nil), b...))
	}
}

// recoverPrevHash returns the chain hash of the last event in the file,
// or GenesisPrevHash if the file is empty / missing.
func recoverPrevHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return GenesisPrevHash, nil
		}
		return "", err
	}
	defer f.Close()
	// Naive: read every line, keep the last non-chainhead. Audit logs
	// rotate daily so the file rarely grows large enough for this to
	// matter; if needed, a future PR can tail the file in reverse.
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var lastEventLine []byte
	for sc.Scan() {
		l := sc.Bytes()
		if len(l) == 0 {
			continue
		}
		// chainhead lines start with `{"_type":"chainhead"`; skip.
		if isChainHead(l) {
			continue
		}
		lastEventLine = append(lastEventLine[:0], l...)
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if len(lastEventLine) == 0 {
		return GenesisPrevHash, nil
	}
	sum := sha256.Sum256(lastEventLine)
	return hex.EncodeToString(sum[:]), nil
}

func isChainHead(line []byte) bool {
	// Cheap: check for the literal "\"_type\":\"chainhead\"" substring
	// near the head of the line. A real parse only happens during
	// verify.
	if len(line) < 20 {
		return false
	}
	return containsBytes(line, []byte(`"_type":"chainhead"`)) ||
		containsBytes(line, []byte(`"_type": "chainhead"`))
}

func containsBytes(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		eq := true
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				eq = false
				break
			}
		}
		if eq {
			return true
		}
	}
	return false
}

// marshalEventCanonical serializes an Event with deterministic key
// order. We hand-roll the encoder so map-key ordering inside
// ParametersRedacted is stable (Go's encoding/json already sorts map
// keys, so the only handcraft is wrapping ParametersRedacted in a
// json.Marshaler that emits sorted keys; the rest is regular Marshal).
//
// IMPORTANT: this defines the CANONICAL wire format for audit-chain
// hashing. Any addition to dbadmin.Event or dbadmin.Target MUST be
// mirrored into the `wire` struct below AND covered by a test that pins
// the marshal output for a fixture event (TestCanonicalMarshal_*).
// Without the mirror, the new field is silently dropped from the hash
// input, causing chain verification to break on the very next event
// after the change ships — and any historical audit logs verified by a
// new binary will appear tampered. Because Go's encoding/json emits
// struct fields in declaration order, the wire field ORDER is also part
// of the canonical contract; new fields must be APPENDED, never inserted
// between existing fields. Fields with `json:"-"` tags on Event/Target
// are intentionally NOT chained (they're considered presentation-only).
func marshalEventCanonical(e *dbadmin.Event) ([]byte, error) {
	type wire struct {
		EventID            string                      `json:"event_id"`
		Timestamp          string                      `json:"timestamp"`
		UserID             string                      `json:"user_id"`
		UserRoleAtTime     string                      `json:"user_role_at_time"`
		SourceIP           string                      `json:"source_ip"`
		UserAgentHash      string                      `json:"user_agent_hash"`
		Action             string                      `json:"action"`
		Target             dbadmin.Target              `json:"target"`
		Statement          string                      `json:"statement"`
		ParametersRedacted *sortedJSONMap              `json:"parameters_redacted,omitempty"`
		ResultRows         int64                       `json:"result_rows"`
		DurationMS         int64                       `json:"duration_ms"`
		Error              string                      `json:"error"`
		StepUpJTI          string                      `json:"step_up_jti"`
		PrevEventHash      string                      `json:"prev_event_hash"`
	}
	var prm *sortedJSONMap
	if len(e.ParametersRedacted) > 0 {
		prm = &sortedJSONMap{m: e.ParametersRedacted}
	}
	w := wire{
		EventID:            e.EventID,
		Timestamp:          e.Timestamp.UTC().Format(time.RFC3339Nano),
		UserID:             e.UserID,
		UserRoleAtTime:     e.UserRoleAtTime.String(),
		SourceIP:           e.SourceIP,
		UserAgentHash:      e.UserAgentHash,
		Action:             string(e.Action),
		Target:             e.Target,
		Statement:          e.Statement,
		ParametersRedacted: prm,
		ResultRows:         e.ResultRows,
		DurationMS:         e.DurationMS,
		Error:              e.Error,
		StepUpJTI:          e.StepUpJTI,
		PrevEventHash:      e.PrevEventHash,
	}
	return json.Marshal(&w)
}

// sortedJSONMap marshals a map[string]any with keys sorted
// lexicographically. Required for canonical, hash-stable serialization.
type sortedJSONMap struct {
	m map[string]any
}

// MarshalJSON satisfies json.Marshaler.
func (s *sortedJSONMap) MarshalJSON() ([]byte, error) {
	if s == nil || len(s.m) == 0 {
		return []byte("{}"), nil
	}
	keys := make([]string, 0, len(s.m))
	for k := range s.m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf []byte
	buf = append(buf, '{')
	for i, k := range keys {
		if i > 0 {
			buf = append(buf, ',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf = append(buf, kb...)
		buf = append(buf, ':')
		vb, err := json.Marshal(s.m[k])
		if err != nil {
			return nil, err
		}
		buf = append(buf, vb...)
	}
	buf = append(buf, '}')
	return buf, nil
}

// Compile-time assertion.
var _ dbadmin.AuditSink = (*FileAuditSink)(nil)

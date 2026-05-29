package dbadmin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/auracp/auracp/internal/store"
	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/standalone"
)

// SigningKeyPath is the default on-disk location of the audit chain HMAC
// signing key. It lives in /etc/auracp/secrets/ — a directory that is
// root-owned, mode 0700 in production, mirroring the sibling
// /etc/auracp/secret.key (panel KEK). Tests may override the path via
// the SetSigningKeyPathForTest helper.
//
// FIX-1 (PD-SEC-01): moving the key out of the settings table prevents
// any authenticated panel user from reading it via GET /api/settings.
var (
	signingKeyPath    = "/etc/auracp/secrets/aura-db-audit.key"
	signingKeyPathMu  sync.RWMutex
)

// SigningKeySettingKey is the legacy settings-table key that used to hold
// the audit signing key. The migration in loadOrCreateSigningKey copies
// any pre-existing value out of the settings table into the on-disk file,
// then deletes the settings row so subsequent GET /api/settings calls
// cannot leak it.
const SigningKeySettingKey = "aura_db_audit_signing_key"

// SetSigningKeyPathForTest overrides the on-disk path used by
// loadOrCreateSigningKey. Test-only — production callers must use the
// default path.
func SetSigningKeyPathForTest(p string) (restore func()) {
	signingKeyPathMu.Lock()
	prev := signingKeyPath
	signingKeyPath = p
	signingKeyPathMu.Unlock()
	return func() {
		signingKeyPathMu.Lock()
		signingKeyPath = prev
		signingKeyPathMu.Unlock()
	}
}

func currentSigningKeyPath() string {
	signingKeyPathMu.RLock()
	defer signingKeyPathMu.RUnlock()
	return signingKeyPath
}

// panelAudit implements dbadmin.AuditSink with a dual-write strategy:
//
//  1. The SHA-256 hash-chained NDJSON log at AuditPath (forensic source
//     of truth). Embedded standalone.FileAuditSink — no replication of
//     primitives.
//
//  2. A one-line summary mirrored into the panel's existing audit_log
//     table so the panel UI's /api/audit feed shows Aura-DB activity
//     alongside site/database events. Best-effort: a failed mirror does
//     NOT fail the request (matches AuditSink contract).
//
// FIX-6 (INT-10/SDK-2): the panel-UI mirror writes through a bounded
// async queue so Record() returns in <1µs even when the SQLite store is
// under contention. Overflow events are dropped with a counter, never
// blocking the caller. Close drains the queue with a 5s deadline.
type panelAudit struct {
	chain *standalone.FileAuditSink
	store *store.Store
	log   *slog.Logger

	// mirror is the bounded async queue feeding store.AddAudit. nil when
	// store is nil. Capacity is panelAuditQueueCap.
	mirror chan mirrorEvent
	wg     sync.WaitGroup
	quit   chan struct{}
	drops  atomic.Int64
	closed atomic.Bool
}

// mirrorEvent carries the fields panelAudit.drainMirror needs to write a
// row into the panel audit_log table. We snapshot the data at enqueue
// time so the drain goroutine never touches the original Event.
//
// FIX-PR105 / C5: reqID is the engine-emitted correlation ID propagated
// from the request goroutine; we capture it at Record time so the panel
// audit_log line shares the same request ID as the slog line emitted by
// the request middleware (see slog.go). Without this, an operator
// grepping the journal for a request-id finds the slog line but not the
// audit_log row, and has to manually correlate by user+timestamp.
type mirrorEvent struct {
	actor  string
	action string
	target string
	detail string
	id     string
	reqID  string
}

const (
	// panelAuditQueueCap is the depth of the async mirror queue. At
	// ~200 events/s sustained this allows ~5s of burst slack before
	// drops start.
	panelAuditQueueCap = 1024
	// panelAuditCloseTimeout bounds Close()'s mirror-drain wait.
	panelAuditCloseTimeout = 5 * time.Second
	// panelAuditDropLogEvery rate-limits the "queue full" warn line so a
	// hot path doesn't drown the logs.
	panelAuditDropLogEvery = 100
)

// newPanelAudit constructs a panelAudit. signingKey may be nil — in that
// case the chain is unsigned (still hash-linked). Returns an error if
// the chain log cannot be opened (filesystem permission issue, etc.).
func newPanelAudit(path string, signingKey []byte, st *store.Store, logger *slog.Logger) (*panelAudit, error) {
	if logger == nil {
		logger = slog.Default()
	}
	sink := &standalone.FileAuditSink{
		Path:       path,
		SigningKey: signingKey,
		Logger:     logger,
	}
	if err := sink.Start(); err != nil {
		return nil, err
	}
	a := &panelAudit{
		chain: sink,
		store: st,
		log:   logger,
		quit:  make(chan struct{}),
	}
	if st != nil {
		a.mirror = make(chan mirrorEvent, panelAuditQueueCap)
		a.wg.Add(1)
		go a.drainMirror()
	}
	return a, nil
}

// Record dual-writes the event. Returns quickly (chain.Record uses its
// own queue; mirror is non-blocking with drop-on-overflow). Per
// AuditSink contract: MUST NOT fail the caller, MUST return quickly.
func (a *panelAudit) Record(ctx context.Context, e dbadmin.Event) {
	// 1) Forensic log (the standalone sink itself is non-blocking).
	if a.chain != nil {
		a.chain.Record(ctx, e)
	}
	// 2) Panel-UI mirror. Best-effort, fully async.
	if a.mirror == nil || a.closed.Load() {
		return
	}
	actor := e.UserID
	if actor == "" {
		actor = "system"
	}
	// FIX-PR105 / C2: previously the detail field was hand-rolled with
	// fmt.Sprintf("%q"), which uses Go-string-literal quoting (Unicode
	// escapes, embedded \x00 → \x00, etc.) — NOT JSON-string quoting.
	// Any audit event whose Error or other text field contained a
	// non-ASCII byte produced an audit_log row that wasn't valid JSON,
	// and the panel's /api/audit endpoint then served broken rows that
	// the SPA refused to parse. Marshal a proper struct so encoding/json
	// owns the escaping.
	detailJSON, err := json.Marshal(struct {
		EventID  string `json:"event_id"`
		Role     string `json:"role"`
		Rows     int64  `json:"rows"`
		DurMS    int64  `json:"dur_ms"`
		Err      string `json:"err,omitempty"`
	}{
		EventID: e.EventID,
		Role:    e.UserRoleAtTime.String(),
		Rows:    e.ResultRows,
		DurMS:   e.DurationMS,
		Err:     e.Error,
	})
	if err != nil {
		// json.Marshal of plain string/int can only fail on a programming
		// bug; fall back to a stable empty-object so the audit_log row
		// stays valid JSON regardless.
		detailJSON = []byte("{}")
	}
	me := mirrorEvent{
		actor:  actor,
		action: "dbadmin." + string(e.Action),
		target: e.Target.String(),
		detail: string(detailJSON),
		id:     e.EventID,
		reqID:  RequestIDFromContext(ctx),
	}
	select {
	case a.mirror <- me:
	default:
		// Queue full — drop with a counter. Rate-limited slog warn.
		n := a.drops.Add(1)
		if n%panelAuditDropLogEvery == 1 {
			a.log.Warn("dbadmin: panel audit mirror queue full, event dropped",
				"event_id", e.EventID, "drops_total", n)
		}
	}
}

// Drops returns the cumulative count of mirror events dropped due to
// queue saturation. Exposed for tests + ops metrics.
func (a *panelAudit) Drops() int64 { return a.drops.Load() }

// drainMirror pulls queued events into the panel audit_log table. One
// goroutine, owned by panelAudit. Exits on quit channel close.
//
// FIX-PR105 / C5: warn logs now carry the request-id captured at enqueue
// time (mirrorEvent.reqID) so a "panel audit mirror failed" line can be
// joined to the HTTP request that produced the event without a manual
// timestamp dance.
func (a *panelAudit) drainMirror() {
	defer a.wg.Done()
	for {
		select {
		case me := <-a.mirror:
			if err := a.store.AddAudit(me.actor, me.action, me.target, me.detail); err != nil {
				a.log.Warn("dbadmin: panel audit mirror failed",
					"err", err, "event_id", me.id, "req_id", me.reqID)
			}
		case <-a.quit:
			// Drain pending events with a deadline.
			deadline := time.Now().Add(panelAuditCloseTimeout)
			for {
				select {
				case me := <-a.mirror:
					if time.Now().After(deadline) {
						return
					}
					if err := a.store.AddAudit(me.actor, me.action, me.target, me.detail); err != nil {
						a.log.Warn("dbadmin: panel audit mirror failed on drain",
							"err", err, "event_id", me.id, "req_id", me.reqID)
					}
				default:
					return
				}
			}
		}
	}
}

// Reopen rotates the underlying NDJSON chain file (signal-driven
// logrotate hook). Mirrors FileAuditSink.Reopen; safe before / during
// drain.
func (a *panelAudit) Reopen() error {
	if a.chain == nil {
		return nil
	}
	return a.chain.Reopen()
}

// Close flushes and closes the chained log. Safe to call multiple times.
// Drains the panel-UI mirror queue with a bounded deadline.
func (a *panelAudit) Close() error {
	if !a.closed.CompareAndSwap(false, true) {
		return nil
	}
	if a.mirror != nil {
		close(a.quit)
		// Bounded wait for the drain goroutine. The drain goroutine
		// enforces its own panelAuditCloseTimeout deadline internally.
		done := make(chan struct{})
		go func() {
			a.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(panelAuditCloseTimeout + time.Second):
			// Defensive: drain goroutine wedged. Proceed anyway.
			a.log.Warn("dbadmin: panel audit mirror drain exceeded deadline")
		}
	}
	if a.chain == nil {
		return nil
	}
	return a.chain.Close()
}

// loadOrCreateSigningKey returns the HMAC chain-head signing key.
//
// FIX-1 (PD-SEC-01): the key lives on disk at currentSigningKeyPath()
// (default /etc/auracp/secrets/aura-db-audit.key, mode 0600), NOT in the
// panel settings table. Storing it in the settings table leaked it to
// every authenticated user via GET /api/settings.
//
// Boot-time migration: if the legacy settings row still exists, we copy
// its value out to the file (only if the file is absent — never
// overwrite a stronger on-disk key with the old one), then DELETE the
// settings row idempotently so future boots don't keep re-reading it.
//
// On a fresh deployment the file is created with 32 cryptographically
// random bytes, mode 0600. The parent directory is created with mode
// 0700 (root-only). The file is base64-encoded so it round-trips
// through ops tooling that may not handle binary cleanly; the actual
// HMAC input is the decoded raw bytes.
//
// PR #10.5 / FIX-C4: if the on-disk file is present but corrupted
// (wrong size, broken base64, world-readable, etc.) we now REFUSE TO
// START rather than silently minting a fresh key and breaking the
// existing audit chain. Silent regeneration was the original behavior
// pre-PR-#10.5: a stray `echo > aura-db-audit.key` from an ops script
// would invalidate every prior chain entry on the next reboot with no
// boot-log signal, and the operator had no way to notice until a
// forensics request months later. Now the daemon logs a loud error and
// fails Mount(); ops must triage (restore the key from backup, or
// explicitly delete the file and the chain log together to start
// fresh).
func loadOrCreateSigningKey(st *store.Store) ([]byte, error) {
	path := currentSigningKeyPath()

	// Phase 1: legacy migration.
	if st != nil {
		if v, ok := st.GetSetting(SigningKeySettingKey); ok && v != "" {
			// File absent? Copy the legacy value over once.
			if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
				if raw, decErr := base64.StdEncoding.DecodeString(v); decErr == nil && len(raw) == 32 {
					if werr := writeSigningKeyFile(path, raw); werr == nil {
						// Only delete after a successful write.
						_ = st.DeleteSetting(SigningKeySettingKey)
					}
				} else {
					// Bad legacy value: discard. The on-disk-create
					// branch below will mint a fresh key.
					_ = st.DeleteSetting(SigningKeySettingKey)
				}
			} else {
				// File already exists; legacy row is redundant — drop
				// it. This is the idempotent path on every subsequent
				// boot after the first migration.
				_ = st.DeleteSetting(SigningKeySettingKey)
			}
		}
	}

	// Phase 2: read existing file.
	if raw, err := readSigningKeyFile(path); err == nil {
		return raw, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		// FIX-C4: corrupted on-disk key is a refuse-on-boot condition.
		// Surfacing the wrapped error here means Mount() returns it,
		// auracpd's log.Fatalf prints it, and systemd captures the
		// "dbadmin: read audit signing key … : …" line so the operator
		// sees exactly what's wrong.
		slog.Default().Error("dbadmin: refusing to start with corrupted audit signing key",
			"path", path, "err", err)
		return nil, fmt.Errorf("dbadmin: read audit signing key %q: %w", path, err)
	}

	// Phase 3: create fresh.
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	if err := writeSigningKeyFile(path, b[:]); err != nil {
		return nil, err
	}
	return b[:], nil
}

// writeSigningKeyFile persists raw to path, base64-encoded, mode 0600,
// with the parent dir mode 0700. Atomic via write-then-rename.
func writeSigningKeyFile(path string, raw []byte) error {
	if len(raw) != 32 {
		return fmt.Errorf("dbadmin: refusing to write signing key of length %d (want 32)", len(raw))
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("dbadmin: mkdir %q: %w", dir, err)
	}
	enc := base64.StdEncoding.EncodeToString(raw)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(enc), 0o600); err != nil {
		return fmt.Errorf("dbadmin: write %q: %w", tmp, err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("dbadmin: rename %q: %w", tmp, err)
	}
	return nil
}

// readSigningKeyFile reads + decodes a 32-byte signing key from path.
// Returns os.ErrNotExist (wrapped) when the file is absent so callers
// can branch on errors.Is.
func readSigningKeyFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// Reject world-readable files. Defense-in-depth: a wrong mode
	// almost certainly means the operator misconfigured ops tooling
	// and a fresh `cat` reads the key. Refuse to proceed.
	if fi, statErr := os.Stat(path); statErr == nil {
		if fi.Mode().Perm()&^0o600 != 0 {
			return nil, fmt.Errorf("dbadmin: audit signing key %q has mode %o, want 0600", path, fi.Mode().Perm())
		}
	}
	raw, err := base64.StdEncoding.DecodeString(string(bytesTrim(b)))
	if err != nil {
		return nil, fmt.Errorf("dbadmin: signing key %q is not valid base64: %w", path, err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("dbadmin: signing key %q decoded to %d bytes (want 32)", path, len(raw))
	}
	return raw, nil
}

// bytesTrim is a minimal trim of trailing newline/whitespace bytes that
// editors love to add. We hand-roll it to avoid pulling in strings/
// bytes for a one-off in a security-critical path.
func bytesTrim(b []byte) []byte {
	for len(b) > 0 {
		c := b[len(b)-1]
		if c == '\n' || c == '\r' || c == ' ' || c == '\t' {
			b = b[:len(b)-1]
			continue
		}
		break
	}
	return b
}

// Compile-time interface assertion.
var _ dbadmin.AuditSink = (*panelAudit)(nil)

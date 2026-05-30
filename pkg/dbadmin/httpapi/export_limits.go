package httpapi

import (
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// ErrEmptyUserID is returned by exportLockManager.tryAcquire when the
// caller passes an empty userID. The handler upstream rejects
// unauthenticated requests before reaching the lock, so an empty ID
// here is a programmer error (C11 / SEC-7, PR #16.5). Returning an
// error rather than panicking lets the handler emit a clean 500 with
// a logged stack trace instead of crashing the server.
var ErrEmptyUserID = errors.New("export: empty userID — authn upstream wiring missing")

// Export caps. These are the hard ceilings the export handler applies
// regardless of operator-supplied request limits. Each is a constant so
// tests can reference them; future PRs may move these onto
// dbadmin.Config.Query.* once an operator-tunable surface is needed.
const (
	// exportMaxRowsHardCap is the absolute ceiling on rows streamed
	// per export request. Requests asking for more are silently
	// clamped; the stream emits a truncation marker on hit.
	exportMaxRowsHardCap = 1_000_000

	// exportMaxBytesHardCap is the absolute ceiling on body bytes
	// streamed per export request (1 GiB).
	exportMaxBytesHardCap int64 = 1 << 30
)

// exportLockManager limits concurrent in-flight export requests per
// user. The mapping is userID → slot{busy + lastSeen}. The handler
// calls tryAcquire on entry and release on exit. tryAcquire is
// non-blocking; the handler returns 409 on contention with Retry-After
// so the client can serialize its own multi-tab usage.
//
// SEC-4 (PR #16.5): the slots map previously grew without bound — a
// federated IdP that rotates subject IDs would leak one map entry per
// user-session. Each release now stamps lastSeen; tryAcquire opportun-
// istically evicts entries whose slot is idle and lastSeen older than
// exportSlotIdleTTL. Cap exportSlotsMax bounds worst-case memory if
// every entry is fresh (LRU-by-lastSeen eviction).
const (
	// exportSlotIdleTTL is how long an idle slot survives in the map
	// before being eligible for opportunistic eviction.
	exportSlotIdleTTL = 1 * time.Hour
	// exportSlotsMax is the hard ceiling on map size; once reached the
	// LRU-by-lastSeen entry is evicted to make room for a new acquire.
	exportSlotsMax = 4096
)

type exportSlot struct {
	busy     atomic.Int32
	lastSeen atomic.Int64 // unix nanos
}

type exportLockManager struct {
	mu    sync.Mutex
	slots map[string]*exportSlot
	now   func() time.Time
}

func newExportLockManager() *exportLockManager {
	return &exportLockManager{
		slots: map[string]*exportSlot{},
		now:   time.Now,
	}
}

// tryAcquire returns (true, nil) when no other export is in-flight for
// userID and reserves the slot; release MUST be called when the handler
// returns. Returns (false, ErrEmptyUserID) when userID is empty — the
// authn layer rejects anonymous callers upstream so reaching here with
// "" is a wiring bug (C11 / SEC-7).
func (m *exportLockManager) tryAcquire(userID string) (bool, error) {
	if userID == "" {
		return false, ErrEmptyUserID
	}
	now := m.now()
	m.mu.Lock()
	m.evictLocked(now)
	slot, ok := m.slots[userID]
	if !ok {
		// Cap-enforced LRU eviction: when full, drop the
		// least-recently-released idle entry.
		if len(m.slots) >= exportSlotsMax {
			m.dropOldestIdleLocked()
		}
		slot = &exportSlot{}
		m.slots[userID] = slot
	}
	m.mu.Unlock()
	if !slot.busy.CompareAndSwap(0, 1) {
		return false, nil
	}
	slot.lastSeen.Store(now.UnixNano())
	return true, nil
}

// release frees the per-user slot. Safe to call multiple times. No-op
// for empty userID (defensive — should never happen post-acquire).
func (m *exportLockManager) release(userID string) {
	if userID == "" {
		return
	}
	m.mu.Lock()
	slot, ok := m.slots[userID]
	m.mu.Unlock()
	if !ok {
		return
	}
	slot.busy.Store(0)
	slot.lastSeen.Store(m.now().UnixNano())
}

// evictLocked drops idle entries whose lastSeen is older than
// exportSlotIdleTTL. Caller MUST hold m.mu.
func (m *exportLockManager) evictLocked(now time.Time) {
	cutoff := now.Add(-exportSlotIdleTTL).UnixNano()
	for k, s := range m.slots {
		if s.busy.Load() == 0 && s.lastSeen.Load() < cutoff {
			delete(m.slots, k)
		}
	}
}

// dropOldestIdleLocked removes the single oldest idle entry. Used by
// the cap-enforced path. Caller MUST hold m.mu.
func (m *exportLockManager) dropOldestIdleLocked() {
	var oldestKey string
	var oldestAt int64 = 0
	first := true
	for k, s := range m.slots {
		if s.busy.Load() != 0 {
			continue
		}
		ts := s.lastSeen.Load()
		if first || ts < oldestAt {
			oldestKey = k
			oldestAt = ts
			first = false
		}
	}
	if oldestKey != "" {
		delete(m.slots, oldestKey)
	}
}

// countingWriter wraps an io.Writer + counts bytes written. Used to
// enforce the byte cap on the streaming body without depending on the
// driver's row-byte estimate.
//
// C14 (PR #16.5): the byte count is held in an atomic.Int64 so Write
// from one goroutine and BytesWritten reads from another (e.g. a
// future progress-observer fanout) are race-free. The current export
// handler is single-goroutine; the atomic is cheap and removes a
// latent-race footgun.
type countingWriter struct {
	w       io.Writer
	n       atomic.Int64
	flusher interface{ Flush() }
}

func newCountingWriter(w io.Writer) *countingWriter {
	cw := &countingWriter{w: w}
	if f, ok := w.(interface{ Flush() }); ok {
		cw.flusher = f
	}
	return cw
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	return n, err
}

// BytesWritten reports the cumulative byte count.
func (c *countingWriter) BytesWritten() int64 { return c.n.Load() }

// Flush exposes the underlying flusher so the export encoders can
// preserve incremental delivery to the browser.
func (c *countingWriter) Flush() {
	if c.flusher != nil {
		c.flusher.Flush()
	}
}

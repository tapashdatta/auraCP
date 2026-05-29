package httpapi

import (
	"io"
	"sync"
	"sync/atomic"
)

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
// user. The mapping is userID → 1-slot semaphore. The handler calls
// tryAcquire on entry and release on exit. tryAcquire is non-blocking;
// the handler returns 409 on contention with Retry-After so the client
// can serialize its own multi-tab usage.
type exportLockManager struct {
	mu    sync.Mutex
	slots map[string]*atomic.Int32
}

func newExportLockManager() *exportLockManager {
	return &exportLockManager{slots: map[string]*atomic.Int32{}}
}

// tryAcquire returns true when no other export is in-flight for userID
// and reserves the slot; release MUST be called when the handler
// returns. Returns false when userID is empty (we don't gate anonymous
// callers since the authn layer rejects them upstream) — defensive
// no-op to avoid false 409s if a test path is missing user wiring.
func (m *exportLockManager) tryAcquire(userID string) bool {
	if userID == "" {
		return true
	}
	m.mu.Lock()
	slot, ok := m.slots[userID]
	if !ok {
		slot = &atomic.Int32{}
		m.slots[userID] = slot
	}
	m.mu.Unlock()
	return slot.CompareAndSwap(0, 1)
}

// release frees the per-user slot. Safe to call multiple times.
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
	slot.Store(0)
}

// countingWriter wraps an io.Writer + counts bytes written. Used to
// enforce the byte cap on the streaming body without depending on the
// driver's row-byte estimate.
type countingWriter struct {
	w       io.Writer
	n       int64
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
	c.n += int64(n)
	return n, err
}

// BytesWritten reports the cumulative byte count.
func (c *countingWriter) BytesWritten() int64 { return c.n }

// Flush exposes the underlying flusher so the export encoders can
// preserve incremental delivery to the browser.
func (c *countingWriter) Flush() {
	if c.flusher != nil {
		c.flusher.Flush()
	}
}

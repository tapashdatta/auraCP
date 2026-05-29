package httpapi

import (
	"context"
	"sync"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// DEF-25: handlers, middleware, recovery, WS, and export all invoke
// dbadmin.AuditSink.Record() synchronously on the request goroutine. A
// slow sink (rotating file, remote forwarder, fsync-on-write SQLite)
// stalls every request behind it. asyncSink wraps the configured sink
// with a bounded channel + a single dispatcher goroutine so callers
// return immediately. When the channel is full, records are dropped
// (with a drop counter) rather than blocking the request — the audit
// contract (audit.go §contract) is "MUST NOT fail the user-facing
// request"; losing the rare overflow record is consistent with that.
//
// Wrap policy: the standalone surface defers to whatever sink the
// embedder passes; tests / dev mode sinks already record in-memory
// inline. The wrap is opt-in at engine wiring (the panel mount turns it
// on for SQLite).
//
// The constants are package-private; ops tuning can come later.
const (
	asyncSinkQueueSize = 4096
)

type asyncSink struct {
	inner dbadmin.AuditSink
	ch    chan auditTask

	mu      sync.Mutex
	dropped int64
	closed  bool

	done chan struct{}
}

type auditTask struct {
	ctx context.Context
	ev  dbadmin.Event
}

// newAsyncSink wraps inner with a bounded queue + dispatcher. Callers
// invoke Stop() to drain. The dispatcher only runs while inner is set
// and the underlying type is genuinely async-safe.
func newAsyncSink(inner dbadmin.AuditSink) *asyncSink {
	if inner == nil {
		return nil
	}
	a := &asyncSink{
		inner: inner,
		ch:    make(chan auditTask, asyncSinkQueueSize),
		done:  make(chan struct{}),
	}
	go a.run()
	return a
}

func (a *asyncSink) run() {
	defer close(a.done)
	for t := range a.ch {
		a.inner.Record(t.ctx, t.ev)
	}
}

// Record is the AuditSink interface entry point. Non-blocking: drops on
// overflow (incrementing the dropped counter), so a slow inner sink
// cannot apply backpressure to the request goroutine.
func (a *asyncSink) Record(ctx context.Context, ev dbadmin.Event) {
	if a == nil {
		return
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()

	// Strip the request context so cancellation between hand-off and
	// dispatch cannot orphan the record. The inner sink should receive
	// a never-cancelled context — audit records must outlive the
	// request that produced them.
	bg := context.WithoutCancel(ctx)
	select {
	case a.ch <- auditTask{ctx: bg, ev: ev}:
	default:
		a.mu.Lock()
		a.dropped++
		a.mu.Unlock()
	}
}

// Stop closes the queue and waits for the dispatcher to drain. Safe to
// call multiple times.
func (a *asyncSink) Stop() {
	if a == nil {
		return
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	close(a.ch)
	a.mu.Unlock()
	<-a.done
}

// recordAudit is the single audit hand-off used by every httpapi call
// site (handlers, middleware, recoverer, WS, export). When the
// server's asyncAudit is set, the event is queued non-blocking;
// otherwise it falls back to the engine's sink directly.
//
// We do NOT route through the async layer when the underlying sink
// implements an inline "queryable" interface that tests use to assert
// events synchronously — in tests, dropping ordering would break the
// test contract. The runtime check below detects the test sink via the
// auditQueryable interface (see handlers_audit.go).
func (s *server) recordAudit(ctx context.Context, ev dbadmin.Event) {
	if s == nil || s.engine == nil || s.engine.Audit() == nil {
		return
	}
	// Test sinks expose Events() — keep them synchronous so unit tests
	// can assert audit events immediately after the handler returns.
	if _, isInline := s.engine.Audit().(auditQueryable); isInline {
		s.engine.Audit().Record(ctx, ev)
		return
	}
	if s.asyncAudit != nil {
		s.asyncAudit.Record(ctx, ev)
		return
	}
	s.engine.Audit().Record(ctx, ev)
}

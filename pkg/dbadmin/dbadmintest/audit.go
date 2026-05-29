package dbadmintest

import (
	"context"
	"sync"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// Audit is an in-memory dbadmin.AuditSink. Captures every event for later
// assertion. Safe for concurrent use.
type Audit struct {
	mu     sync.Mutex
	events []dbadmin.Event
}

// NewAudit constructs an empty Audit.
func NewAudit() *Audit {
	return &Audit{}
}

// Record appends the event to the in-memory log. Never fails.
func (a *Audit) Record(ctx context.Context, e dbadmin.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}

// Events returns a snapshot of every event recorded, in chronological
// order of Record calls. The returned slice is a defensive copy — the
// caller may iterate freely without holding a lock.
func (a *Audit) Events() []dbadmin.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]dbadmin.Event, len(a.events))
	copy(out, a.events)
	return out
}

// EventsByAction returns every event whose Action matches.
func (a *Audit) EventsByAction(action dbadmin.Action) []dbadmin.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []dbadmin.Event{}
	for _, e := range a.events {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

// Reset clears every captured event. Useful between test cases that
// share an Audit instance.
func (a *Audit) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = nil
}

// Len returns the number of events recorded so far.
func (a *Audit) Len() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.events)
}

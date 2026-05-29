package dbadmin

import (
	"sync"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// stepUpKey identifies a single step-up window. Keyed on the panel
// session token (so logout revokes step-up flags implicitly via session
// deletion) and the action — we key by action rather than an action
// class because pkg/dbadmin.Action does not expose a public Class()
// method. Each step-up grant covers exactly one action.
type stepUpKey struct {
	session string
	action  dbadmin.Action
}

// stepUpStore is the in-memory step-up flag store. Single-process; if HA
// becomes a goal, move this to the sessions table.
type stepUpStore struct {
	mu      sync.Mutex
	entries map[stepUpKey]time.Time

	// reaper state.
	stopCh chan struct{}
	once   sync.Once
}

func newStepUpStore() *stepUpStore {
	s := &stepUpStore{
		entries: map[stepUpKey]time.Time{},
		stopCh:  make(chan struct{}),
	}
	go s.reaperLoop()
	return s
}

// set records a step-up flag with the given TTL. Replacing an existing
// flag is allowed (the operator just re-stepped-up).
func (s *stepUpStore) set(session string, action dbadmin.Action, ttl time.Duration) {
	if session == "" || action == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[stepUpKey{session: session, action: action}] = time.Now().Add(ttl)
}

// has reports whether a non-expired step-up flag exists.
func (s *stepUpStore) has(session string, action dbadmin.Action) bool {
	if session == "" || action == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.entries[stepUpKey{session: session, action: action}]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.entries, stepUpKey{session: session, action: action})
		return false
	}
	return true
}

// stop terminates the reaper goroutine. Safe to call multiple times.
func (s *stepUpStore) stop() {
	s.once.Do(func() { close(s.stopCh) })
}

func (s *stepUpStore) reaperLoop() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case now := <-t.C:
			s.mu.Lock()
			for k, exp := range s.entries {
				if now.After(exp) {
					delete(s.entries, k)
				}
			}
			s.mu.Unlock()
		}
	}
}

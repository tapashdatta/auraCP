package dbadmin

import (
	"sync"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// stepUpKey identifies a single step-up window. Keyed on the panel
// session token (so logout revokes step-up flags implicitly via session
// deletion + the explicit InvalidateSession hook below), the
// dbadmin.ActionClass (so one approval covers sibling actions in the
// class), and the connection id (so a step-up for connection A does
// not authorize a destructive action on connection B).
//
// PR #10.5:
//   - FIX-SDK-3: previously keyed on raw dbadmin.Action; now on
//     ActionClass (one approval per class instead of per action).
//   - FIX-INT-12: previously did not include connection id; now does,
//     so a per-connection class flag is per-connection.
//   - FIX-PD-SEC-04: stale step-up flags used to survive panel logout
//     until the TTL expired or the reaper ran. InvalidateSession
//     surgically deletes every entry for a logged-out session.
type stepUpKey struct {
	session string
	class   dbadmin.ActionClass
	connID  string
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

// setClass records a step-up flag with the given TTL keyed by
// (session, action class, connection id). class == ActionClassNone
// is treated as a no-op since by definition no step-up is required.
func (s *stepUpStore) setClass(session string, class dbadmin.ActionClass, connID string, ttl time.Duration) {
	if session == "" || class == dbadmin.ActionClassNone {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[stepUpKey{session: session, class: class, connID: connID}] = time.Now().Add(ttl)
}

// hasClass reports whether a non-expired step-up flag exists for the
// (session, class, connection id) tuple.
func (s *stepUpStore) hasClass(session string, class dbadmin.ActionClass, connID string) bool {
	if session == "" || class == dbadmin.ActionClassNone {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := stepUpKey{session: session, class: class, connID: connID}
	exp, ok := s.entries[k]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.entries, k)
		return false
	}
	return true
}

// InvalidateSession drops every step-up flag bound to the given panel
// session token. Wired into the panel's POST /api/auth/logout path
// (FIX-PD-SEC-04). Without it, a logout-then-relogin operator could
// briefly inherit step-up windows from a previous session because the
// session token is per-login.
//
// Exported so the panel api package can call it without reaching into
// package internals.
func (s *stepUpStore) InvalidateSession(session string) {
	if session == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.entries {
		if k.session == session {
			delete(s.entries, k)
		}
	}
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

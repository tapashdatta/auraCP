package httpapi

import "sync"

// perUserQueryCap is the per-(user) ceiling on concurrent /query and
// /sql/stream requests. The driver pool is sized at PoolSizePerConn (~4)
// per connection; without this gate a single user can fill the pool and
// starve everyone else. The cap is intentionally higher than the pool
// size so legitimate burst use (open editor + run-on-paste) doesn't
// 429, but the per-second mutating limit (10/s) keeps a misbehaving
// client from sustaining the burst.
const perUserQueryCap = 16

// userSemaphore is a per-user counting semaphore. acquire returns true
// when a slot is available and reserves it; release returns the slot.
// The map grows with user-id churn — DEF-32 limits buckets to the
// limiter's policy (we share the user-id space).
type userSemaphore struct {
	mu     sync.Mutex
	cap    int
	counts map[string]int
}

func newUserSemaphore(cap int) *userSemaphore {
	if cap <= 0 {
		cap = perUserQueryCap
	}
	return &userSemaphore{cap: cap, counts: map[string]int{}}
}

// acquire returns true when a slot is available for user. When user is
// empty (unauthenticated path — defensive), the call is a no-op true.
func (s *userSemaphore) acquire(user string) bool {
	if s == nil || user == "" {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.counts[user] >= s.cap {
		return false
	}
	s.counts[user]++
	return true
}

func (s *userSemaphore) release(user string) {
	if s == nil || user == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.counts[user] > 0 {
		s.counts[user]--
	}
	if s.counts[user] == 0 {
		delete(s.counts, user)
	}
}

package httpapi

import (
	"container/list"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// limiter is a per-(class, user) rate limiter. Three classes:
//
//   - reading  (50 req/s burst 100)
//   - mutating (10 req/s burst 20)
//   - step-up  (10 verify attempts / 15 minutes sliding window, per
//     SECURITY.md §4.4); enforced via a separate per-key fixed-window
//     counter on top of the token bucket so brute-force attempts on
//     /step-up/verify get gated even when the per-second burst is well
//     under the global ceiling.
//
// DEF-7: buckets evict LRU-style at limiterMaxEntries to keep the map
// from growing unbounded under user-id churn.
//
// DEF-21: key namespaces use a separator character ('\x1f' — ASCII unit
// separator) that cannot appear inside a user ID. The previous "r:" /
// "w:" prefixes collided with user IDs starting with "r:" or "w:".
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*list.Element // key -> *list.Element holding *limiterBucket
	lru     *list.List

	readRate   rate.Limit
	readBurst  int
	writeRate  rate.Limit
	writeBurst int
	stepRate   rate.Limit
	stepBurst  int

	// step-up sliding window. Configured for SECURITY.md §4.4:
	// at most stepWindowCap successful Allow() calls per stepWindow
	// duration per (user) key. We track a small ring of recent
	// allowances to enforce.
	stepWindow    time.Duration
	stepWindowCap int
}

// limiterBucket is one entry in the LRU cache. The token-bucket limiter
// gates per-second rate; stepStamps tracks the sliding window for the
// step-up class.
type limiterBucket struct {
	key        string
	bucket     *rate.Limiter
	class      rateLimitClass
	stepStamps []time.Time // only populated for rateClassStepUp
}

// limiterMaxEntries caps the resident bucket count. Past the cap, oldest
// buckets are evicted on each insert. 10K is roughly: a token-bucket
// limiter is ~80 bytes, the LRU element ~64 bytes; cap is ~1 MiB.
const limiterMaxEntries = 10_000

// keySep separates the class label from the user ID inside a bucket
// key. ASCII unit-separator (0x1F) is a control byte that cannot appear
// in a realistic user ID, eliminating the "r:" / "w:" collision pointed
// out by DEF-21.
const keySep = "\x1f"

func newLimiter() *limiter {
	return &limiter{
		buckets:       map[string]*list.Element{},
		lru:           list.New(),
		readRate:      50,
		readBurst:     100,
		writeRate:     10,
		writeBurst:    20,
		stepRate:      1, // 1 req/s fill rate for step-up
		stepBurst:     5, // small burst so legitimate retries don't 429
		stepWindow:    15 * time.Minute,
		stepWindowCap: 10,
	}
}

// allow takes one token for the given user + class. Returns true when
// the request may proceed.
func (l *limiter) allow(user string, class rateLimitClass) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	k := key(user, class)
	el, ok := l.buckets[k]
	if !ok {
		b := newBucketForClass(l, k, class)
		el = l.lru.PushFront(b)
		l.buckets[k] = el
		l.evictIfFull()
	} else {
		l.lru.MoveToFront(el)
	}
	b := el.Value.(*limiterBucket)

	// Token bucket first.
	if !b.bucket.Allow() {
		return false
	}

	// Step-up: secondary sliding window. SECURITY.md §4.4 mandates 10
	// /15min — far below the per-second burst, so the secondary gate is
	// what actually enforces the policy.
	if class == rateClassStepUp {
		now := time.Now()
		cutoff := now.Add(-l.stepWindow)
		// drop expired stamps from the front
		i := 0
		for i < len(b.stepStamps) && b.stepStamps[i].Before(cutoff) {
			i++
		}
		if i > 0 {
			b.stepStamps = append(b.stepStamps[:0], b.stepStamps[i:]...)
		}
		if len(b.stepStamps) >= l.stepWindowCap {
			return false
		}
		b.stepStamps = append(b.stepStamps, now)
	}
	return true
}

// newBucketForClass returns a new limiterBucket pre-configured for the
// given class. The token-bucket fill rate / burst comes from the
// per-class fields on the parent limiter.
func newBucketForClass(l *limiter, k string, class rateLimitClass) *limiterBucket {
	var (
		r     rate.Limit
		burst int
	)
	switch class {
	case rateClassMutating:
		r, burst = l.writeRate, l.writeBurst
	case rateClassStepUp:
		r, burst = l.stepRate, l.stepBurst
	default:
		r, burst = l.readRate, l.readBurst
	}
	return &limiterBucket{
		key:    k,
		bucket: rate.NewLimiter(r, burst),
		class:  class,
	}
}

// evictIfFull removes the oldest bucket once the map exceeds the cap.
// Called from allow() under l.mu.
func (l *limiter) evictIfFull() {
	for l.lru.Len() > limiterMaxEntries {
		oldest := l.lru.Back()
		if oldest == nil {
			return
		}
		b := oldest.Value.(*limiterBucket)
		delete(l.buckets, b.key)
		l.lru.Remove(oldest)
	}
}

// key builds the per-(class, user) bucket key. The class portion lives
// before keySep, which cannot appear inside `user`.
func key(user string, class rateLimitClass) string {
	var prefix string
	switch class {
	case rateClassMutating:
		prefix = "w"
	case rateClassStepUp:
		prefix = "s"
	default:
		prefix = "r"
	}
	return prefix + keySep + user
}

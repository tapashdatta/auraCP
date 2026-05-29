package httpapi

import (
	"sync"

	"golang.org/x/time/rate"
)

// limiter is a per-(user, class) token-bucket rate limiter. Two classes:
// reading (50 req/s burst 100) and mutating (10 req/s burst 20). The
// `key()` helper namespaces empty user IDs (unauthenticated requests
// shouldn't reach here, but if they do they share one bucket).
type limiter struct {
	mu       sync.Mutex
	buckets  map[string]*rate.Limiter
	readRate rate.Limit
	readBurst int
	writeRate rate.Limit
	writeBurst int
}

func newLimiter() *limiter {
	return &limiter{
		buckets:    map[string]*rate.Limiter{},
		readRate:   50,
		readBurst:  100,
		writeRate:  10,
		writeBurst: 20,
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
	b, ok := l.buckets[k]
	if !ok {
		var r rate.Limit
		var burst int
		switch class {
		case rateClassMutating:
			r, burst = l.writeRate, l.writeBurst
		default:
			r, burst = l.readRate, l.readBurst
		}
		b = rate.NewLimiter(r, burst)
		l.buckets[k] = b
	}
	return b.Allow()
}

func key(user string, class rateLimitClass) string {
	prefix := "r:"
	if class == rateClassMutating {
		prefix = "w:"
	}
	return prefix + user
}

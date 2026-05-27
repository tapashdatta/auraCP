package api

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/auracp/auracp/internal/auth"
)

const csrfCookie = "auracp_csrf"

// Secure wraps the whole handler with baseline protections:
//   - security headers on every response
//   - a CSRF double-submit token (cookie + matching X-CSRF-Token header on writes)
//   - login rate-limiting per client IP
func Secure(next http.Handler) http.Handler {
	rl := newRateLimiter(10, 5*time.Minute) // 10 login attempts / 5 min / IP
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		securityHeaders(w)
		ensureCSRFCookie(w, r)

		if isLoginAttempt(r) && !rl.allow(clientIP(r)) {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many attempts, slow down"})
			return
		}
		if isUnsafe(r) && strings.HasPrefix(r.URL.Path, "/api/") && !csrfOK(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "invalid CSRF token"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Content-Security-Policy",
		"default-src 'self'; "+
			"script-src 'self'; "+
			"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; "+
			"font-src 'self' https://fonts.gstatic.com; "+
			"img-src 'self' data:; "+
			"connect-src 'self'; "+
			"frame-ancestors 'none'; base-uri 'self'")
}

func isUnsafe(r *http.Request) bool {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func isLoginAttempt(r *http.Request) bool {
	return r.Method == http.MethodPost &&
		(r.URL.Path == "/api/auth/login" || r.URL.Path == "/api/auth/mfa/verify")
}

func ensureCSRFCookie(w http.ResponseWriter, r *http.Request) {
	if _, err := r.Cookie(csrfCookie); err == nil {
		return
	}
	tok, err := auth.RandomToken()
	if err != nil {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookie, Value: tok, Path: "/",
		HttpOnly: false, // readable by the SPA so it can echo it back
		SameSite: http.SameSiteLaxMode, Secure: isTLS(r),
	})
	// make it available to this request too
	r.AddCookie(&http.Cookie{Name: csrfCookie, Value: tok})
}

func csrfOK(r *http.Request) bool {
	c, err := r.Cookie(csrfCookie)
	if err != nil || c.Value == "" {
		return false
	}
	return r.Header.Get("X-CSRF-Token") == c.Value
}

func isTLS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i > 0 {
		host = host[:i]
	}
	return host
}

// ---- tiny fixed-window rate limiter ----

type rateLimiter struct {
	mu     sync.Mutex
	hits   map[string][]time.Time
	max    int
	window time.Duration
}

func newRateLimiter(max int, window time.Duration) *rateLimiter {
	return &rateLimiter{hits: map[string][]time.Time{}, max: max, window: window}
}

func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-rl.window)
	kept := rl.hits[key][:0]
	for _, t := range rl.hits[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= rl.max {
		rl.hits[key] = kept
		return false
	}
	rl.hits[key] = append(kept, now)
	return true
}

package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"runtime/debug"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// requestIDRE is the validation regex for inbound X-Request-Id headers.
// Anything failing the pattern is regenerated to prevent log-injection.
var requestIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// middleware is a tiny chain helper.
type middleware func(http.Handler) http.Handler

// chain composes middlewares right-to-left so the first middleware in the
// slice runs first on the request path.
func chain(h http.Handler, mw ...middleware) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

// requestID generates a fresh ID per request (or reuses an inbound one
// matching the safe pattern), sets the X-Request-Id response header, and
// stashes the value on the context.
func requestID() middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get("X-Request-Id")
			if !requestIDRE.MatchString(id) {
				id = newRequestID()
			}
			w.Header().Set("X-Request-Id", id)
			ctx := context.WithValue(r.Context(), ctxRequestID, id)
			ctx = context.WithValue(ctx, ctxStartTime, time.Now())
			ctx = context.WithValue(ctx, ctxAuditState, &auditState{})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// newRequestID returns a fresh, log-safe random ID.
func newRequestID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "req_" + base64.RawURLEncoding.EncodeToString(b[:])
}

// recoverer traps panics, emits 500 with the canonical envelope, and logs
// the stack with the request ID. Audit-best-effort.
func recoverer(s *server) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					stack := debug.Stack()
					_ = stack // kept for future logger wiring; not echoed to client
					if s != nil && s.engine != nil {
						// Emit a panic audit record best-effort.
						st := auditFrom(r.Context())
						st.suppress = true
						user, _ := userFrom(r.Context())
						s.engine.Audit().Record(r.Context(), dbadmin.Event{
							EventID:       newRequestID(),
							Timestamp:     time.Now().UTC(),
							UserID:        user.ID,
							SourceIP:      clientIP(r),
							UserAgentHash: uaHash(r),
							Action:        dbadmin.Action("panic"),
							Error:         fmt.Sprintf("panic: %v", rec),
						})
					}
					writeError(w, r, http.StatusInternalServerError, CodeInternal, "internal error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// perRouteTimeout wraps r.Context() with a deadline. Handlers that need
// to surface deadline-exceeded as 504 inspect ctx.Err().
func perRouteTimeout(d time.Duration) middleware {
	return func(next http.Handler) http.Handler {
		if d <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// maxBody installs http.MaxBytesReader on the request body.
func maxBody(n int64) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}

// authn runs Engine.Auth.Authenticate on every request and stashes the
// resolved User on the context. Returns 401 (nil user / ErrUnauthenticated)
// or 500 (I/O failure). Every denial emits an audit record (SECURITY.md
// §9.1: auth.login / auth.failed must be auditable; the outer audit()
// middleware never fires on this code path because we short-circuit
// before reaching it).
func authn(s *server) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, err := s.engine.AuthSurface().Authenticate(r)
			if err != nil {
				if errors.Is(err, dbadmin.ErrUnauthenticated) {
					emitDenialAudit(s, r, dbadmin.Action("auth.denied"), "unauthenticated")
					writeError(w, r, http.StatusUnauthorized, CodeUnauthenticated, "authentication required")
					return
				}
				emitDenialAudit(s, r, dbadmin.Action("auth.error"), "authn-io-error")
				writeError(w, r, http.StatusInternalServerError, CodeInternal, "authentication failed")
				return
			}
			if user.ID == "" {
				emitDenialAudit(s, r, dbadmin.Action("auth.denied"), "empty-user")
				writeError(w, r, http.StatusUnauthorized, CodeUnauthenticated, "authentication required")
				return
			}
			ctx := context.WithValue(r.Context(), ctxUser, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// csrf enforces double-submit token on every non-safe method. Token is
// the X-Aura-Csrf header byte-compared to the __Host-aura_csrf cookie.
//
// The WS upgrade route is excluded; it validates the token via the
// subprotocol header instead.
func csrf(s *server) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			if s != nil && s.csrfDisabled {
				next.ServeHTTP(w, r)
				return
			}
			header := r.Header.Get("X-Aura-Csrf")
			cookie, err := r.Cookie("__Host-aura_csrf")
			if err != nil || cookie == nil || header == "" {
				emitDenialAudit(s, r, dbadmin.Action("csrf.denied"), "missing-token")
				writeError(w, r, http.StatusForbidden, CodeCSRFRejected, "missing CSRF token")
				return
			}
			if subtle.ConstantTimeCompare([]byte(header), []byte(cookie.Value)) != 1 {
				emitDenialAudit(s, r, dbadmin.Action("csrf.denied"), "token-mismatch")
				writeError(w, r, http.StatusForbidden, CodeCSRFRejected, "CSRF token mismatch")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// audit emits the audit event after the handler returns. Uses the
// per-request auditState (populated by handlers via setAuditAction etc).
func audit(s *server) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			st := auditFrom(r.Context())
			if st.suppress || st.action == "" {
				return
			}
			user, _ := userFrom(r.Context())
			started := startTimeFrom(r.Context())
			var dur int64
			if !started.IsZero() {
				dur = time.Since(started).Milliseconds()
			}
			event := dbadmin.Event{
				EventID:       newRequestID(),
				Timestamp:     time.Now().UTC(),
				UserID:        user.ID,
				SourceIP:      clientIP(r),
				UserAgentHash: uaHash(r),
				Action:        st.action,
				Target:        st.target,
				Statement:     st.statement,
				ResultRows:    st.rows,
				DurationMS:    dur,
				Error:         st.err,
				StepUpJTI:     st.stepUpJTI,
			}
			if conn := connIDFrom(r.Context()); conn != "" && event.Target.ConnectionID == "" {
				event.Target.ConnectionID = conn
			}
			// Role-at-time best-effort: user.Roles map keyed by conn id.
			if cid := event.Target.ConnectionID; cid != "" {
				event.UserRoleAtTime = user.Roles[cid]
			}
			s.engine.Audit().Record(r.Context(), event)
		})
	}
}

// emitDenialAudit emits an audit event from a middleware that is about
// to short-circuit before the outer audit() runs. SECURITY.md §9.1
// requires every authn / authz / CSRF / rate-limit denial to be
// forensically visible. We use synthetic Action values ("auth.denied",
// "csrf.denied", "ratelimit.denied") to keep these distinguishable from
// real handler actions in the audit log.
func emitDenialAudit(s *server, r *http.Request, action dbadmin.Action, reason string) {
	if s == nil || s.engine == nil || s.engine.Audit() == nil {
		return
	}
	st := auditFrom(r.Context())
	if st.suppress {
		return
	}
	user, _ := userFrom(r.Context())
	s.engine.Audit().Record(r.Context(), dbadmin.Event{
		EventID:       newRequestID(),
		Timestamp:     time.Now().UTC(),
		UserID:        user.ID,
		SourceIP:      clientIP(r),
		UserAgentHash: uaHash(r),
		Action:        action,
		Error:         reason,
	})
	// Suppress the outer audit() from re-emitting — denial events
	// are atomic and should not be paired with a "successful" event
	// from a downstream layer that never ran.
	st.suppress = true
}

// rateLimitClass is the route's class for rate-limit bucket selection.
type rateLimitClass int

const (
	rateClassReading rateLimitClass = iota
	rateClassMutating
)

// rateLimit installs the per-(user, class) token-bucket limiter.
func rateLimit(s *server, class rateLimitClass) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s == nil || s.limiter == nil {
				next.ServeHTTP(w, r)
				return
			}
			user, _ := userFrom(r.Context())
			if !s.limiter.allow(user.ID, class) {
				emitDenialAudit(s, r, dbadmin.Action("ratelimit.denied"), "exceeded")
				w.Header().Set("Retry-After", "1")
				writeError(w, r, http.StatusTooManyRequests, CodeRateLimited, "rate limit exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// shutdownGate returns 503 once the engine is shutting down.
func shutdownGate(s *server) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.engine.IsShuttingDown() {
				writeError(w, r, http.StatusServiceUnavailable, CodeUnavailable, "engine shutting down")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func uaHash(r *http.Request) string {
	ua := r.Header.Get("User-Agent")
	if ua == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(ua))
	return hex.EncodeToString(sum[:8])
}

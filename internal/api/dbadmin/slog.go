package dbadmin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
)

// PR #10.5 / FIX-INT-9: shared request-id middleware.
//
// Pre-PR-#10.5 the panel (log.Printf at the api.Secure layer) and the
// dbadmin engine (slog.Default() through the standalone audit sink)
// wrote to two different logger styles, with no shared correlation key.
// An operator chasing a 500 had to grep two formats, cross-correlate by
// wall-clock, and hope the request didn't span the second boundary.
//
// requestIDMiddleware mints a short hex correlation token, stamps it
// into a request-scoped context, and echoes it back in the X-Request-Id
// response header. Downstream code (the panel audit mirror, the
// FileAuditSink chain entry, the slog lines emitted by handlers) pulls
// the same id off the context via RequestIDFromContext. The id is also
// visible to the operator's curl output / browser DevTools, so support
// can paste it back to dev for grepping.

type ctxKey int

const ctxKeyReqID ctxKey = 0

// RequestIDHeader is the response header carrying the engine-minted
// request id. Mirrors the convention used by NGINX (X-Request-ID, with
// our preferred mixed case) and by AWS / GCP load balancers.
const RequestIDHeader = "X-Request-Id"

// WithRequestIDMiddleware wraps the dbadmin handler so every request
// flowing through Mount gets a stable correlation id available to:
//
//   - panelAudit.Record (snapshotted at enqueue time → propagated to
//     drainMirror's slog warn line and the audit_log row's detail JSON
//     via the engine's Event, if the upstream classifier sets it);
//   - any slog line a handler chooses to emit (slog.With("req_id", id))
//     for a coherent journal trace.
//
// The middleware is wired in Mount() between httpapi.NewWithOptions and
// the mux registration so EVERY /api/dbadmin/* request gets an id even
// if the engine handler short-circuits (401, 503, etc).
func WithRequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if id == "" {
			id = newRequestID()
		}
		w.Header().Set(RequestIDHeader, id)
		ctx := context.WithValue(r.Context(), ctxKeyReqID, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestIDFromContext extracts the request id stamped by
// WithRequestIDMiddleware. Returns "" when no id is present, so callers
// can defensively use it without nil-checks.
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(ctxKeyReqID).(string); ok {
		return v
	}
	return ""
}

// SharedLogger returns a slog.Logger pre-stamped with the package label
// so panel-side and dbadmin-side log lines share a "comp" attribute and
// can be filtered together. Useful as the second arg to newPanelAudit.
//
// Hosts that already configured slog.SetDefault to a JSON / structured
// handler get that handler verbatim; we just attach the component
// attribute.
func SharedLogger() *slog.Logger {
	return slog.Default().With("comp", "dbadmin")
}

// newRequestID returns 8 hex bytes (16 chars). 64 bits of entropy is
// plenty to make collisions vanishingly rare over the daemon's
// lifetime, and short enough to scan visually in a journal line.
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read on Linux only fails on a misconfigured kernel; in
		// that case any fixed-but-unique-per-process token is fine
		// because the daemon is about to fall over anyway.
		return "noreqid"
	}
	return hex.EncodeToString(b[:])
}

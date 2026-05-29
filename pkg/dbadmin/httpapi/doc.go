// Package httpapi is the REST + WebSocket wire surface for Aura DB. It is
// the concrete implementation that pkg/dbadmin.Engine.Handler() returns.
//
// The router uses net/http's pattern matching (Go 1.22+) for method + path
// routing. Middleware composes via a tiny inline chain helper. The
// WebSocket endpoint uses gorilla/websocket; rate limiting uses
// golang.org/x/time/rate.
//
// Security posture:
//   - Auth required on every route (no public endpoints).
//   - CSRF: double-submit token on every mutating request, single-use
//     handshake token in the WS subprotocol.
//   - Body size capped (1 MiB by default; 64 MiB on /import).
//   - Per-route timeouts via context.WithTimeout (NOT http.TimeoutHandler).
//   - SQL re-classified on every executing route.
//   - Errors emit the canonical envelope { error: { code, message, request_id } }
//     per SDK.md §7.
//   - Connection responses are redacted: no password/key bytes are echoed.
//   - Audit emitted on every mutating action and every authn/authz/classifier
//     denial.
package httpapi

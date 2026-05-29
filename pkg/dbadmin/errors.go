package dbadmin

import "errors"

// Sentinel errors returned across the SDK surface. These values are
// semver-stable: hosts may compare with errors.Is and depend on the
// behavior never changing.

var (
	// ErrUnauthenticated indicates the request carries no valid
	// session. Auth.Authenticate returns this when there's no cookie /
	// header / token, when the token is forged or expired, or when
	// session binding (IP class / UA hash) fails. The engine maps it
	// to HTTP 401.
	ErrUnauthenticated = errors.New("dbadmin: not authenticated")

	// ErrStepUpRequired indicates the request is authenticated but the
	// action requires a fresh MFA verification the user has not
	// performed (or whose flag has expired). The engine maps it to
	// HTTP 403 with a body that signals "step-up required" + the
	// action class so the frontend can initiate the right MFA flow.
	ErrStepUpRequired = errors.New("dbadmin: step-up required")

	// ErrForbidden indicates the user is authenticated but lacks the
	// role / permission for this action. The engine maps it to HTTP
	// 404 (NOT 403) for connection-scoped actions, to avoid leaking
	// the existence of connections the user has no grant on. For
	// global actions (where existence isn't a concern), 403 is used.
	ErrForbidden = errors.New("dbadmin: forbidden")

	// ErrNotFound indicates the addressed resource doesn't exist. The
	// engine maps it to HTTP 404.
	//
	// Connection-scoped not-found and forbidden are deliberately
	// indistinguishable from the client's perspective: both surface as
	// 404 with the same error body. See SECURITY.md §10.3.
	ErrNotFound = errors.New("dbadmin: not found")

	// ErrConflict indicates the requested state change conflicts with
	// the current state (e.g., trying to create a connection with a
	// name that already exists, or trying to delete a connection while
	// queries are in flight). Maps to HTTP 409.
	ErrConflict = errors.New("dbadmin: conflict")

	// ErrInvalidInput indicates the request body or query parameters
	// failed validation. Maps to HTTP 400. The error message describes
	// which field is invalid; it's safe to surface verbatim to the
	// operator.
	ErrInvalidInput = errors.New("dbadmin: invalid input")

	// ErrInternal indicates an unexpected condition: a panic recovered
	// from a downstream impl, an I/O error from a store call, etc. The
	// engine maps it to HTTP 500 and emits a request-ID for the
	// operator to share with support. The verbatim message is logged
	// server-side and NEVER surfaced to the client.
	ErrInternal = errors.New("dbadmin: internal error")
)

// Error is the wire shape of every API error response. The Code field is
// semver-stable; the Message field is human-readable and may change. The
// RequestID is the engine's correlation ID, useful for grepping the
// audit log.
type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

// Error satisfies the error interface so an *Error can be returned from
// handlers and bubble up through the engine's error path.
func (e *Error) Error() string {
	if e.RequestID != "" {
		return e.Code + ": " + e.Message + " (req " + e.RequestID + ")"
	}
	return e.Code + ": " + e.Message
}

// Code strings emitted in API error responses. Each is semver-stable.
const (
	CodeUnauthenticated  = "unauthenticated"
	CodeStepUpRequired   = "step-up-required"
	CodeForbidden        = "forbidden"
	CodeNotFound         = "not-found"
	CodeConflict         = "conflict"
	CodeInvalidInput     = "invalid-input"
	CodeRateLimited      = "rate-limited"
	CodeQueryForbidden   = "query-forbidden"
	CodeQueryTimeout     = "query-timeout"
	CodeResultCapped     = "result-capped"
	CodeInternal         = "internal-error"
	CodeUnavailable      = "unavailable"
)

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/classifier"
	"github.com/auracp/auracp/pkg/dbadmin/driver"
	"github.com/auracp/auracp/pkg/dbadmin/history"
	"github.com/auracp/auracp/pkg/dbadmin/rows"
	"github.com/auracp/auracp/pkg/dbadmin/schema"
)

// Canonical error codes used in the wire envelope. kebab-case per
// SDK.md §7 (the form is semver-stable and matches the codes in
// pkg/dbadmin/errors.go — these handler-layer codes extend that
// foundation with httpapi-specific failures).
//
// DEF-35: these constants ARE the public wire surface, not internal
// implementation. They are exported so SDK consumers writing Go can
// switch on them by name; the kebab-case string value is the wire
// form. Renaming a constant is a breaking change — the wire shipped
// in PR #8 freezes both the identifier and the value. Future codes
// must be appended; never renamed or removed without a major bump.
const (
	CodeUnauthenticated    = "unauthenticated"
	CodeForbidden          = "forbidden"
	CodeStepUpRequired     = "step-up-required"
	CodeNotFound           = "not-found"
	CodeConflict           = "conflict"
	CodeInvalidInput       = "invalid-input"
	CodeInvalidJSON        = "invalid-json"
	CodeInvalidIdentifier  = "invalid-identifier"
	CodeInvalidPredicate   = "invalid-predicate"
	CodeEmptyUpdate        = "empty-update"
	CodePKMismatch         = "pk-mismatch"
	CodeNoPrimaryKey       = "no-primary-key"
	CodeBodyTooLarge       = "body-too-large"
	CodeStatementTooLarge  = "statement-too-large"
	CodeForbiddenStatement = "forbidden-statement"
	CodeRowCapExceeded     = "row-cap-exceeded"
	CodeResultCapped       = "result-capped"
	CodeRateLimited        = "rate-limited"
	CodeBackendAuthFailed  = "backend-auth-failed"
	CodeBackendUnavailable = "backend-unavailable"
	CodeBackendTimeout     = "backend-timeout"
	CodeBackendConflict    = "backend-conflict"
	CodeBackendNotFound    = "backend-not-found"
	CodeBackendPermission  = "backend-permission-denied"
	CodeSyntaxError        = "syntax-error"
	CodeTimeout            = "timeout"
	CodeClientClosed       = "client-closed"
	CodeHistoryStoreClosed = "history-store-closed"
	CodeUnavailable        = "unavailable"
	CodeInternal           = "internal-error"
	CodeNotImplemented     = "not-implemented"
	CodeOriginRejected     = "origin-rejected"
	CodeCSRFRejected       = "csrf-rejected"
	// CodeSlowLogUnavailable is emitted by the slow-log WS handler
	// when the backend's prerequisites are missing
	// (slow_query_log=OFF, log_output=FILE, pg_stat_statements not
	// installed). The error frame's Message carries the operator-
	// actionable hint string verbatim.
	CodeSlowLogUnavailable = "slowlog-unavailable"
)

// errorEnvelope is the canonical wire shape for every error response.
// Mirrors SDK.md §7 exactly: {"error":{"code","message","request_id"}}.
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
	Details   any    `json:"details,omitempty"`
}

// writeError serializes the canonical error envelope at the given status.
// requestID is read from ctx; an empty value is emitted unchanged.
//
// DEF-20: when the response has already been written (mid-stream
// failure), the second WriteHeader call logs "superfluous WriteHeader"
// and silently drops the new status. We detect this via the
// auditingWriter wrapper installed by audit() middleware (which tracks
// emitted status). When the response was already started, we skip the
// header write but still attempt to push the envelope onto the body so
// the SDK can surface the error tail; if even that fails, the trailer
// channel is the canonical signal.
func writeError(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	if aw, ok := w.(*auditingWriter); ok && aw.status != 0 {
		// Status already emitted — emit body envelope only.
		_ = json.NewEncoder(w).Encode(errorEnvelope{
			Error: errorBody{
				Code:      code,
				Message:   msg,
				RequestID: requestIDFrom(r.Context()),
			},
		})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{
		Error: errorBody{
			Code:      code,
			Message:   msg,
			RequestID: requestIDFrom(r.Context()),
		},
	})
}

// writeErrorDetails is like writeError but attaches a structured details
// payload (e.g. {"forbiddenPatterns": [...]}). Use sparingly — leaking
// structured details about the failure mode is sometimes a security
// problem; only attach when the client genuinely needs it (forbidden-
// classifier match list, conflict pointer).
func writeErrorDetails(w http.ResponseWriter, r *http.Request, status int, code, msg string, details any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{
		Error: errorBody{
			Code:      code,
			Message:   msg,
			RequestID: requestIDFrom(r.Context()),
			Details:   details,
		},
	})
}

// mapErr classifies a backend / SDK error into a (HTTP status, error code,
// public message) tuple. Returns (500, CodeInternal, "internal error") for
// anything it doesn't recognize. The caller passes the resulting tuple to
// writeError. The full err is NOT echoed in the public message — it is
// logged server-side at the caller's discretion.
func mapErr(err error) (status int, code string, msg string) {
	switch {
	case err == nil:
		return http.StatusOK, "", ""

	// dbadmin sentinels
	case errors.Is(err, dbadmin.ErrUnauthenticated):
		return http.StatusUnauthorized, CodeUnauthenticated, "authentication required"
	case errors.Is(err, dbadmin.ErrStepUpRequired):
		return http.StatusPreconditionRequired, CodeStepUpRequired, "step-up authentication required"
	case errors.Is(err, dbadmin.ErrForbidden):
		return http.StatusForbidden, CodeForbidden, "forbidden"
	case errors.Is(err, dbadmin.ErrNotFound):
		return http.StatusNotFound, CodeNotFound, "not found"
	case errors.Is(err, dbadmin.ErrConflict):
		return http.StatusConflict, CodeConflict, "conflict"
	case errors.Is(err, dbadmin.ErrInvalidInput):
		return http.StatusBadRequest, CodeInvalidInput, "invalid input"

	// schema
	case errors.Is(err, schema.ErrInvalidIdentifier):
		return http.StatusBadRequest, CodeInvalidIdentifier, "invalid identifier"
	case errors.Is(err, schema.ErrTableNotFound):
		return http.StatusNotFound, CodeNotFound, "table not found"

	// history
	case errors.Is(err, history.ErrNotFound):
		return http.StatusNotFound, CodeNotFound, "history entry not found"
	case errors.Is(err, history.ErrInvalidInput):
		return http.StatusBadRequest, CodeInvalidInput, "invalid input"
	case errors.Is(err, history.ErrClosed):
		return http.StatusServiceUnavailable, CodeHistoryStoreClosed, "history store closed"

	// rows
	case errors.Is(err, rows.ErrInvalidPredicate):
		return http.StatusBadRequest, CodeInvalidPredicate, "invalid predicate"
	case errors.Is(err, rows.ErrRowCapExceeded):
		return http.StatusUnprocessableEntity, CodeRowCapExceeded, "row cap exceeded; add LIMIT or use /sql/stream"
	case errors.Is(err, rows.ErrNoPrimaryKey):
		return http.StatusUnprocessableEntity, CodeNoPrimaryKey, "table has no primary key"
	case errors.Is(err, rows.ErrPKMismatch):
		return http.StatusBadRequest, CodePKMismatch, "primary key mismatch"
	case errors.Is(err, rows.ErrEmptyUpdate):
		return http.StatusBadRequest, CodeEmptyUpdate, "empty update"
	case errors.Is(err, rows.ErrConcurrentModification):
		// edit-1: the row's snapshot columns moved under the client.
		return http.StatusConflict, CodeConflict, "row changed since last read"

	// classifier
	case errors.Is(err, classifier.ErrTooLarge):
		return http.StatusRequestEntityTooLarge, CodeStatementTooLarge, "statement too large"

	// driver — DO NOT echo backend message verbatim; the SQL or
	// parameter values may be embedded by the underlying driver.
	case errors.Is(err, driver.ErrAuth):
		return http.StatusBadGateway, CodeBackendAuthFailed, "backend authentication failed"
	case errors.Is(err, driver.ErrUnavailable):
		return http.StatusBadGateway, CodeBackendUnavailable, "backend unavailable"
	case errors.Is(err, driver.ErrTimeout):
		return http.StatusGatewayTimeout, CodeBackendTimeout, "backend timeout"
	case errors.Is(err, driver.ErrCanceled):
		return 499, CodeClientClosed, "client closed request"
	case errors.Is(err, driver.ErrSyntax):
		return http.StatusUnprocessableEntity, CodeSyntaxError, "syntax error in statement"
	case errors.Is(err, driver.ErrPermission):
		return http.StatusForbidden, CodeBackendPermission, "backend permission denied"
	case errors.Is(err, driver.ErrConflict):
		return http.StatusConflict, CodeBackendConflict, "backend conflict"
	case errors.Is(err, driver.ErrNotFound):
		return http.StatusNotFound, CodeBackendNotFound, "backend resource not found"
	case errors.Is(err, driver.ErrCapped):
		return http.StatusUnprocessableEntity, CodeResultCapped, "result capped; use /sql/stream"
	case errors.Is(err, driver.ErrClosed):
		return http.StatusInternalServerError, CodeInternal, "internal driver state"
	case errors.Is(err, driver.ErrSlowLogUnavailable):
		// Surface the hint (operator-actionable) verbatim via the
		// error's Error() string — callers using writeErrorDetails
		// can attach the hint separately.
		return http.StatusUnprocessableEntity, CodeSlowLogUnavailable, err.Error()

	// context
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, CodeTimeout, "request timed out"
	case errors.Is(err, context.Canceled):
		return 499, CodeClientClosed, "client closed request"
	}

	// JSON decode errors
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return http.StatusBadRequest, CodeInvalidJSON, "invalid JSON"
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return http.StatusBadRequest, CodeInvalidJSON, "invalid JSON type"
	}
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		return http.StatusRequestEntityTooLarge, CodeBodyTooLarge, "request body too large"
	}
	// DisallowUnknownFields + EOF + trailing-data + generic "readJSON"
	// errors come back as plain errors; the substrings are stable
	// across Go versions.
	msgText := err.Error()
	if strings.Contains(msgText, "unknown field") ||
		strings.Contains(msgText, "unexpected EOF") ||
		strings.Contains(msgText, "unexpected end of JSON input") ||
		strings.Contains(msgText, "trailing data") ||
		strings.Contains(msgText, "readJSON:") {
		return http.StatusBadRequest, CodeInvalidJSON, "invalid JSON"
	}

	return http.StatusInternalServerError, CodeInternal, "internal error"
}

// writeMappedErr is a convenience that runs mapErr and emits the envelope.
// Used by handlers that just want to "return an error" without worrying
// about the status/code translation.
func writeMappedErr(w http.ResponseWriter, r *http.Request, err error) {
	status, code, msg := mapErr(err)
	writeError(w, r, status, code, msg)
	// Record the error string on the audit accumulator so the
	// audit middleware emits a denial event for authn/authz/classifier
	// rejections.
	if status >= 400 && status < 500 {
		setAuditError(r.Context(), fmt.Sprintf("%s:%s", code, msg))
	}
}

package httpapi

import (
	"context"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// ctxKey is the unexported context-key type. Keeps our keys distinct from
// every other package's by the Go type system.
type ctxKey int

const (
	ctxUser ctxKey = iota
	ctxRequestID
	ctxConnectionID
	ctxAuditState
	ctxStartTime
)

// auditState is the per-request audit accumulator. Handlers populate it
// via setAuditAction / setAuditTarget / setAuditError; the audit middleware
// reads it on the way out and emits one Event.
type auditState struct {
	action     dbadmin.Action
	target     dbadmin.Target
	rows       int64
	err        string
	stepUpJTI  string
	statement  string
	suppress   bool // if true, the middleware does not emit an event
}

// userFrom returns the authenticated User stored in ctx, or the zero User
// (and false) when none is present.
func userFrom(ctx context.Context) (dbadmin.User, bool) {
	v, ok := ctx.Value(ctxUser).(dbadmin.User)
	return v, ok
}

// requestIDFrom returns the request ID stored in ctx, empty string when
// none.
func requestIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxRequestID).(string)
	return v
}

// connIDFrom returns the connection ID stored in ctx, empty string when
// none.
func connIDFrom(ctx context.Context) dbadmin.ConnectionID {
	v, _ := ctx.Value(ctxConnectionID).(dbadmin.ConnectionID)
	return v
}

// auditFrom returns the per-request audit accumulator, allocating one if
// the context has none.
func auditFrom(ctx context.Context) *auditState {
	v, ok := ctx.Value(ctxAuditState).(*auditState)
	if !ok || v == nil {
		return &auditState{}
	}
	return v
}

// startTimeFrom returns the per-request start timestamp.
func startTimeFrom(ctx context.Context) time.Time {
	v, _ := ctx.Value(ctxStartTime).(time.Time)
	return v
}

// setAuditAction stamps the action + target on the per-request accumulator.
func setAuditAction(ctx context.Context, action dbadmin.Action, target dbadmin.Target) {
	st := auditFrom(ctx)
	st.action = action
	st.target = target
}

// setAuditError records the error string on the per-request accumulator.
func setAuditError(ctx context.Context, err string) {
	auditFrom(ctx).err = err
}

// setAuditRows records the rows-affected count on the per-request accumulator.
func setAuditRows(ctx context.Context, n int64) {
	auditFrom(ctx).rows = n
}

// suppressAudit tells the audit middleware to skip emitting an event for
// this request (used by routes whose handlers emit explicitly, or by routes
// that are not auditable at all).
func suppressAudit(ctx context.Context) {
	auditFrom(ctx).suppress = true
}

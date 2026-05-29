package dbadmin

import (
	"context"
	"time"
)

// AuditSink receives every audit event the engine emits. Implementations
// own durable storage, tamper-evidence (chain), and optional forwarding to
// syslog / webhooks / external SIEM.
//
// Contract:
//
//   - Record is append-only. Implementations MUST NOT modify or delete
//     previously recorded events.
//
//   - Record is called from the request goroutine and MUST return
//     quickly (< 10 ms typical). Heavy work — chain signing, remote
//     forwarding, file rotation — belongs in a background goroutine
//     owned by the implementation. Buffer aggressively if needed.
//
//   - A failed Record MUST NOT fail the user-facing request. This is a
//     deliberate trade-off: a temporary audit-storage outage should not
//     block legitimate operator work. Implementations are expected to
//     buffer-or-spool on failure and alert separately (logs, metrics).
//
//   - For every state-changing action, the engine emits TWO events:
//     one before the action starts (capturing what was requested) and
//     one after (capturing the outcome). The two share an EventID
//     relationship: the outcome event's EventID is the next ULID in
//     monotonic order. Implementations correlate by user_id + timestamp
//     proximity if needed; the engine does not embed back-references.
//
//   - For read-only operations, the engine samples at the rate
//     configured in Engine.Config.Audit.SampleReadQueries (default
//     0.01 = 1%). Implementations receive the sampled events; they do
//     not see the un-sampled ones.
type AuditSink interface {
	// Record persists an event.
	//
	// MUST return quickly. MUST NOT fail the caller's request even
	// when persistence fails (implementations log the error
	// internally and surface metrics, not exceptions).
	Record(context.Context, Event)
}

// Event is the unit of audit. Fields are populated by the engine; sinks
// extend with PrevEventHash for chain linking.
type Event struct {
	// EventID is a ULID — sortable lexicographically by time, unique
	// within an Aura DB instance.
	EventID string

	// Timestamp in UTC with nanosecond precision.
	Timestamp time.Time

	// UserID identifies who performed the action. Opaque to the
	// engine; comes verbatim from User.ID at the time of the action.
	UserID string

	// UserRoleAtTime is the role the user held on the connection (or
	// the role they nominally held for global actions) when the
	// action was attempted. Captured at attempt time so a later role
	// change doesn't obscure history.
	UserRoleAtTime Role

	// SourceIP is the client IP at attempt time. Implementations may
	// truncate to /24 (IPv4) / /56 (IPv6) for privacy; the engine
	// passes the raw IP and lets the sink decide.
	SourceIP string

	// UserAgentHash is a SHA-256 hash of the User-Agent header,
	// truncated to 16 hex chars. We don't store the raw UA because
	// it varies on every browser update and would bloat the log.
	UserAgentHash string

	// Action is the canonical action class.
	Action Action

	// Target identifies what the action affected. Empty for actions
	// that have no specific target (e.g., login).
	Target Target

	// Statement is the SQL text for query-class actions. Empty for
	// non-query actions.
	//
	// Sensitive parameters (CREATE USER ... IDENTIFIED BY 'pw') are
	// redacted by the classifier before the engine emits the event.
	// Sinks should not attempt their own redaction.
	Statement string

	// ParametersRedacted is the map of parameterized-query values
	// AFTER redaction. The engine's classifier identifies values
	// that look like credentials and replaces them with "[redacted]"
	// before the event reaches the sink.
	ParametersRedacted map[string]any

	// ResultRows is the row count of the action's result, or 0 for
	// actions without a row count (e.g., login).
	ResultRows int64

	// DurationMS is the wall-clock duration of the action in
	// milliseconds. 0 for pre-action events.
	DurationMS int64

	// Error is the failure message if the action failed, empty on
	// success. For pre-action events (the "request" event of the
	// pair), this is always empty.
	Error string

	// StepUpJTI identifies the step-up assertion that authorized this
	// action, if any. Empty if the action didn't require step-up.
	StepUpJTI string

	// PrevEventHash links to the previous event in the audit chain
	// (SHA-256 of the prior event's serialized form). Set by the
	// sink, not the engine.
	PrevEventHash string
}

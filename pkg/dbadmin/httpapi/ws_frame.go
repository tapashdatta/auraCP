package httpapi

import "time"

// WebSocket protocol constants. See design.wsProtocol.
const (
	wsSubprotocol = "aura.sql.v1"

	wsReadLimit  int64 = 256 * 1024
	wsWriteWait        = 10 * time.Second
	wsPongWait         = 60 * time.Second
	wsPingPeriod       = 30 * time.Second

	wsMaxStreamDuration = 30 * time.Minute

	wsMaxRowsHardCap  = 10_000_000
	wsMaxBytesHardCap = 1 << 30 // 1 GiB

	// Close codes (custom; 4000–4999 reserved for application use).
	wsCloseUnauthenticated     = 4401
	wsCloseForbidden           = 4403
	wsCloseFrameTooLarge       = 4413
	wsCloseForbiddenStatement  = 4422
	wsCloseStepUpRequired      = 4428
	wsCloseClientCancel        = 4499
	wsCloseBackendTimeout      = 4504
	wsClosePolicyViolation     = 1008
	wsCloseInternal            = 1011
	wsCloseUnsupportedData     = 1003
	wsCloseNormal              = 1000
)

// frameType labels client→server and server→client frames.
type frameType string

const (
	frameOpen     frameType = "open"
	frameCancel   frameType = "cancel"
	framePing     frameType = "ping"
	frameMeta     frameType = "meta"
	frameRow      frameType = "row"
	frameProgress frameType = "progress"
	frameDone     frameType = "done"
	frameError    frameType = "error"
)

// openFrame is the first frame the client sends.
//
// DEF-6: Csrf carries the same token presented on the handshake. The
// server revalidates it on every inbound open frame to defend against
// replay against a hijacked WS session.
type openFrame struct {
	Type         frameType            `json:"type"`
	ConnectionID string               `json:"connectionId"`
	Statement    string               `json:"statement"`
	Parameters   []any                `json:"parameters,omitempty"`
	Limits       *openFrameLimitsSpec `json:"limits,omitempty"`
	Csrf         string               `json:"csrf,omitempty"`
}

type openFrameLimitsSpec struct {
	MaxRows   int   `json:"maxRows"`
	MaxBytes  int64 `json:"maxBytes"`
	TimeoutMS int64 `json:"timeoutMs"`
}

type cancelFrame struct {
	Type frameType `json:"type"`
	// DEF-6: optional CSRF on cancel. When set, must equal the
	// handshake cookie; otherwise the cancel is ignored. When omitted,
	// the server accepts the cancel (the connection itself was
	// authenticated at upgrade).
	Csrf string `json:"csrf,omitempty"`
}

type metaFrame struct {
	Type            frameType           `json:"type"`
	QueryID         string              `json:"queryId"`
	Class           string              `json:"class"`
	Columns         []columnInfoDTO     `json:"columns"`
	EffectiveLimits openFrameLimitsSpec `json:"effectiveLimits"`
}

type rowFrame struct {
	Type frameType `json:"type"`
	Rows [][]any   `json:"rows"`
}

type progressFrame struct {
	Type         frameType `json:"type"`
	RowsEmitted  int64     `json:"rowsEmitted"`
	BytesEmitted int64     `json:"bytesEmitted"`
	ElapsedMS    int64     `json:"elapsedMs"`
}

type doneFrame struct {
	Type       frameType `json:"type"`
	TotalRows  int64     `json:"totalRows"`
	TotalBytes int64     `json:"totalBytes"`
	DurationMS int64     `json:"durationMs"`
	Truncated  bool      `json:"truncated"`
}

type errorFrame struct {
	Type      frameType `json:"type"`
	Code      string    `json:"code"`
	Message   string    `json:"message"`
	RequestID string    `json:"request_id"`
}

// ─── Slow-log subprotocol (v0.3.2-C) ─────────────────────────────────
//
// /connections/{id}/slow-log/stream rides the same wsSubprotocol token
// and reuses the open / cancel / error / progress / done close codes
// of the SQL stream. The slow-log-specific frames are an open-frame
// variant carrying SlowLog parameters and a row frame carrying one
// slowLogRow per emission. The meta frame carries Mode + a Hint when
// the backend's prerequisites are met but degraded (e.g. snapshot
// mode for Postgres). Mode "unavailable" is never emitted as a meta
// frame — it always resolves to an error frame so the client takes
// a definite "not enabled" branch.

const wsSlowLogSubprotocol = "aura.slowlog.v1"

// slowLogParamsSpec is carried inside the open frame's SlowLog field
// to parameterise the slow-log read. SinceMS is a Unix millisecond
// epoch — the server clamps to "not in the future" and treats zero as
// "all available rows". MinDurationMS filters per-execution (table
// mode) or per-mean (snapshot mode). MaxRows / TimeoutMS clamp to the
// engine's Config().Query.* effective caps, same as openFrameLimitsSpec.
type slowLogParamsSpec struct {
	SinceMS       int64 `json:"sinceMs,omitempty"`
	MinDurationMS int64 `json:"minDurationMs,omitempty"`
	MaxRows       int   `json:"maxRows,omitempty"`
	// Follow asks the server to keep the connection open and poll for
	// new rows on an interval. Honoured only in table mode (MariaDB);
	// snapshot mode (Postgres) refuses follow=true and emits a single
	// snapshot then done.
	Follow bool `json:"follow,omitempty"`
}

// slowLogMetaFrame describes the slow-log session's effective
// parameters and the discovered backend mode. Distinct type from
// metaFrame so SDKs can switch on frame type without inspecting
// optional columns.
type slowLogMetaFrame struct {
	Type            frameType `json:"type"`
	Mode            string    `json:"mode"`            // "table" | "snapshot"
	Hint            string    `json:"hint,omitempty"`  // operator-actionable note (empty when fully usable)
	PollIntervalMS  int64     `json:"pollIntervalMs,omitempty"`
	EffectiveLimits openFrameLimitsSpec `json:"effectiveLimits"`
}

// slowLogRow is one emitted slow-query record. Field names are
// camelCased per SDK wire convention. SQL text is REDACTED to a
// length-capped excerpt; the audit log carries the verbatim text
// server-side.
type slowLogRow struct {
	Type         frameType `json:"type"` // always frameRow
	TimestampMS  int64     `json:"timestampMs"`
	UserHost     string    `json:"userHost"`
	Database     string    `json:"database,omitempty"`
	QueryTimeMS  float64   `json:"queryTimeMs"`
	LockTimeMS   float64   `json:"lockTimeMs,omitempty"`
	MeanTimeMS   float64   `json:"meanTimeMs"`
	Calls        int64     `json:"calls"`
	RowsExamined int64     `json:"rowsExamined,omitempty"`
	RowsSent     int64     `json:"rowsSent,omitempty"`
	SQLExcerpt   string    `json:"sqlExcerpt"`
}

// wsSlowLogPollInterval is the default poll cadence for Follow=true
// streams. Set conservatively: a 2s tick is fine-grained enough for an
// operator watching a degrading query without hammering the backend.
// The httpapi layer surfaces this in the meta frame so the UI can
// render a "next refresh in 2s" affordance.
const wsSlowLogPollInterval = 2 * time.Second

// wsSlowLogSQLExcerptCap is the maximum bytes of sql_text emitted per
// row. We never echo more than this to the client — operator logs are
// in the audit sink. 2 KiB is enough to spot the slow query while
// keeping the WS frame size bounded.
const wsSlowLogSQLExcerptCap = 2048


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

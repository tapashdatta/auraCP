package httpapi

import (
	"net/http"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/history"
	"github.com/gorilla/websocket"
)

// server is the per-engine handler context. Holds the engine reference
// and all auxiliary state — limiter, upgrader, saved-queries store,
// optional history store, CSRF gate.
type server struct {
	engine *dbadmin.Engine

	limiter        *limiter
	upgrader       websocket.Upgrader
	saved          *savedQueriesStore
	historyStore   history.Store
	allowedOrigins []string
	// allowLoopbackEmptyOrigin opts the embedder into accepting WS
	// upgrades that arrive without an Origin header from a loopback
	// peer (127.0.0.1 / ::1). Required for native CLI / desktop
	// clients that don't set Origin. Off by default — empty Origin
	// from a non-loopback peer is always rejected.
	allowLoopbackEmptyOrigin bool
	csrfDisabled             bool // for tests / dev mode
	// csrfCookieName + csrfHeaderName let an embedder rebind the CSRF
	// double-submit identity. The standalone surface defaults to
	// __Host-aura_csrf / X-Aura-Csrf; the panel mount overrides both
	// to its existing auracp_csrf / X-CSRF-Token so the panel SPA's
	// existing CSRF mint is honored (FIX-2 / PD-SEC-02_INT-1).
	csrfCookieName string
	csrfHeaderName string

	// DEF-25: async audit hand-off. All httpapi.Record calls go through
	// recordAudit, which uses async when configured and falls back to
	// the engine's sink directly when not. Defaults to wrapping the
	// engine's sink so slow sinks never block the request goroutine.
	asyncAudit *asyncSink

	// DEF-32: per-user concurrent-query semaphore. PoolSizePerConn is
	// typically 4; without a cap a single user can keep N=100 reads
	// pending against a 4-slot pool, queueing everyone else.
	queryGate *userSemaphore

	// DEF-4: signed-URL grants for password reveal. The /reveal POST
	// mints a token; the /reveal/{token} GET burns it.
	revealStore *revealStore
}

// Default CSRF cookie / header names. Exported so embedders that override
// one side can keep the other on the default.
const (
	DefaultCSRFCookieName = "__Host-aura_csrf"
	DefaultCSRFHeaderName = "X-Aura-Csrf"
)

// Options carries optional knobs for New. Embedders that need to override
// defaults (e.g., supply a WS Origin allowlist, opt into accepting empty
// Origin from native loopback clients, or disable CSRF for tests) pass an
// Options struct via NewWithOptions. The zero value is the secure default.
type Options struct {
	// AllowedOrigins is the explicit WS Origin allowlist. When empty,
	// only same-origin requests (Origin matches Host) are accepted.
	// When non-empty, the upgrade is refused unless the inbound
	// Origin string exactly matches one of these values.
	AllowedOrigins []string

	// AllowLoopbackEmptyOrigin allows the WS upgrader to accept a
	// request with no Origin header when the peer is loopback
	// (127.0.0.1 or ::1). Off by default. Turn this on only for
	// hosts that need to support native (non-browser) CLI clients.
	AllowLoopbackEmptyOrigin bool

	// CSRFDisabled turns off the CSRF middleware. Tests and tightly-
	// scoped dev mode only — production hosts must leave this false.
	CSRFDisabled bool

	// CSRFCookieName overrides the cookie consulted by the CSRF gate
	// and the WS handshake. Empty string falls back to
	// DefaultCSRFCookieName. Embedders that mount this engine behind
	// a host that already mints a different CSRF cookie name (e.g.
	// the auraCP control panel mints `auracp_csrf`) override this so
	// no double-cookie state can develop. FIX-2 / PD-SEC-02_INT-1.
	CSRFCookieName string

	// CSRFHeaderName overrides the request header consulted by the
	// CSRF gate. Empty falls back to DefaultCSRFHeaderName.
	CSRFHeaderName string
}

// New constructs the HTTP wire surface for an engine. The returned
// http.Handler is mounted by hosts under any prefix (typically
// /api/dbadmin/).
//
// The returned handler participates in graceful shutdown via the
// engine's in-flight counter; calls after Engine.Shutdown return 503.
//
// This is the legacy zero-config entry point — embedders that need to
// supply an Origin allowlist or other knobs should use NewWithOptions.
func New(e *dbadmin.Engine) http.Handler {
	return NewWithOptions(e, Options{})
}

// NewWithOptions is like New but accepts a configuration struct.
func NewWithOptions(e *dbadmin.Engine, opts Options) http.Handler {
	cookieName := opts.CSRFCookieName
	if cookieName == "" {
		cookieName = DefaultCSRFCookieName
	}
	headerName := opts.CSRFHeaderName
	if headerName == "" {
		headerName = DefaultCSRFHeaderName
	}
	s := &server{
		engine:                   e,
		limiter:                  newLimiter(),
		saved:                    newSavedQueriesStore(),
		allowedOrigins:           opts.AllowedOrigins,
		allowLoopbackEmptyOrigin: opts.AllowLoopbackEmptyOrigin,
		csrfDisabled:             opts.CSRFDisabled,
		csrfCookieName:           cookieName,
		csrfHeaderName:           headerName,
		queryGate:                newUserSemaphore(perUserQueryCap),
		revealStore:              newRevealStore(),
	}
	if e != nil && e.Audit() != nil {
		s.asyncAudit = newAsyncSink(e.Audit())
	}
	// The Upgrader's CheckOrigin defers to the package-level helper so
	// the policy is identical whether the upgrade is gated by the
	// gorilla/websocket library or our handler's pre-check. Returning
	// false fails the upgrade with the library's stock 403.
	s.upgrader = websocket.Upgrader{
		Subprotocols:      []string{wsSubprotocol},
		EnableCompression: false,
		CheckOrigin: func(r *http.Request) bool {
			return originAllowed(s, r)
		},
	}
	return s.routes()
}

// init wires this package's router factory into pkg/dbadmin.
// Engine.Handler() returns whatever this factory produces.
func init() {
	dbadmin.WireHandler(func(e *dbadmin.Engine) http.Handler { return New(e) })
}

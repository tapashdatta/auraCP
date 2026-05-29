package dbadmin

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/auracp/auracp/internal/secret"
	"github.com/auracp/auracp/internal/store"
	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/httpapi"
)

// Panel CSRF identity. Must mirror internal/api/middleware.go's
// csrfCookie / "X-CSRF-Token" header literals. Pulled out as exported
// constants so a future reshuffle inside internal/api cannot silently
// re-introduce the FIX-2 mismatch without touching this file too.
const (
	PanelCSRFCookieName = "auracp_csrf"
	PanelCSRFHeaderName = "X-CSRF-Token"
)

// defaultShutdownTimeout bounds mountCloser.Close. FIX-5 / C1_INT-8.
const defaultShutdownTimeout = 30 * time.Second

// Mount wires the Aura DB engine onto the panel's *http.ServeMux at the
// path /api/dbadmin/. The returned engine is owned by the caller (kept
// for graceful Shutdown); the returned io.Closer flushes the audit chain
// log on shutdown.
//
// Both return values are non-nil on success. On error, neither is.
//
// Wiring contract:
//
//   - mux is the same mux api.Register() writes to; we install ONE entry,
//     "/api/dbadmin/", and let net/http's longest-prefix dispatch send
//     anything under that prefix into the dbadmin handler.
//
//   - st is the panel store; Mount runs RunMigrations to add the
//     aura_db_* tables idempotently.
//
//   - sec is the panel's secret.Box; connection credentials use the same
//     KEK as the panel's other encrypted-at-rest values.
//
//   - currentFn must be the panel's session resolver (api.Server.currentUser).
//     We deliberately accept a function rather than the *api.Server to
//     avoid an import cycle.
//
//   - cfg is the panel's typed config; LoadFromStore is the typical
//     supplier.
func Mount(mux *http.ServeMux, st *store.Store, sec *secret.Box, currentFn CurrentUserFunc, cfg Config) (*dbadmin.Engine, io.Closer, error) {
	if mux == nil {
		return nil, nil, errors.New("dbadmin: nil mux")
	}
	if st == nil {
		return nil, nil, errors.New("dbadmin: nil store")
	}
	if sec == nil {
		return nil, nil, errors.New("dbadmin: nil secret.Box")
	}
	if currentFn == nil {
		return nil, nil, errors.New("dbadmin: nil currentFn")
	}

	if err := RunMigrations(st.DB); err != nil {
		return nil, nil, err
	}

	conns := newPanelConns(st, sec)
	stepUp := newStepUpStore()
	authImpl := newPanelAuth(st, conns, currentFn, stepUp)

	signingKey, err := loadOrCreateSigningKey(st)
	if err != nil {
		stepUp.stop()
		return nil, nil, err
	}
	// PR #10.5 / FIX-INT-9: pass the shared logger so audit warn lines
	// land in the same slog handler the rest of the panel emits to,
	// pre-stamped with comp=dbadmin for easy filtering.
	auditImpl, err := newPanelAudit(cfg.AuditPath, signingKey, st, SharedLogger())
	if err != nil {
		stepUp.stop()
		return nil, nil, err
	}

	engine, err := dbadmin.New(dbadmin.Options{
		Auth:   authImpl,
		Conns:  conns,
		Audit:  auditImpl,
		Config: cfg.ToEngine(),
	})
	if err != nil {
		_ = auditImpl.Close()
		stepUp.stop()
		return nil, nil, err
	}

	// Mount under /api/dbadmin/. The engine's handler emits routes
	// relative to its mount point (StripPrefix peels the panel prefix
	// before dispatch). Same-origin only: AllowedOrigins is left nil so
	// the WS upgrader falls back to "Origin must equal Host".
	//
	// FIX-2 / PD-SEC-02_INT-1: the standalone dbadmin defaults to the
	// __Host-aura_csrf / X-Aura-Csrf double-submit, but the panel SPA
	// has minted auracp_csrf / X-CSRF-Token since v0.x. Rebind the
	// CSRF identity here so every mutating /api/dbadmin/* request the
	// browser issues is validated against the panel's existing cookie.
	handler := httpapi.NewWithOptions(engine, httpapi.Options{
		CSRFCookieName: PanelCSRFCookieName,
		CSRFHeaderName: PanelCSRFHeaderName,
	})
	// FIX-INT-9: wrap with the request-id middleware so every request
	// flowing into /api/dbadmin/* gets a correlation id available to
	// downstream slog lines AND the panel audit mirror.
	mux.Handle("/api/dbadmin/", WithRequestIDMiddleware(
		http.StripPrefix("/api/dbadmin", handler)))

	// PR #10.5 / OPS-04 (reassigned from PR #9.5): probe endpoints.
	// /healthz answers "is the process up" (always 200 once Mount
	// returns); /readyz answers "is the engine ready to serve" — the
	// audit chain is open, the engine has not yet started shutting
	// down, and the panel store responds to a ping. We install these
	// at the /api/dbadmin/ namespace's siblings (not the panel root)
	// so an external load balancer can probe the dbadmin surface
	// independently of the panel SPA's catch-all handler. Both are
	// JSON to match the rest of the API surface; both are public (no
	// session cookie required) because they are used by orchestrators
	// (systemd, k8s, uptime monitors) that have no panel identity.
	mux.HandleFunc("/api/dbadmin/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("/api/dbadmin/readyz", &readyzHandler{
		engine: engine,
		audit:  auditImpl,
		store:  st,
	})

	timeout := cfg.ShutdownTimeout
	if timeout <= 0 {
		timeout = defaultShutdownTimeout
	}
	return engine, &mountCloser{
		audit:           auditImpl,
		stepUp:          stepUp,
		engine:          engine,
		shutdownTimeout: timeout,
	}, nil
}

// readyzHandler answers /api/dbadmin/readyz. Ready means: (1) the
// engine is not shutting down, (2) the audit chain sink is open
// (non-nil), and (3) the panel SQLite store responds to a Ping().
//
// PR #10.5 / OPS-04: previously there was no readiness probe at all,
// which meant a load balancer would route traffic to a daemon whose
// engine was mid-shutdown or whose audit chain had failed to open —
// both states the panel would otherwise have silently degraded.
type readyzHandler struct {
	engine *dbadmin.Engine
	audit  *panelAudit
	store  *store.Store
}

func (h *readyzHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	// Engine state — refuse readiness if Shutdown has started.
	if h.engine == nil || h.engine.IsShuttingDown() {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"shutting-down","engine":false}`))
		return
	}
	// Audit chain — required for ANY mutating action; if the sink
	// closed (disk full, etc.) we are not ready to accept writes.
	if h.audit == nil || h.audit.chain == nil || h.audit.closed.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"audit-down","audit":false}`))
		return
	}
	// Store ping — required for connection lookups and audit_log
	// mirror writes. Bound by a short context to keep the probe cheap.
	if h.store != nil && h.store.DB != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := h.store.DB.PingContext(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"store-down","store":false}`))
			return
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ready","engine":true,"audit":true,"store":true}`))
}

// mountCloser bundles the three things Mount creates that need to be
// stopped on graceful shutdown.
//
// PR #10.5 / FIX-PD-SEC-04: the closer also exposes InvalidateSession
// so the panel logout handler can drop any step-up flags bound to the
// session it just deleted. Without this hook a logged-out operator's
// step-up grants survive until either the in-memory TTL expires
// (default 5 minutes) or the reaper loop ticks.
type mountCloser struct {
	audit           *panelAudit
	stepUp          *stepUpStore
	engine          *dbadmin.Engine
	shutdownTimeout time.Duration
}

// InvalidateSession drops every in-memory step-up flag bound to the
// given panel session token. Panel callers invoke this from POST
// /api/auth/logout immediately after deleting the session row.
func (c *mountCloser) InvalidateSession(sessionToken string) {
	if c == nil || c.stepUp == nil {
		return
	}
	c.stepUp.InvalidateSession(sessionToken)
}

// SessionInvalidator is the narrow capability the panel logout handler
// needs from the dbadmin adapter. Both mountCloser and any test
// double satisfy it.
type SessionInvalidator interface {
	InvalidateSession(sessionToken string)
}

// LogoutHookFor returns a func(sessionToken string) that the panel's
// SetLogoutHook accepts. Convenience wrapper around the io.Closer the
// caller already holds — avoids forcing every caller to type-assert the
// io.Closer back to *mountCloser.
func LogoutHookFor(c io.Closer) func(string) {
	mc, ok := c.(SessionInvalidator)
	if !ok {
		return func(string) {}
	}
	return mc.InvalidateSession
}

// Close stops the step-up reaper, flushes the audit chain, and signals
// engine shutdown. The engine's in-flight drain is bounded by
// shutdownTimeout (FIX-5 / C1_INT-8): previously this used
// context.Background() which let a wedged in-flight WebSocket or query
// hang the daemon's exit indefinitely. Now process shutdown completes
// within shutdownTimeout regardless of the engine's drain state.
func (c *mountCloser) Close() error {
	if c == nil {
		return nil
	}
	timeout := c.shutdownTimeout
	if timeout <= 0 {
		timeout = defaultShutdownTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_ = c.engine.Shutdown(ctx)
	c.stepUp.stop()
	return c.audit.Close()
}

// Reopen forwards the rotation signal to the audit chain sink. The
// installer wires this to SIGHUP so logrotate can rotate the NDJSON
// file without losing the in-memory hash-chain head.
func (c *mountCloser) Reopen() error {
	if c == nil || c.audit == nil {
		return nil
	}
	return c.audit.Reopen()
}

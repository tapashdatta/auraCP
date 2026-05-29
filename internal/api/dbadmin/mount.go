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
	auditImpl, err := newPanelAudit(cfg.AuditPath, signingKey, st, nil)
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
	mux.Handle("/api/dbadmin/", http.StripPrefix("/api/dbadmin", handler))

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

// mountCloser bundles the three things Mount creates that need to be
// stopped on graceful shutdown.
type mountCloser struct {
	audit           *panelAudit
	stepUp          *stepUpStore
	engine          *dbadmin.Engine
	shutdownTimeout time.Duration
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

package dbadmin

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
)

// engineHandlerFactory is the package-private indirection that the
// httpapi subpackage installs via WireHandler. It avoids an import cycle
// between pkg/dbadmin and pkg/dbadmin/httpapi.
var engineHandlerFactory func(*Engine) http.Handler

// WireHandler is called by package httpapi at init-time to register its
// router factory. The engine then returns that factory's result from
// Handler(). Hosts MUST NOT call this directly.
func WireHandler(fn func(*Engine) http.Handler) {
	engineHandlerFactory = fn
}

// Options carries everything New() needs to construct an Engine. Auth,
// Conns, and Audit are required. Config is optional; the zero value is
// replaced by DefaultConfig().
type Options struct {
	Auth   Auth            // required
	Conns  ConnectionStore // required
	Audit  AuditSink       // required
	Config Config          // optional; defaults applied if zero-valued

	// Hooks are optional integration points. Nil means "no hook." See
	// hooks.go (PR #8) for the interface definitions; for now they're
	// accepted as `any` and ignored.
	QueryHook  any
	StepUpHook any
}

// Engine is the runnable Aura DB. After New(), the Engine is safe for
// concurrent use. Mutating the Engine value after construction is
// undefined behavior; rebuild a new Engine via New() if config changes.
type Engine struct {
	auth   Auth
	conns  ConnectionStore
	audit  AuditSink
	config Config

	// shutdown is closed when Shutdown() is called. Handler() rejects
	// new requests after this point with HTTP 503.
	shutdown chan struct{}
	// shut tracks whether shutdown has been initiated. Atomically
	// transitioned 0→1.
	shut atomic.Bool

	// inflight counts in-flight requests so Shutdown can wait for
	// them to drain.
	inflight sync.WaitGroup
}

// New constructs an Engine. Returns an error if a required interface is
// nil or if Config fails validation.
func New(opt Options) (*Engine, error) {
	if opt.Auth == nil {
		return nil, errors.New("dbadmin: Options.Auth is required")
	}
	if opt.Conns == nil {
		return nil, errors.New("dbadmin: Options.Conns is required")
	}
	if opt.Audit == nil {
		return nil, errors.New("dbadmin: Options.Audit is required")
	}

	cfg := mergeConfig(DefaultConfig(), opt.Config)
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	e := &Engine{
		auth:     opt.Auth,
		conns:    opt.Conns,
		audit:    opt.Audit,
		config:   cfg,
		shutdown: make(chan struct{}),
	}
	return e, nil
}

// Config returns a copy of the engine's resolved config (defaults + host
// overrides). The returned value is a snapshot; mutating it does NOT
// affect the engine.
func (e *Engine) Config() Config {
	return e.config
}

// AuthSurface exposes the Auth implementation to subpackages
// (httpapi). Not for host use.
func (e *Engine) AuthSurface() Auth { return e.auth }

// Conns exposes the ConnectionStore implementation to subpackages.
// Not for host use.
func (e *Engine) Conns() ConnectionStore { return e.conns }

// Audit exposes the AuditSink implementation to subpackages.
// Not for host use.
func (e *Engine) Audit() AuditSink { return e.audit }

// IsShuttingDown reports whether Shutdown has been called.
func (e *Engine) IsShuttingDown() bool { return e.shut.Load() }

// TrackInflight increments the in-flight counter and returns a release
// function. Used by the httpapi middleware to participate in graceful
// shutdown. Not for host use.
func (e *Engine) TrackInflight() func() {
	e.inflight.Add(1)
	return e.inflight.Done
}

// Handler returns the http.Handler that mounts the engine's REST + WebSocket
// surface. Hosts mount it at any path; the engine's URLs are relative to
// the mount point.
//
// PR #8: the router is composed by the httpapi subpackage, which registers
// its factory via WireHandler() at package init. The fallback (when
// httpapi is not linked, e.g. tiny smoke binaries) is the previous PR #1
// minimal handler — authn + 501 — so the package remains importable
// stand-alone.
func (e *Engine) Handler() http.Handler {
	if engineHandlerFactory != nil {
		return engineHandlerFactory(e)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if e.shut.Load() {
			writeError(w, http.StatusServiceUnavailable, CodeUnavailable,
				"engine shutting down")
			return
		}
		e.inflight.Add(1)
		defer e.inflight.Done()
		_, err := e.auth.Authenticate(r)
		if err != nil {
			if errors.Is(err, ErrUnauthenticated) {
				writeError(w, http.StatusUnauthorized,
					CodeUnauthenticated, "authentication required")
				return
			}
			writeError(w, http.StatusInternalServerError,
				CodeInternal, "authentication failed")
			return
		}
		writeError(w, http.StatusNotImplemented, "not-implemented",
			"route not yet implemented: "+r.Method+" "+r.URL.Path)
	})
}

// Shutdown gracefully stops the engine: refuses new requests, waits for
// in-flight requests to drain (bounded by ctx), and signals the audit
// sink to flush.
//
// Calling Shutdown more than once is safe; subsequent calls return
// immediately.
//
// After Shutdown returns (or ctx expires), Handler() returns 503 for
// every request.
func (e *Engine) Shutdown(ctx context.Context) error {
	if !e.shut.CompareAndSwap(false, true) {
		return nil
	}
	close(e.shutdown)

	done := make(chan struct{})
	go func() {
		e.inflight.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

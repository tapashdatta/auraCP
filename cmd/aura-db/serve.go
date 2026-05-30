package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin/httpapi"
	"github.com/auracp/auracp/pkg/dbadmin/standalone"
)

func runServe(g globalFlags, args []string) error {
	fs := newFlagSet("serve", os.Stderr)
	listen := fs.String("listen", "", "override config.listen")
	tlsCert := fs.String("tls-cert", "", "override config.tls.cert_file")
	tlsKey := fs.String("tls-key", "", "override config.tls.key_file")
	dryRun := fs.Bool("dry-run", false, "validate config + open DBs + print routes, then exit 0")
	help := fs.Bool("help", false, "show help")
	fs.Usage = func() { fmt.Fprint(fs.Output(), helpServe) }
	if err := fs.Parse(args); err != nil {
		return userErr(err.Error())
	}
	if *help {
		fmt.Fprint(os.Stdout, helpServe)
		return nil
	}

	cfg, err := standalone.LoadConfig(g.configPath)
	if err != nil {
		return err
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	if *tlsCert != "" {
		cfg.TLS.CertFile = *tlsCert
	}
	if *tlsKey != "" {
		cfg.TLS.KeyFile = *tlsKey
	}
	if g.logLevel != "" {
		cfg.Logging.Level = g.logLevel
	}
	if g.logFormat != "" {
		cfg.Logging.Format = g.logFormat
	}

	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	app, err := standalone.Bootstrap(ctx, cfg)
	if err != nil {
		return err
	}
	defer app.Close()

	if *dryRun {
		fmt.Fprintf(os.Stderr, "aura-db: dry-run OK; config=%s listen=%s\n", g.configPath, cfg.Listen)
		return nil
	}

	pidPath := resolvePIDFile()
	if err := writePIDFile(pidPath); err != nil {
		return err
	}
	defer removePIDFile(pidPath)

	// v0.3.2-A: wire the durable saved-queries store so SQL Editor
	// saves survive daemon restarts. Bootstrap opened it at the path
	// declared in cfg.Storage.SavedDBPath (or HistoryDBPath as fallback).
	handler := httpapi.NewWithOptions(app.Engine, httpapi.Options{
		SavedStore: app.Saved,
	})
	handler = withRequestID(handler)
	handler = withAccessLog(handler, app.Logger)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	if cfg.TLS.CertFile != "" {
		srv.TLSConfig = &tls.Config{MinVersion: tlsMinVersion(cfg.TLS.MinVersion)}
	}

	go watchAuxiliarySignals(app)

	errCh := make(chan error, 1)
	go func() {
		var err error
		if cfg.TLS.CertFile != "" {
			err = srv.ListenAndServeTLS(cfg.TLS.CertFile, cfg.TLS.KeyFile)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	app.Logger.Info("aura-db ready", "listen", cfg.Listen, "version", version)

	select {
	case <-ctx.Done():
		app.Logger.Info("aura-db shutdown initiated")
	case err := <-errCh:
		if err != nil {
			return err
		}
	}

	// C8: stop accepting new HTTP requests FIRST, then drain the
	// engine's in-flight work. Sharing a 30s deadline across both is
	// fine — the engine completes already-accepted requests; we use
	// separate context-with-timeout so a wedged srv.Shutdown can't
	// starve Engine.Shutdown of its drain budget.
	httpCtx, cancelHTTP := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelHTTP()
	if err := srv.Shutdown(httpCtx); err != nil {
		app.Logger.Warn("http shutdown timed out", "err", err)
	}
	engCtx, cancelEng := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelEng()
	if err := app.Engine.Shutdown(engCtx); err != nil {
		app.Logger.Warn("engine shutdown timed out", "err", err)
	}
	app.Logger.Info("aura-db shutdown complete")
	return nil
}

func tlsMinVersion(s string) uint16 {
	switch strings.ToUpper(strings.ReplaceAll(s, " ", "")) {
	case "TLS1.2":
		return tls.VersionTLS12
	case "TLS1.3", "":
		return tls.VersionTLS13
	default:
		return tls.VersionTLS13
	}
}

// withRequestID stamps an X-Request-ID header on every response (and
// generates one if the client didn't send it). The standalone request
// ID is the ULID-encoded current time.
func withRequestID(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = standalone.NewULID()
		}
		w.Header().Set("X-Request-ID", id)
		h.ServeHTTP(w, r)
	})
}

// withAccessLog emits a single info-level log line per request. SQL
// bodies, passwords, and tokens are never logged.
func withAccessLog(h http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: 200}
		h.ServeHTTP(rw, r)
		logger.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"dur_ms", time.Since(start).Milliseconds(),
			"ip_class", standalone.IPClass(r),
		)
	})
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (w *responseWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// watchAuxiliarySignals handles SIGHUP (audit log reopen) and SIGUSR1
// (dump diagnostics). Returns when the channel is closed; serve()
// blocks on the primary context.
func watchAuxiliarySignals(app *standalone.Standalone) {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGHUP, syscall.SIGUSR1)
	for sig := range ch {
		switch sig {
		case syscall.SIGHUP:
			if app.Audit != nil {
				if err := app.Audit.Reopen(); err != nil {
					app.Logger.Warn("audit reopen failed", "err", err)
				} else {
					app.Logger.Info("audit log reopened")
				}
			}
		case syscall.SIGUSR1:
			dumpDiagnostics(app, os.Stderr)
		}
	}
}

func dumpDiagnostics(app *standalone.Standalone, w io.Writer) {
	fmt.Fprintf(w, "aura-db: pid=%d audit_drops=%d listen=%s\n",
		os.Getpid(), app.Audit.Drops(), app.Config.Listen)
}

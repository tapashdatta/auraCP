package standalone

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/history"
)

// Standalone is the fully wired engine + dependencies. Returned by
// Bootstrap; the caller mounts Engine.Handler() and calls Close on
// shutdown.
type Standalone struct {
	Engine    *dbadmin.Engine
	Auth      *Auth
	Conns     *Connections
	Audit     *FileAuditSink
	History   history.Store
	Store     *Store
	KEK       *KEK
	Config    Config
	Logger    *slog.Logger
	LogCloser io.Closer
}

// Close releases every wired resource in reverse order. Idempotent.
func (s *Standalone) Close() error {
	var firstErr error
	report := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.Audit != nil {
		report(s.Audit.Close())
	}
	if s.History != nil {
		report(s.History.Close())
	}
	if s.Store != nil {
		report(s.Store.Close())
	}
	if s.KEK != nil {
		s.KEK.Zero()
	}
	if s.LogCloser != nil {
		report(s.LogCloser.Close())
	}
	return firstErr
}

// Bootstrap wires Auth + ConnectionStore + AuditSink + history into a
// dbadmin.Engine. Order of operations:
//
//  1. Build logger.
//  2. Load KEK (env var or file).
//  3. Open audit sink (so we can record boot events later).
//  4. Open history store.
//  5. Open users/sessions/conns SQLite + migrate.
//  6. Construct Auth + Connections.
//  7. Call dbadmin.New(Options{...}).
//
// Bootstrap does NOT bind a listener — callers do that themselves
// after deciding TLS/non-TLS.
func Bootstrap(ctx context.Context, cfg Config) (*Standalone, error) {
	logger, closer, err := NewLogger(cfg.Logging.Level, cfg.Logging.Format, cfg.Logging.Destination)
	if err != nil {
		return nil, err
	}
	out := &Standalone{Config: cfg, Logger: logger, LogCloser: closer}

	// KEK
	kekPath := cfg.KEK.File
	if kekPath == "" {
		kekPath = DefaultKEKPath
	}
	kek, err := LoadKEK(kekPath)
	if err != nil {
		out.cleanupOnError()
		return nil, fmt.Errorf("standalone: load KEK: %w", err)
	}
	out.KEK = kek

	// Audit sink
	auditPath := cfg.Storage.AuditLogPath
	if auditPath != "" {
		if err := os.MkdirAll(filepath.Dir(auditPath), 0o750); err != nil {
			out.cleanupOnError()
			return nil, err
		}
	}
	sinkSigningKey, err := loadAuditSigningKey(cfg.Audit.ChainSigning)
	if err != nil {
		out.cleanupOnError()
		return nil, err
	}
	// Build configured forwarders BEFORE starting the sink so any
	// misconfiguration fails boot loudly (no silent "your SIEM is
	// receiving nothing" regressions — audit-forwarders-unwired).
	fwd, err := buildForwarder(cfg.Audit.Forwarders, logger)
	if err != nil {
		out.cleanupOnError()
		return nil, err
	}

	sink := &FileAuditSink{
		Path:         auditPath,
		SigningKey:   sinkSigningKey,
		SigningEvery: cfg.Audit.ChainSigning.EveryEvents,
		SigningClock: cfg.Audit.ChainSigning.Every,
		Logger:       logger,
		Forwarder:    fwd,
	}
	if err := sink.Start(); err != nil {
		out.cleanupOnError()
		return nil, err
	}
	out.Audit = sink

	// History
	hist, err := history.Open(ctx, cfg.Storage.HistoryDBPath, dbadmin.EngineMariaDB)
	if err != nil {
		out.cleanupOnError()
		return nil, fmt.Errorf("standalone: open history: %w", err)
	}
	out.History = hist

	// Store (users/sessions/conns/lockouts)
	store, err := OpenStore(ctx, cfg.Storage.DBPath)
	if err != nil {
		out.cleanupOnError()
		return nil, fmt.Errorf("standalone: open store: %w", err)
	}
	out.Store = store

	authRuntime := cfg.AuthRuntime()
	auth := NewAuth(store, kek, authRuntime)
	conns := NewConnections(store, kek)
	out.Auth = auth
	out.Conns = conns

	eng, err := dbadmin.New(dbadmin.Options{
		Auth:   auth,
		Conns:  conns,
		Audit:  sink,
		Config: cfg.ToDBAdminConfig(),
	})
	if err != nil {
		out.cleanupOnError()
		return nil, err
	}
	out.Engine = eng
	return out, nil
}

func (s *Standalone) cleanupOnError() {
	if s == nil {
		return
	}
	if s.Audit != nil {
		_ = s.Audit.Close()
	}
	if s.History != nil {
		_ = s.History.Close()
	}
	if s.Store != nil {
		_ = s.Store.Close()
	}
	if s.KEK != nil {
		s.KEK.Zero()
	}
	if s.LogCloser != nil {
		_ = s.LogCloser.Close()
	}
}

// buildForwarder constructs the per-config audit forwarder. Returns
// NopForwarder when no forwarders are configured. For multiple
// forwarders, wraps them in a MultiForwarder so a single misbehaving
// SIEM doesn't stop the others. Unknown `kind` values fail boot loudly;
// webhook forwarders enforce https:// URLs and require an HMAC secret
// (SEC-06 / audit-forwarders-unwired).
func buildForwarder(cfgs []AuditForwarderConfig, logger *slog.Logger) (Forwarder, error) {
	if len(cfgs) == 0 {
		return NopForwarder{}, nil
	}
	out := make([]Forwarder, 0, len(cfgs))
	for i, f := range cfgs {
		switch f.Kind {
		case "syslog":
			if f.Address == "" {
				return nil, fmt.Errorf("standalone: audit.forwarders[%d] syslog: address required", i)
			}
			facility := 0
			if f.Facility != "" {
				// Accept either a number or the well-known name (caller
				// can use raw integer string). Defaults to local6 (22)
				// when zero/blank.
				_, _ = fmt.Sscanf(f.Facility, "%d", &facility)
			}
			out = append(out, &SyslogForwarder{
				Address:  f.Address,
				Protocol: f.Protocol,
				Facility: facility,
			})
		case "webhook":
			if !strings.HasPrefix(f.URL, "https://") {
				return nil, fmt.Errorf("standalone: audit.forwarders[%d] webhook: URL must be https:// (got %q)", i, f.URL)
			}
			if f.SecretFile == "" {
				return nil, fmt.Errorf("standalone: audit.forwarders[%d] webhook: secret_file required for HMAC signing", i)
			}
			secret, rerr := os.ReadFile(f.SecretFile)
			if rerr != nil {
				return nil, fmt.Errorf("standalone: audit.forwarders[%d] webhook secret: %w", i, rerr)
			}
			if len(secret) == 0 {
				return nil, fmt.Errorf("standalone: audit.forwarders[%d] webhook secret %q is empty", i, f.SecretFile)
			}
			out = append(out, &WebhookForwarder{URL: f.URL, Secret: secret})
		default:
			return nil, fmt.Errorf("standalone: audit.forwarders[%d]: unknown kind %q (expected syslog|webhook)", i, f.Kind)
		}
	}
	if logger != nil {
		logger.Info("standalone: audit forwarders active", "count", len(out))
	}
	if len(out) == 1 {
		return out[0], nil
	}
	return MultiForwarder{Targets: out}, nil
}

// loadAuditSigningKey returns the HMAC key for chain-head signing, or
// nil if signing is disabled.
func loadAuditSigningKey(cfg ChainSigningConfig) ([]byte, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if cfg.KeyFile == "" {
		return nil, errors.New("standalone: audit.chain_signing.key_file required when enabled")
	}
	st, err := os.Stat(cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("standalone: stat audit signing key: %w", err)
	}
	if st.Mode().Perm()&^0o400 != 0 {
		return nil, fmt.Errorf("standalone: audit signing key %q mode %o broader than 0400", cfg.KeyFile, st.Mode().Perm())
	}
	return os.ReadFile(cfg.KeyFile)
}

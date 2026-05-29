package dbadmin

import (
	"fmt"
	"time"
)

// Config holds tunable engine policy. Zero-value Config is replaced by
// defaults during New(); operators override via the host's configuration
// system (yaml file, panel settings UI, etc.).
//
// Defaults match SECURITY.md §14. Any change to a default value here MUST
// be reflected in that document in the same commit.
type Config struct {
	Session    SessionConfig
	RateLimits RateLimitConfig
	Query      QueryConfig
	Audit      AuditConfig
	Network    NetworkConfig
}

// SessionConfig controls session lifecycle. Note that Auth implementations
// own session storage; these are policy values the engine surfaces to the
// implementation via Engine.Config.
type SessionConfig struct {
	// IdleTTL is reset on every authenticated request. Default: 15min.
	IdleTTL time.Duration

	// AbsoluteTTL is fixed at session creation; not slid by activity.
	// Default: 8 hours.
	AbsoluteTTL time.Duration

	// MaxConcurrent caps simultaneous sessions per user. Default: 5.
	// The oldest is evicted when the cap is exceeded.
	MaxConcurrent int
}

// RateLimitConfig controls per-IP and per-user rate limits enforced by the
// engine's middleware. Set any limit to 0 to disable it (not recommended).
type RateLimitConfig struct {
	// LoginPerIP15m caps login attempts per IP per 15 minutes.
	// Default: 10. Exceeding triggers a 15-minute lockout per IP.
	LoginPerIP15m int

	// LoginPerUser15m caps login attempts per user per 15 minutes.
	// Default: 5.
	LoginPerUser15m int

	// QueryPerUserPerMin caps SQL queries per user per minute, across
	// all connections. Default: 30.
	QueryPerUserPerMin int

	// QueryPerIPPerMin caps SQL queries per source IP per minute.
	// Default: 60.
	QueryPerIPPerMin int
}

// QueryConfig controls the resource limits applied to every query the
// engine dispatches to a driver. See SECURITY.md §6.5.
type QueryConfig struct {
	// TimeoutDefault is the default per-query timeout, passed to the
	// driver as a context deadline. Default: 30s.
	TimeoutDefault time.Duration

	// TimeoutMax is the upper bound an operator may configure per
	// connection. Default: 5min. Raising the per-connection timeout
	// above this requires owner + step-up.
	TimeoutMax time.Duration

	// ResultRowsDefault caps result rows returned to the engine per
	// query. Default: 10,000.
	ResultRowsDefault int

	// ResultRowsMax is the upper bound an operator may configure per
	// connection. Default: 100,000.
	ResultRowsMax int

	// ResultBytesDefault caps result body size in bytes. Default:
	// 50 MiB. Hit either ResultRowsDefault or ResultBytesDefault
	// first → result is truncated and CodeResultCapped is set.
	ResultBytesDefault int64

	// ResultBytesMax is the upper bound. Default: 500 MiB.
	ResultBytesMax int64

	// ConcurrentPerUserPerConn caps concurrent in-flight queries
	// per (user, connection). Default: 1 — additional queries queue.
	// Prevents accidental client-side parallel-query storms.
	ConcurrentPerUserPerConn int

	// ConcurrentMax is the engine-wide cap on in-flight queries.
	// Default: 3 per user across connections.
	ConcurrentMax int

	// SQLInputMaxBytes caps the size of an SQL editor input. Default:
	// 1 MiB. Hard ceiling: 16 MiB; configurable up to that with owner
	// + step-up.
	SQLInputMaxBytes int

	// PoolSizePerConn caps the database/sql or pgx connection pool's
	// MaxOpen per Aura DB connection. Default: 4. Hard ceiling: 16.
	// See SECURITY.md §6.5.
	PoolSizePerConn int

	// PoolIdleTimeout closes an idle pooled connection after this
	// duration. Default: 5 minutes. Keeps the panel's memory + DB
	// connection footprint flat for sites that aren't actively used.
	PoolIdleTimeout time.Duration
}

// AuditConfig controls the engine's audit emission behavior.
type AuditConfig struct {
	// SampleReadQueries is the fraction of read-only queries that
	// emit audit events. 0.0 disables read-query auditing; 1.0
	// audits everything. Default: 0.01.
	//
	// Write/DDL/dangerous queries are always audited regardless of
	// this value.
	SampleReadQueries float64

	// RedactSensitiveParams controls whether the classifier-detected
	// sensitive parameters (CREATE USER passwords, etc.) are
	// redacted in audit events. Default: true. We strongly recommend
	// never disabling this.
	RedactSensitiveParams bool
}

// NetworkConfig controls listener-level behavior. The engine doesn't open
// listeners itself (that's the host's job); these values are passed
// through to middleware that sets headers, enforces CORS, etc.
type NetworkConfig struct {
	// TLSMin is the minimum TLS version expected for inbound
	// connections, used to set Strict-Transport-Security and decide
	// whether to redirect HTTP→HTTPS. Default: "TLS1.3".
	TLSMin string

	// MTLSEnabled requires a client certificate on every request.
	// Default: false. When true, the engine refuses requests whose
	// TLS handshake didn't include a valid client cert.
	MTLSEnabled bool

	// CSPReportURI is the optional CSP violation-report destination.
	// Empty means CSP violations are silently dropped client-side.
	CSPReportURI string
}

// DefaultConfig returns the canonical secure-default Config. Used by New()
// when Options.Config is the zero value.
func DefaultConfig() Config {
	return Config{
		Session: SessionConfig{
			IdleTTL:       15 * time.Minute,
			AbsoluteTTL:   8 * time.Hour,
			MaxConcurrent: 5,
		},
		RateLimits: RateLimitConfig{
			LoginPerIP15m:      10,
			LoginPerUser15m:    5,
			QueryPerUserPerMin: 30,
			QueryPerIPPerMin:   60,
		},
		Query: QueryConfig{
			TimeoutDefault:           30 * time.Second,
			TimeoutMax:               5 * time.Minute,
			ResultRowsDefault:        10_000,
			ResultRowsMax:            100_000,
			ResultBytesDefault:       50 * 1024 * 1024,
			ResultBytesMax:           500 * 1024 * 1024,
			ConcurrentPerUserPerConn: 1,
			ConcurrentMax:            3,
			SQLInputMaxBytes:         1024 * 1024,
			PoolSizePerConn:          4,
			PoolIdleTimeout:          5 * time.Minute,
		},
		Audit: AuditConfig{
			SampleReadQueries:     0.01,
			RedactSensitiveParams: true,
		},
		Network: NetworkConfig{
			TLSMin:       "TLS1.3",
			MTLSEnabled:  false,
			CSPReportURI: "",
		},
	}
}

// merge applies non-zero values from `over` on top of `base`. Zero values
// in `over` mean "keep the base value." This lets hosts supply a partial
// Config and have the rest filled in from DefaultConfig.
func mergeConfig(base, over Config) Config {
	out := base

	if over.Session.IdleTTL != 0 {
		out.Session.IdleTTL = over.Session.IdleTTL
	}
	if over.Session.AbsoluteTTL != 0 {
		out.Session.AbsoluteTTL = over.Session.AbsoluteTTL
	}
	if over.Session.MaxConcurrent != 0 {
		out.Session.MaxConcurrent = over.Session.MaxConcurrent
	}

	if over.RateLimits.LoginPerIP15m != 0 {
		out.RateLimits.LoginPerIP15m = over.RateLimits.LoginPerIP15m
	}
	if over.RateLimits.LoginPerUser15m != 0 {
		out.RateLimits.LoginPerUser15m = over.RateLimits.LoginPerUser15m
	}
	if over.RateLimits.QueryPerUserPerMin != 0 {
		out.RateLimits.QueryPerUserPerMin = over.RateLimits.QueryPerUserPerMin
	}
	if over.RateLimits.QueryPerIPPerMin != 0 {
		out.RateLimits.QueryPerIPPerMin = over.RateLimits.QueryPerIPPerMin
	}

	if over.Query.TimeoutDefault != 0 {
		out.Query.TimeoutDefault = over.Query.TimeoutDefault
	}
	if over.Query.TimeoutMax != 0 {
		out.Query.TimeoutMax = over.Query.TimeoutMax
	}
	if over.Query.ResultRowsDefault != 0 {
		out.Query.ResultRowsDefault = over.Query.ResultRowsDefault
	}
	if over.Query.ResultRowsMax != 0 {
		out.Query.ResultRowsMax = over.Query.ResultRowsMax
	}
	if over.Query.ResultBytesDefault != 0 {
		out.Query.ResultBytesDefault = over.Query.ResultBytesDefault
	}
	if over.Query.ResultBytesMax != 0 {
		out.Query.ResultBytesMax = over.Query.ResultBytesMax
	}
	if over.Query.ConcurrentPerUserPerConn != 0 {
		out.Query.ConcurrentPerUserPerConn = over.Query.ConcurrentPerUserPerConn
	}
	if over.Query.ConcurrentMax != 0 {
		out.Query.ConcurrentMax = over.Query.ConcurrentMax
	}
	if over.Query.SQLInputMaxBytes != 0 {
		out.Query.SQLInputMaxBytes = over.Query.SQLInputMaxBytes
	}
	if over.Query.PoolSizePerConn != 0 {
		out.Query.PoolSizePerConn = over.Query.PoolSizePerConn
	}
	if over.Query.PoolIdleTimeout != 0 {
		out.Query.PoolIdleTimeout = over.Query.PoolIdleTimeout
	}

	if over.Audit.SampleReadQueries != 0 {
		out.Audit.SampleReadQueries = over.Audit.SampleReadQueries
	}
	// RedactSensitiveParams: false-default-on means we can't tell
	// "explicitly set false" from "zero value." We respect the
	// override only when the operator sets the value via the host's
	// config system; that path calls a SetRedactSensitive method on
	// the host's typed config, which then constructs a Config with
	// a sentinel. For now, default ON always.

	if over.Network.TLSMin != "" {
		out.Network.TLSMin = over.Network.TLSMin
	}
	if over.Network.MTLSEnabled {
		out.Network.MTLSEnabled = over.Network.MTLSEnabled
	}
	if over.Network.CSPReportURI != "" {
		out.Network.CSPReportURI = over.Network.CSPReportURI
	}

	return out
}

// validate checks that the Config is internally consistent. Called by
// New() before the engine is constructed. Returns an error suitable for
// surfacing to the operator.
func (c *Config) validate() error {
	if c.Session.IdleTTL > c.Session.AbsoluteTTL {
		return fmt.Errorf("dbadmin: SessionConfig.IdleTTL (%s) must not exceed AbsoluteTTL (%s)",
			c.Session.IdleTTL, c.Session.AbsoluteTTL)
	}
	if c.Session.MaxConcurrent < 1 {
		return fmt.Errorf("dbadmin: SessionConfig.MaxConcurrent must be >= 1, got %d", c.Session.MaxConcurrent)
	}

	if c.Query.TimeoutDefault > c.Query.TimeoutMax {
		return fmt.Errorf("dbadmin: QueryConfig.TimeoutDefault (%s) must not exceed TimeoutMax (%s)",
			c.Query.TimeoutDefault, c.Query.TimeoutMax)
	}
	if c.Query.ResultRowsDefault > c.Query.ResultRowsMax {
		return fmt.Errorf("dbadmin: QueryConfig.ResultRowsDefault (%d) must not exceed ResultRowsMax (%d)",
			c.Query.ResultRowsDefault, c.Query.ResultRowsMax)
	}
	if c.Query.ResultBytesDefault > c.Query.ResultBytesMax {
		return fmt.Errorf("dbadmin: QueryConfig.ResultBytesDefault (%d) must not exceed ResultBytesMax (%d)",
			c.Query.ResultBytesDefault, c.Query.ResultBytesMax)
	}
	if c.Query.ConcurrentPerUserPerConn > c.Query.ConcurrentMax {
		return fmt.Errorf("dbadmin: QueryConfig.ConcurrentPerUserPerConn (%d) must not exceed ConcurrentMax (%d)",
			c.Query.ConcurrentPerUserPerConn, c.Query.ConcurrentMax)
	}

	if c.Audit.SampleReadQueries < 0 || c.Audit.SampleReadQueries > 1 {
		return fmt.Errorf("dbadmin: AuditConfig.SampleReadQueries must be in [0, 1], got %v", c.Audit.SampleReadQueries)
	}

	return nil
}

package dbadmin

import (
	"strconv"
	"time"

	"github.com/auracp/auracp/internal/store"
	"github.com/auracp/auracp/pkg/dbadmin"
)

// Config mirrors the engine's typed knobs that the panel surfaces in its
// Settings UI. Zero-valued fields fall back to dbadmin.DefaultConfig()
// during ToEngine() — the secure default always wins for any unset knob.
type Config struct {
	// AuditPath is the SHA-256 hash-chained NDJSON log path. Required;
	// LoadFromStore supplies the default.
	AuditPath string

	// QueryTimeoutSec → dbadmin.QueryConfig.TimeoutDefault.
	QueryTimeoutSec int

	// QueryResultRows → dbadmin.QueryConfig.ResultRowsDefault.
	QueryResultRows int

	// QueryResultBytesMiB → dbadmin.QueryConfig.ResultBytesDefault.
	QueryResultBytesMiB int

	// QueryTimeoutMaxSec → dbadmin.QueryConfig.TimeoutMax. The
	// per-query hard ceiling — operators may dial Default below this,
	// but never above. Zero falls back to dbadmin.DefaultConfig's max
	// (10 minutes; see pkg/dbadmin/config.go).
	//
	// PR #10.5 / FIX-INT-4: previously the panel only surfaced Default
	// fields; the Max fields lived in dbadmin.DefaultConfig() and were
	// invisible to operators. A deploy that needs a lower hard ceiling
	// (e.g. 60s on a shared-tenant host where one wedged query starves
	// everyone) had no way to set one without code patches. Now all
	// three ceilings are wireable from the panel's settings table.
	QueryTimeoutMaxSec int

	// QueryResultRowsMax → dbadmin.QueryConfig.ResultRowsMax. Hard
	// ceiling on result row count; protects panel memory from a
	// SELECT * on a billion-row table.
	QueryResultRowsMax int

	// QueryResultBytesMaxMiB → dbadmin.QueryConfig.ResultBytesMax.
	// Hard ceiling on result byte count; protects panel memory from a
	// SELECT that returns a few rows containing very large BLOBs.
	QueryResultBytesMaxMiB int

	// AuditSampleReads → dbadmin.AuditConfig.SampleReadQueries.
	AuditSampleReads float64

	// SessionIdleMin — surfaced for forms-input parity. Note that since
	// we reuse the panel session, the engine's SessionConfig is mostly
	// informational; the value rides on the engine anyway.
	SessionIdleMin int

	// ShutdownTimeout bounds mountCloser.Close (FIX-5 / C1_INT-8). Zero
	// falls back to defaultShutdownTimeout (30s). Tests set this short
	// to validate the bounded-close contract; ops can dial it up if a
	// graceful drain needs more time on a busy node.
	ShutdownTimeout time.Duration
}

// defaultConfig returns the integration-package defaults. Note that
// dbadmin.DefaultConfig() also applies its own defaults during mergeConfig
// — these are only the panel-surface defaults that have no counterpart in
// the engine (AuditPath) or where we want a clear panel-side opinion.
func defaultConfig() Config {
	return Config{
		AuditPath: "/var/lib/auracp/aura-db/audit.ndjson",
	}
}

// LoadFromStore reads overrides from the panel's settings table. Unset
// keys fall back to defaultConfig() / dbadmin.DefaultConfig(). Never
// returns an error; malformed values are silently treated as unset (the
// panel UI gates values before they reach the table).
func LoadFromStore(st *store.Store) Config {
	c := defaultConfig()
	if st == nil {
		return c
	}
	if v, ok := st.GetSetting("aura_db_query_timeout_sec"); ok {
		if n, err := strconv.Atoi(v); err == nil {
			c.QueryTimeoutSec = n
		}
	}
	if v, ok := st.GetSetting("aura_db_query_result_rows"); ok {
		if n, err := strconv.Atoi(v); err == nil {
			c.QueryResultRows = n
		}
	}
	if v, ok := st.GetSetting("aura_db_query_result_bytes_mib"); ok {
		if n, err := strconv.Atoi(v); err == nil {
			c.QueryResultBytesMiB = n
		}
	}
	// PR #10.5 / FIX-INT-4: hard-ceiling settings (Max). Same naming
	// convention as the Default keys above (snake_case mirroring the
	// engine field names).
	if v, ok := st.GetSetting("aura_db_query_timeout_max_sec"); ok {
		if n, err := strconv.Atoi(v); err == nil {
			c.QueryTimeoutMaxSec = n
		}
	}
	if v, ok := st.GetSetting("aura_db_query_result_rows_max"); ok {
		if n, err := strconv.Atoi(v); err == nil {
			c.QueryResultRowsMax = n
		}
	}
	if v, ok := st.GetSetting("aura_db_query_result_bytes_max_mib"); ok {
		if n, err := strconv.Atoi(v); err == nil {
			c.QueryResultBytesMaxMiB = n
		}
	}
	if v, ok := st.GetSetting("aura_db_audit_sample_reads"); ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			c.AuditSampleReads = f
		}
	}
	if v, ok := st.GetSetting("aura_db_audit_path"); ok && v != "" {
		c.AuditPath = v
	}
	if v, ok := st.GetSetting("aura_db_session_idle_min"); ok {
		if n, err := strconv.Atoi(v); err == nil {
			c.SessionIdleMin = n
		}
	}
	return c
}

// ToEngine returns the engine-shaped Config. Only non-zero values are
// passed through; the engine's mergeConfig fills the rest from
// DefaultConfig().
func (c Config) ToEngine() dbadmin.Config {
	var out dbadmin.Config
	if c.QueryTimeoutSec > 0 {
		out.Query.TimeoutDefault = time.Duration(c.QueryTimeoutSec) * time.Second
	}
	if c.QueryResultRows > 0 {
		out.Query.ResultRowsDefault = c.QueryResultRows
	}
	if c.QueryResultBytesMiB > 0 {
		out.Query.ResultBytesDefault = int64(c.QueryResultBytesMiB) * 1024 * 1024
	}
	// FIX-INT-4: hard ceilings.
	if c.QueryTimeoutMaxSec > 0 {
		out.Query.TimeoutMax = time.Duration(c.QueryTimeoutMaxSec) * time.Second
	}
	if c.QueryResultRowsMax > 0 {
		out.Query.ResultRowsMax = c.QueryResultRowsMax
	}
	if c.QueryResultBytesMaxMiB > 0 {
		out.Query.ResultBytesMax = int64(c.QueryResultBytesMaxMiB) * 1024 * 1024
	}
	if c.AuditSampleReads > 0 {
		out.Audit.SampleReadQueries = c.AuditSampleReads
	}
	if c.SessionIdleMin > 0 {
		out.Session.IdleTTL = time.Duration(c.SessionIdleMin) * time.Minute
	}
	return out
}

package driver

import (
	"context"
	"errors"
	"time"
)

// SlowLogReader is the OPTIONAL capability a Conn implements when its
// backend exposes a slow-query log readable from SQL. The httpapi layer
// type-asserts against this interface; backends that don't satisfy it
// surface a typed ErrSlowLogUnavailable to the operator with a hint at
// the enabling SQL.
//
// Two backend strategies are supported in v0.3.1:
//
//   - MariaDB / MySQL: when @@slow_query_log = ON AND
//     @@log_output LIKE '%TABLE%', the slow-log entries are readable
//     via SELECT FROM mysql.slow_log. The reader polls that table for
//     rows whose start_time > Since and yields one SlowQueryRow per
//     entry. File-only log_output is refused with ErrSlowLogUnavailable
//     — file tailing would require host-side I/O which crosses the
//     "engine is a library" boundary.
//
//   - PostgreSQL: there is no native tail-able slow-log table. We use
//     pg_stat_statements (extension required) and emit one row per
//     statement digest whose mean_exec_time exceeds MinDuration. The
//     Mode field of MetaSlowLog reports "snapshot" so the UI surfaces
//     that this is a periodic poll, not a tail. When the extension is
//     not installed, ErrSlowLogUnavailable is returned with the
//     enabling SQL ("CREATE EXTENSION pg_stat_statements").
//
// All slow-log reads are read-only. The driver never modifies any
// configuration variable (e.g. it never SET GLOBAL slow_query_log=ON);
// enabling slow-log capture remains the operator's responsibility,
// surfaced via the unavailable-error message.
type SlowLogReader interface {
	// SlowLogProbe checks the prerequisites and returns the discovered
	// mode + a hint string the operator can act on. The hint is empty
	// when the feature is fully available. ErrSlowLogUnavailable is
	// returned only when the feature is plainly off and no rows can be
	// produced.
	SlowLogProbe(ctx context.Context) (SlowLogMode, string, error)

	// TailSlowLog opens an iterator that yields slow-query rows whose
	// start_time > Since AND query_time >= MinDuration. The iterator is
	// streaming for MariaDB (one SQL cursor over mysql.slow_log) and
	// snapshot for Postgres (one SELECT against pg_stat_statements).
	//
	// Limits.Timeout caps the read; Limits.MaxRows caps the row count
	// (the iterator returns ErrCapped at the cap). The caller is
	// responsible for re-invoking TailSlowLog with an updated Since to
	// "follow" the log; the driver does not loop internally.
	TailSlowLog(ctx context.Context, limits Limits, opts SlowLogOptions) (SlowLogIter, error)
}

// SlowLogMode identifies the underlying log-read strategy. The httpapi
// layer surfaces this in the meta frame so the UI can render an
// accurate "tail" vs "snapshot" badge.
type SlowLogMode uint8

const (
	// SlowLogModeUnavailable means no rows can be produced. Callers
	// should surface the hint string from SlowLogProbe rather than
	// open an iterator.
	SlowLogModeUnavailable SlowLogMode = iota

	// SlowLogModeTable means rows arrive from a real backing table
	// (MariaDB mysql.slow_log) and a Since cursor moves forward.
	SlowLogModeTable

	// SlowLogModeSnapshot means rows arrive from a digest-level view
	// (Postgres pg_stat_statements) and represent aggregate stats
	// rather than per-execution events.
	SlowLogModeSnapshot
)

// String returns the wire form of the mode.
func (m SlowLogMode) String() string {
	switch m {
	case SlowLogModeTable:
		return "table"
	case SlowLogModeSnapshot:
		return "snapshot"
	default:
		return "unavailable"
	}
}

// SlowLogOptions parameterise a TailSlowLog call. All fields are
// optional except Since (zero means "all available rows").
type SlowLogOptions struct {
	// Since narrows results to rows whose start_time > Since (table
	// mode) or whose digest was last seen after Since (snapshot mode).
	// The caller should bump Since to the last-emitted row's timestamp
	// before re-invoking to follow the log.
	Since time.Time

	// MinDuration filters rows whose query_time (table mode) or
	// mean_exec_time (snapshot mode) is below this threshold. Zero
	// means "no filter".
	MinDuration time.Duration

	// MaxRows caps the per-call row count. The driver also honours
	// Limits.MaxRows; whichever is lower wins. Zero means use limits.
	MaxRows int
}

// SlowLogIter streams SlowQueryRow records. Close must be called.
type SlowLogIter interface {
	// Next reads the next row. Returns (row, nil) on success, (zero,
	// ErrEOF) at end, or (zero, err) on backend error. The driver
	// maps backend errors to the typed sentinels in driver.go.
	Next(ctx context.Context) (SlowQueryRow, error)

	// Close releases the iterator. Idempotent.
	Close() error
}

// SlowQueryRow is one record from the slow-log feed. Field semantics
// vary slightly by mode:
//
//   - Table mode (MariaDB): one row = one execution. Calls is always 1,
//     MeanTime equals QueryTime. SQLDigest is the verbatim sql_text
//     (NOT redacted — the driver layer preserves it for the audit log;
//     the httpapi layer is responsible for redacting before emitting).
//
//   - Snapshot mode (Postgres): one row = one digest aggregate. Calls
//     is the total invocation count since the last pg_stat_statements
//     reset, QueryTime is the total elapsed time across all calls,
//     MeanTime is the per-call mean. SQLDigest is the normalised
//     statement from pg_stat_statements (parameters already replaced
//     with $1, $2, ...).
type SlowQueryRow struct {
	// Time is the row's reference timestamp. Table mode: start_time.
	// Snapshot mode: the snapshot-read time (NOT a per-call timestamp;
	// pg_stat_statements aggregates over the lifetime of the extension).
	Time time.Time

	// UserHost identifies the connection that ran the statement. Table
	// mode: mysql.slow_log.user_host verbatim. Snapshot mode: the
	// pg_roles.rolname of the statement's userid.
	UserHost string

	// QueryTime is the total elapsed time. Table mode: per-execution.
	// Snapshot mode: total across all Calls invocations.
	QueryTime time.Duration

	// LockTime is the per-execution lock wait. Table mode only; zero
	// for snapshot mode.
	LockTime time.Duration

	// MeanTime is QueryTime / Calls (snapshot) or = QueryTime (table).
	MeanTime time.Duration

	// RowsExamined / RowsSent describe the row-scan profile. Table
	// mode populates both; snapshot mode populates RowsExamined as
	// pg_stat_statements.rows (total touched) and leaves RowsSent at 0.
	RowsExamined int64
	RowsSent     int64

	// Calls is the invocation count this row represents. Always 1 for
	// table mode.
	Calls int64

	// Database is the schema/database context where the statement ran.
	Database string

	// SQLDigest is the statement text. See type-level comment for
	// mode-specific semantics. The httpapi layer redacts before
	// emitting to clients.
	SQLDigest string
}

// ErrSlowLogUnavailable is returned by SlowLogProbe (and surfaced
// through TailSlowLog) when the backend has not enabled the
// prerequisites. The error message carries operator-actionable hint
// text (e.g. "SET GLOBAL slow_query_log=ON, log_output='TABLE'").
var ErrSlowLogUnavailable = errors.New("driver: slow log unavailable")

package driver

import (
	"context"
	"errors"
	"fmt"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// Driver opens connections to one engine kind. Implementations are
// stateless — they construct fresh Conns on demand. The pool manager
// (pool.go) holds open Conns across requests; the Driver itself does
// not.
type Driver interface {
	// Engine reports which engine kind this driver handles. The
	// pool dispatches on this.
	Engine() dbadmin.EngineKind

	// Open establishes a backend connection using the supplied
	// connection metadata + decrypted credentials. The Conn is
	// already validated (a Ping has succeeded) when Open returns
	// nil.
	//
	// ctx applies only to Open's connection-establishment phase. The
	// returned Conn carries its own background context for keepalive;
	// per-query timeouts are passed via Query / Exec.
	//
	// Connection.SSHTunnel, when non-nil, causes Open to dial through
	// an SSH tunnel; see tunnel.go.
	Open(ctx context.Context, c *dbadmin.Connection, creds *dbadmin.Credentials, poolSize int) (Conn, error)
}

// Conn is one open backend connection. Safe for concurrent use; the
// underlying database/sql.DB or pgxpool.Pool routes concurrent calls to
// separate physical connections.
type Conn interface {
	// Query runs sql with args and returns a streaming Rows iterator
	// already wrapped in a LimitedRows that enforces the supplied
	// Limits (timeout, row cap, byte cap). The driver wraps sql/pgx
	// errors into the typed Err* errors declared in this package.
	//
	// ctx applies to BOTH connection acquisition (from the underlying
	// pool) AND the query execution. Limits.Timeout, when non-zero,
	// further constrains ctx via context.WithTimeout — whichever
	// deadline fires first wins.
	//
	// Passing a zero Limits is supported for tests, but in production
	// callers MUST pass Limits derived from Config.Query.* to ensure
	// resource caps are enforced. The engine layer rejects empty
	// Limits before dispatching to this method.
	Query(ctx context.Context, limits Limits, sql string, args ...any) (Rows, error)

	// Exec runs sql with args and returns the affected-rows + last-
	// insert-id result. Use for INSERT / UPDATE / DELETE / DDL where
	// the caller doesn't need rows back. Honors Limits.Timeout.
	Exec(ctx context.Context, limits Limits, sql string, args ...any) (Result, error)

	// Ping verifies the backend connection is alive. Returns an error
	// suitable for the engine's connection-test endpoint.
	Ping(ctx context.Context) error

	// ServerVersion returns the version string the backend reports
	// (e.g., "8.0.36-MariaDB" or "PostgreSQL 16.4"). Used by the UI
	// status bar.
	ServerVersion(ctx context.Context) (string, error)

	// Close releases the backend connection and any associated SSH
	// tunnel. Idempotent; subsequent Close calls return nil.
	Close() error
}

// Rows is a streaming, allocation-aware iterator over a query result.
//
// Usage pattern:
//
//	rows, err := conn.Query(ctx, "SELECT id, name FROM users")
//	if err != nil { ... }
//	defer rows.Close()
//	cols := rows.Columns()
//	for {
//	    vals, err := rows.Next(ctx)
//	    if err == ErrEOF { break }
//	    if err != nil { ... }
//	    // serialize vals against cols
//	}
//
// The returned []any values are typed per the driver's mapping rules
// (see Values()):
//
//   - NULL          → untyped nil
//   - integer types → int64
//   - float types   → float64
//   - bool          → bool
//   - text          → string
//   - bytes / BLOB  → []byte (defensive copy; safe to retain)
//   - date / time / timestamp → time.Time (UTC)
//   - JSON          → []byte (raw, to preserve formatting)
//   - everything else → string (best-effort via the driver's text format)
//
// The iterator MUST be closed exactly once. Closing before exhaustion
// is safe; the driver cancels the running query.
type Rows interface {
	// Columns returns metadata for each column in the result. Stable
	// across Next() calls; safe to call before the first Next.
	Columns() []ColumnInfo

	// Next reads the next row.
	//
	// Returns:
	//   - (vals, nil)         when a row is available.
	//   - (nil,  ErrEOF)      when the iterator is exhausted.
	//   - (nil,  ErrCapped)   when a result-row or result-byte cap
	//                         tripped. The cap is set on the Rows
	//                         instance by the caller via limits.go's
	//                         wrappers.
	//   - (nil,  <other>)     on backend errors (mapped via classifyErr).
	//
	// The returned slice's backing array is reused on the next call;
	// callers wanting to retain values past the next Next() MUST copy.
	Next(ctx context.Context) ([]any, error)

	// Close releases the iterator. Idempotent.
	Close() error
}

// Result describes the outcome of an Exec call.
type Result struct {
	// RowsAffected is the count of rows the statement modified.
	// Negative when the backend doesn't report it (rare).
	RowsAffected int64

	// LastInsertID is the auto-generated PK value from a single-row
	// INSERT, when the backend supports it. MySQL always populates;
	// Postgres returns 0 (Postgres uses RETURNING instead and the
	// engine handles that as Query, not Exec).
	LastInsertID int64
}

// ColumnInfo describes one column of a result set. Fields are populated
// best-effort from the driver's metadata; absent capabilities yield
// zero values rather than errors.
type ColumnInfo struct {
	// Name is the column label (alias or source-column name).
	Name string

	// DatabaseTypeName is the engine-specific type name (e.g.,
	// "VARCHAR(255)", "INT", "JSONB", "TIMESTAMPTZ"). The HTTP layer
	// surfaces this in the row-grid header; the frontend uses it for
	// type-aware cell editors.
	DatabaseTypeName string

	// Nullable reports whether NULL values are possible in this
	// column. False when the driver doesn't expose nullability.
	Nullable bool

	// PrimaryKey reports whether the column is a primary key
	// component. Driver-best-effort; PR #4 (schema reader) provides
	// the authoritative answer via information_schema/pg_catalog.
	PrimaryKey bool
}

// ─── Typed errors ────────────────────────────────────────────────────

// ErrEOF is returned by Rows.Next when the iterator is exhausted. Not
// a real error; callers compare with errors.Is.
var ErrEOF = errors.New("driver: end of rows")

// ErrCapped is returned by Rows.Next when a row or byte cap tripped.
// The HTTP layer surfaces this as CodeResultCapped with the operator-
// visible message.
var ErrCapped = errors.New("driver: result capped")

// ErrClosed is returned by Rows.Next when the iterator has already been
// closed. Distinct from ErrEOF so callers can distinguish "no more
// rows" from "use after close" bugs.
var ErrClosed = errors.New("driver: rows closed")

// ErrCanceled is returned when the caller's context was cancelled
// (not due to a deadline; see ErrTimeout for the deadline case).
var ErrCanceled = errors.New("driver: query cancelled")

// ErrTimeout is returned when ctx exceeded its deadline mid-query.
// Mapped from context.DeadlineExceeded + driver-specific timeout errors.
var ErrTimeout = errors.New("driver: query timeout")

// ErrAuth is returned when the backend refused the credentials.
var ErrAuth = errors.New("driver: authentication failed")

// ErrUnavailable is returned when the backend can't be reached
// (network error, server down, DNS failure).
var ErrUnavailable = errors.New("driver: backend unavailable")

// ErrSyntax is returned when the backend rejected the statement at
// parse time. The verbatim backend message is wrapped via fmt.Errorf
// and surfaced to the operator.
var ErrSyntax = errors.New("driver: syntax error")

// ErrPermission is returned when the backend refused the action due
// to its own (database-level) RBAC. Aura DB's RBAC has already
// authorized; this is a SECOND-LEVEL refusal from the database itself.
var ErrPermission = errors.New("driver: backend permission denied")

// ErrConflict is returned on constraint / unique violations.
var ErrConflict = errors.New("driver: backend conflict")

// ErrNotFound is returned when the backend reports a missing database,
// schema, or other addressed-but-absent object. Distinct from
// ErrSyntax (the query was well-formed; the object simply doesn't
// exist).
var ErrNotFound = errors.New("driver: not found")

// ─── Helpers ────────────────────────────────────────────────────────

// For returns the bundled driver for an engine, or an error if the
// engine isn't supported.
//
// The bundled drivers are stateless singletons; safe to share.
func For(engine dbadmin.EngineKind) (Driver, error) {
	switch engine {
	case dbadmin.EngineMariaDB:
		return mysqlDriver, nil
	case dbadmin.EnginePostgres:
		return postgresDriver, nil
	default:
		return nil, fmt.Errorf("driver: unsupported engine %v", engine)
	}
}

// Singleton driver instances; constructed at package init.
var (
	mysqlDriver    Driver = &mysqlDriverImpl{}
	postgresDriver Driver = &postgresDriverImpl{}
)

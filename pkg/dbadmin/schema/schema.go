package schema

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/driver"
)

// Options configures a Reader built via ForWithOptions. Zero values fall
// back to package defaults (30s timeout, 50K rows, 50MB), preserving the
// behavior of the plain For(...) factory.
//
// PR #4.5: Options.Limits plumbs the engine's Config.Query.* into schema
// reads so operators can shrink (or, with the relevant overrides, grow)
// the read budget without editing this package. Per-connection cache
// isolation lives on CacheConfig.Bucket — Cache wraps Reader, so the
// bucket belongs there, not on the underlying Reader.
type Options struct {
	// Limits, when non-zero, overrides the package's defaultLimits()
	// for every backend query the Reader issues. Fields that are zero
	// fall back to defaults (i.e. partial overrides are allowed).
	Limits driver.Limits
}

// Reader is the engine-agnostic schema-metadata interface. Implementations
// query information_schema (MySQL/MariaDB) or pg_catalog (PostgreSQL) via
// the supplied driver.Conn and produce normalized results.
//
// Every method takes a context; the driver layer enforces its own
// resource limits. Schema reads default to this package's 30s /
// 50K-rows / 50MB caps via defaultLimits(); operators can override
// per-Reader via ForWithOptions(..., Options{Limits: ...}) — PR #4.5
// plumbs engine Config.Query.* through that path.
//
// Identifier inputs (database, schema, table names) are validated by
// every method that takes one. On failure the method returns
// ErrInvalidIdentifier; the implementation does NOT panic on operator
// input. The Cache wrapper validates identifiers and returns
// ErrInvalidIdentifier without invoking the inner reader.
type Reader interface {
	// ListDatabases returns the databases visible to the connection.
	// MySQL: SHOW DATABASES (or information_schema.schemata).
	// Postgres: SELECT datname FROM pg_database WHERE has-connect-priv.
	ListDatabases(ctx context.Context) ([]string, error)

	// ListSchemas returns the schemas in a database. MySQL conflates
	// databases and schemas; this method returns a single-element slice
	// with the database name for MySQL connections.
	ListSchemas(ctx context.Context, database string) ([]string, error)

	// ListTables returns table + view summaries in a schema. Ordered
	// by name.
	ListTables(ctx context.Context, schema string) ([]TableSummary, error)

	// GetTable returns full table metadata (columns, indexes, FKs,
	// triggers, primary-key columns).
	GetTable(ctx context.Context, schema, table string) (*Table, error)

	// ListViews returns views in a schema. Distinguished from tables
	// here because the operator UI typically renders them under a
	// separate node.
	ListViews(ctx context.Context, schema string) ([]ViewSummary, error)

	// ListFunctions returns stored functions in a schema.
	ListFunctions(ctx context.Context, schema string) ([]FunctionSummary, error)

	// ListProcedures returns stored procedures (MySQL-only; Postgres
	// returns an empty slice as procedures are mostly the same as
	// functions in Postgres land).
	ListProcedures(ctx context.Context, schema string) ([]ProcedureSummary, error)

	// ListTriggers returns triggers in a schema.
	ListTriggers(ctx context.Context, schema string) ([]TriggerSummary, error)

	// Engine reports which engine this Reader serves.
	Engine() dbadmin.EngineKind
}

// For returns the bundled Reader implementation for the given conn's
// engine. The conn is borrowed for the lifetime of the Reader; callers
// retain ownership and remain responsible for closing it.
//
// Equivalent to ForWithOptions(c, engine, Options{}); schema reads use
// this package's defaultLimits().
func For(c driver.Conn, engine dbadmin.EngineKind) (Reader, error) {
	return ForWithOptions(c, engine, Options{})
}

// ForWithOptions returns the bundled Reader implementation for the
// given conn's engine, using opt.Limits for every backend query. Pass
// Options{} for legacy defaults; pass opt.Limits derived from the
// engine's Config.Query to honor operator policy.
//
// PR #4.5: replaces the hard-wired defaultLimits() path with one where
// the engine's Config.Query.Timeout / ResultRows / ResultBytes apply.
func ForWithOptions(c driver.Conn, engine dbadmin.EngineKind, opt Options) (Reader, error) {
	lim := resolveLimits(opt.Limits)
	switch engine {
	case dbadmin.EngineMariaDB:
		return &mysqlReader{conn: c, limits: lim}, nil
	case dbadmin.EnginePostgres:
		return &postgresReader{conn: c, limits: lim}, nil
	case dbadmin.EngineMongo:
		return newMongoReader(c, lim)
	default:
		return nil, fmt.Errorf("schema: unsupported engine %v", engine)
	}
}

// resolveLimits merges caller-supplied Limits with defaultLimits(), so
// zero fields fall back to defaults. Callers can supply just a Timeout
// (the common case from Config.Query.TimeoutDefault) and keep the other
// caps at package defaults.
func resolveLimits(l driver.Limits) driver.Limits {
	d := defaultLimits()
	if l.Timeout > 0 {
		d.Timeout = l.Timeout
	}
	if l.MaxRows > 0 {
		d.MaxRows = l.MaxRows
	}
	if l.MaxBytes > 0 {
		d.MaxBytes = l.MaxBytes
	}
	if l.MaxBytesPerCell > 0 {
		d.MaxBytesPerCell = l.MaxBytesPerCell
	}
	return d
}

// ─── Normalized model types ──────────────────────────────────────────

// TableSummary is the lightweight per-table info returned by ListTables.
// Full column / index / FK details require GetTable.
type TableSummary struct {
	Schema       string // owning schema
	Name         string
	Kind         TableKind // table or view
	Comment      string    // operator-set DDL comment, if any
	RowsEstimate int64     // from information_schema / pg_class.reltuples — approximate
	Engine       string    // MySQL only: InnoDB, MyISAM, etc. Empty for Postgres.
}

// TableKind distinguishes tables from views (and materialized views, in
// Postgres). Used by the UI to choose the right icon.
type TableKind uint8

const (
	KindTable TableKind = iota
	KindView
	KindMaterializedView // Postgres only
)

// String returns a stable lowercased name for use in API payloads.
func (k TableKind) String() string {
	switch k {
	case KindTable:
		return "table"
	case KindView:
		return "view"
	case KindMaterializedView:
		return "matview"
	default:
		return "unknown"
	}
}

// Table is the full table metadata GetTable returns.
type Table struct {
	Schema      string
	Name        string
	Kind        TableKind
	Comment     string
	Columns     []Column
	PrimaryKey  []string // column names in PK order; empty if no PK
	Indexes     []Index
	ForeignKeys []ForeignKey
	Triggers    []TriggerSummary

	// Engine-specific extras: MySQL surfaces ENGINE/CHARACTER_SET,
	// Postgres surfaces TABLESPACE. Operators see these in the
	// inspector pane.
	Extras map[string]string
}

// Column describes one column of a table or view.
type Column struct {
	Name            string
	Position        int    // 1-indexed; matches information_schema.ordinal_position
	DataType        string // engine-normalized: VARCHAR(255), BIGINT, JSONB, etc.
	Nullable        bool
	Default         string // SQL expression; empty if no default
	Comment         string
	IsPrimaryKey    bool
	IsAutoIncrement bool // MySQL AUTO_INCREMENT, Postgres SERIAL/IDENTITY
	IsGenerated     bool // generated/computed columns
	CharacterSet    string // MySQL only
	Collation       string
}

// Index describes a per-table index.
type Index struct {
	Name      string
	Columns   []string // in index order
	Unique    bool
	Primary   bool   // true when this index backs the table's PK
	Method    string // BTREE, HASH, GIN, GIST, BRIN, etc.
	Predicate string // WHERE clause for partial indexes (Postgres); empty otherwise
	Comment   string
}

// ForeignKey describes a per-table FK constraint.
type ForeignKey struct {
	Name              string
	Columns           []string // local columns in order
	ReferencedSchema  string
	ReferencedTable   string
	ReferencedColumns []string // referenced columns in order
	OnDelete          string   // CASCADE, RESTRICT, SET NULL, NO ACTION, SET DEFAULT
	OnUpdate          string
}

// ViewSummary describes one view. Full view definition (the SELECT) is
// fetched separately via GetTable when Kind == KindView; this struct is
// the listing-time lightweight info.
type ViewSummary struct {
	Schema     string
	Name       string
	Comment    string
	Updatable  bool // information_schema.views.is_updatable
	Definition string
}

// FunctionSummary describes a stored function. Body fetched separately
// when the operator opens the function detail view.
type FunctionSummary struct {
	Schema      string
	Name        string
	Language    string // SQL, PLPGSQL, JS (Postgres), PHP (rare)
	ReturnType  string
	Arguments   string // pre-formatted argument list
	Comment     string
	IsAggregate bool // Postgres only
}

// ProcedureSummary — same shape as FunctionSummary, but for MySQL
// stored procedures.
type ProcedureSummary struct {
	Schema    string
	Name      string
	Language  string
	Arguments string
	Comment   string
}

// TriggerSummary describes one trigger.
type TriggerSummary struct {
	Schema  string
	Table   string // table the trigger fires on
	Name    string
	// Event is the firing event(s). For triggers that fire on multiple
	// events (Postgres allows e.g. "INSERT OR UPDATE"), this is a list
	// of events separated by " OR ". MySQL triggers only fire on one
	// event so this is always a single token there.
	Event   string
	Timing  string // BEFORE, AFTER, INSTEAD OF
	Comment string
	// Definition is the full CREATE TRIGGER DDL for both engines.
	// On Postgres it comes directly from pg_get_triggerdef; on MySQL
	// it is synthesized from the available information_schema columns
	// so the shape matches across engines.
	Definition string
}

// ─── Errors ──────────────────────────────────────────────────────────

// ErrInvalidIdentifier is returned when an identifier fails validation.
// Callers should treat this as a hard refusal — not a syntax error to
// retry.
var ErrInvalidIdentifier = errors.New("schema: invalid identifier")

// ErrTableNotFound is returned by GetTable when the table doesn't
// exist (or isn't visible to the connection). Distinct from a generic
// query error so callers can map to HTTP 404 cleanly.
var ErrTableNotFound = errors.New("schema: table not found")

// CappedError wraps driver.ErrCapped with the partial result the
// schema reader had managed to collect before the row / byte cap
// tripped. Returned by reader methods when an underlying
// information_schema / pg_catalog query exceeds the configured Limits
// mid-stream. The caller can:
//
//   - Treat it as a hard error (errors.Is(err, driver.ErrCapped) is
//     true) and surface "result capped" to the operator with a hint to
//     increase the limits.
//   - Recover the partial slice via errors.As(err, &capped) /
//     CappedError.Partial() — useful when the caller wants to render
//     "first N items shown" alongside a banner.
//
// PR #4 silently returned the partial slice and dropped the cap signal;
// PR #4.5 routes it through this typed error so the operator UI can
// distinguish complete from partial reads.
type CappedError struct {
	// Got is the partial result accumulated before the cap tripped.
	// Its concrete type matches the method that returned it (e.g.
	// []TableSummary for ListTables, *Table for GetTable, etc.).
	Got any

	// Method is the symbolic name of the reader method that capped
	// (e.g. "ListTables", "GetTable.fillColumns"). Useful for logs.
	Method string

	// Inner is the underlying driver error (always wraps driver.ErrCapped).
	Inner error
}

// Error implements error.
func (e *CappedError) Error() string {
	if e.Method != "" {
		return fmt.Sprintf("schema: %s capped: %v", e.Method, e.Inner)
	}
	return fmt.Sprintf("schema: capped: %v", e.Inner)
}

// Unwrap exposes the underlying driver error so errors.Is/As walk
// through to driver.ErrCapped.
func (e *CappedError) Unwrap() error { return e.Inner }

// Partial returns the accumulated partial result; callers can type-
// assert it to the expected return type of the call that capped.
func (e *CappedError) Partial() any { return e.Got }

// newCappedError constructs a CappedError. Internal helper.
func newCappedError(method string, got any, inner error) error {
	return &CappedError{Method: method, Got: got, Inner: inner}
}

// ─── Identifier validation ───────────────────────────────────────────

// identifierRE matches the safe identifier shape: starts with a letter
// or underscore, followed by letters / digits / underscores / dollar
// signs, up to 63 characters total. 63 is Postgres's NAMEDATALEN limit
// and is also accepted by MySQL (whose ceiling is 64). This is the
// intersection of MySQL's and Postgres's quoted-identifier rules
// without requiring quoting. Anything more exotic must use the full
// quoted-identifier path (not yet exposed; tracked for a future PR
// that adds delimited-identifier escape support).
var identifierRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_$]{0,62}$`)

// ValidateIdentifier returns ErrInvalidIdentifier if name fails the
// safe-identifier pattern: starts with a letter or underscore,
// followed by letters / digits / underscores / dollar signs, up to
// 63 characters total (Postgres NAMEDATALEN). Callers MUST run this on
// every operator-supplied identifier before reaching the database.
//
// Empty strings are explicitly rejected — they would match no rows in
// a parameterized query but might match unintended ones in some
// edge cases (e.g., empty schema name matching session default).
func ValidateIdentifier(name string) error {
	if name == "" {
		return fmt.Errorf("%w: empty", ErrInvalidIdentifier)
	}
	if !identifierRE.MatchString(name) {
		return fmt.Errorf("%w: %q does not match [a-zA-Z_][a-zA-Z0-9_$]{0,62}", ErrInvalidIdentifier, name)
	}
	return nil
}

// ValidateMongoIdentifier returns ErrInvalidIdentifier if name fails
// the MongoDB-specific safe-identifier pattern. Unlike SQL identifiers
// (which the relational engines quote), MongoDB database, collection,
// and field names are referenced as raw JSON keys — so we apply a
// distinct allowlist that is wider than the SQL one (covers `.` and
// `-` which are legal in BSON keys) but still excludes characters that
// would let an operator smuggle a query operator into a structured
// payload: '$', '"', '\', '\x00', and whitespace.
//
// Specifically we accept any byte sequence of 1..120 bytes that
// contains none of: '/', '\\', '"', '$', '\x00', ' '. The 120-byte
// ceiling matches MongoDB's documented limit on namespace component
// length (database + ".$" + collection ≤ 255 bytes on legacy storage
// engines; keeping each component ≤ 120 bytes is well within the
// safety envelope and matches the convention the official drivers use
// in their own validators).
//
// Reserved names — '', 'admin', 'local', 'config' as DATABASE names —
// are NOT rejected here because operators with the right role may
// legitimately address them; ListDatabases filters them from the
// default listing instead.
//
// v0.3.2-F.
func ValidateMongoIdentifier(name string) error {
	if name == "" {
		return fmt.Errorf("%w: empty", ErrInvalidIdentifier)
	}
	if len(name) > 120 {
		return fmt.Errorf("%w: %q exceeds 120-byte length cap", ErrInvalidIdentifier, name)
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		switch b {
		case '/', '\\', '"', '$', 0x00:
			return fmt.Errorf("%w: %q contains reserved byte 0x%02x at offset %d", ErrInvalidIdentifier, name, b, i)
		}
		if b == ' ' || b == '\t' || b == '\n' || b == '\r' {
			return fmt.Errorf("%w: %q contains whitespace at offset %d", ErrInvalidIdentifier, name, i)
		}
	}
	return nil
}

// cloneTable returns a deep copy of t. Used by the cache so concurrent
// callers can't mutate each other's view of a cached *Table. Returns
// nil if t is nil.
func cloneTable(t *Table) *Table {
	if t == nil {
		return nil
	}
	cp := &Table{
		Schema:  t.Schema,
		Name:    t.Name,
		Kind:    t.Kind,
		Comment: t.Comment,
	}
	if t.PrimaryKey != nil {
		cp.PrimaryKey = append([]string(nil), t.PrimaryKey...)
	}
	if t.Columns != nil {
		cp.Columns = append([]Column(nil), t.Columns...)
	}
	if t.Indexes != nil {
		cp.Indexes = make([]Index, len(t.Indexes))
		for i, idx := range t.Indexes {
			cp.Indexes[i] = idx
			if idx.Columns != nil {
				cp.Indexes[i].Columns = append([]string(nil), idx.Columns...)
			}
		}
	}
	if t.ForeignKeys != nil {
		cp.ForeignKeys = make([]ForeignKey, len(t.ForeignKeys))
		for i, fk := range t.ForeignKeys {
			cp.ForeignKeys[i] = fk
			if fk.Columns != nil {
				cp.ForeignKeys[i].Columns = append([]string(nil), fk.Columns...)
			}
			if fk.ReferencedColumns != nil {
				cp.ForeignKeys[i].ReferencedColumns = append([]string(nil), fk.ReferencedColumns...)
			}
		}
	}
	if t.Triggers != nil {
		cp.Triggers = append([]TriggerSummary(nil), t.Triggers...)
	}
	if t.Extras != nil {
		cp.Extras = make(map[string]string, len(t.Extras))
		for k, v := range t.Extras {
			cp.Extras[k] = v
		}
	}
	return cp
}

// ─── Cache types — see cache.go for implementation ───────────────────

// CacheConfig configures a Cache wrapping a Reader. Zero values use
// reasonable defaults.
type CacheConfig struct {
	// TTL is how long entries remain fresh. Default 5 minutes.
	TTL time.Duration

	// MaxEntries caps the total cached entries (across keys + types).
	// Default 1000. Excess entries evicted LRU.
	MaxEntries int

	// Bucket is an opaque per-connection (or per-role) discriminator the
	// cache prepends to every key. PR #4.5: prevents cross-user cache
	// poisoning where two operators with different RBAC visibility into
	// the same schema would share a GetTable cache entry.
	//
	// Empty bucket = legacy unbucketed key space (PR #4 behavior). A
	// non-empty bucket is treated as opaque; the connection ID is a
	// reasonable choice.
	Bucket string
}

// limits used as the default driver.Limits for schema-level queries.
// Schema queries are read-only against system catalogs; we accept the
// engine's default timeout and cap rows generously (information_schema
// rarely returns more than a few thousand rows even for huge schemas).
func defaultLimits() driver.Limits {
	return driver.Limits{
		Timeout:  30 * time.Second,
		MaxRows:  50_000,
		MaxBytes: 50 * 1024 * 1024,
	}
}

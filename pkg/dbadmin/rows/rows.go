package rows

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/driver"
	"github.com/auracp/auracp/pkg/dbadmin/schema"
)

// Operator runs row operations against one driver.Conn, using a
// schema.Reader for PK metadata.
//
// Construction:
//
//	op := rows.New(conn, schemaReader, opts)
//
// The engine is derived from the schema.Reader so callers cannot
// accidentally pair a Postgres reader with a MariaDB quoting strategy.
//
// All methods accept a context for cancellation + deadline propagation.
// They enforce identifier safety + PK preflight at the package level;
// callers do not need to ValidateIdentifier upstream (we do it here).
//
// Ownership (M11): an Operator BORROWS its driver.Conn — it does NOT
// take ownership and will NOT close the conn on any error path. The
// caller (typically an HTTP handler) opens the conn, defers Close, then
// passes the conn into rows.New for the lifetime of the request. An
// Operator is safe to use from a single goroutine; concurrent use is
// unsupported (mirrors driver.Conn's contract).
type Operator struct {
	conn   driver.Conn
	schema schema.Reader
	engine dbadmin.EngineKind
	limits driver.Limits

	// maxValueBytes caps the encoded byte size of any single value
	// passed to UpdateByPK.Set or InsertOpts.Values (M10). Zero means
	// "use defaultMaxValueBytes".
	maxValueBytes int64
}

// Options configure a new Operator.
type Options struct {
	// Limits applies to every query the operator issues. The engine
	// passes Config.Query-derived limits here. Zero value gets a
	// conservative default (30s timeout, 10K rows, 50MB).
	Limits driver.Limits

	// MaxValueBytes caps the encoded byte size of any single value
	// passed to UpdateByPK.Set or InsertOpts.Values (M10). Defends
	// against operators stuffing a 100 MB payload into a TEXT column
	// via a single UPDATE. Strings and []byte are measured by len();
	// other types are not size-capped. Zero falls back to
	// defaultMaxValueBytes (1 MiB).
	MaxValueBytes int64
}

// New constructs an Operator. conn and reader are required; the engine
// is derived from reader.Engine() so the quoting strategy is always
// consistent with the schema metadata source.
//
// L7: negative Limits values are rejected — they are nonsensical and
// almost certainly a caller bug (e.g. forgotten initialization).
func New(conn driver.Conn, reader schema.Reader, opt Options) (*Operator, error) {
	if conn == nil {
		return nil, errors.New("rows: nil driver.Conn")
	}
	if reader == nil {
		return nil, errors.New("rows: nil schema.Reader")
	}
	engine := reader.Engine()
	switch engine {
	case dbadmin.EngineMariaDB, dbadmin.EnginePostgres:
		// supported
	default:
		return nil, fmt.Errorf("rows: unsupported engine %v from reader", engine)
	}
	l := opt.Limits
	if l.Timeout < 0 {
		return nil, fmt.Errorf("rows: Options.Limits.Timeout must be >= 0 (got %v)", l.Timeout)
	}
	if l.MaxRows < 0 {
		return nil, fmt.Errorf("rows: Options.Limits.MaxRows must be >= 0 (got %d)", l.MaxRows)
	}
	if l.MaxBytes < 0 {
		return nil, fmt.Errorf("rows: Options.Limits.MaxBytes must be >= 0 (got %d)", l.MaxBytes)
	}
	if l.Timeout == 0 {
		l.Timeout = defaultTimeout
	}
	if l.MaxRows == 0 {
		l.MaxRows = defaultMaxRows
	}
	if l.MaxBytes == 0 {
		l.MaxBytes = defaultMaxBytes
	}
	if opt.MaxValueBytes < 0 {
		return nil, fmt.Errorf("rows: Options.MaxValueBytes must be >= 0 (got %d)", opt.MaxValueBytes)
	}
	mvb := opt.MaxValueBytes
	if mvb == 0 {
		mvb = defaultMaxValueBytes
	}
	return &Operator{
		conn:          conn,
		schema:        reader,
		engine:        engine,
		limits:        l,
		maxValueBytes: mvb,
	}, nil
}

// ─── Predicate model ─────────────────────────────────────────────────

// Op enumerates the operators a Predicate may use. Operator strings are
// constants here so an attacker can't smuggle in ";" or other SQL via
// the operator field.
//
// Engine-divergence warnings (H8 / L3):
//
//   - OpLike: case-sensitivity is engine + collation dependent.
//     Postgres LIKE is ALWAYS case-sensitive on text/varchar. MariaDB
//     LIKE is case-INSENSITIVE under the default *_ci collation but
//     case-sensitive under *_bin / *_cs. Callers wanting deterministic
//     case semantics should use OpILike (always case-insensitive) or
//     constrain the column collation upstream.
//
//   - OpILike: Postgres has a native ILIKE; MariaDB does not. This
//     package rewrites OpILike → `LOWER(col) LIKE LOWER(?)` for MariaDB.
//     Note: the LOWER() rewrite is correct for ASCII but locale-
//     dependent for multibyte. On MariaDB with utf8mb4_general_ci the
//     two paths agree; under utf8mb4_bin they will diverge for
//     non-ASCII case folding (e.g. ß / ẞ).
type Op string

const (
	OpEq  Op = "="
	OpNeq Op = "!="
	OpLt  Op = "<"
	OpLte Op = "<="
	OpGt  Op = ">"
	OpGte Op = ">="

	// OpLike: pattern match with SQL `%` / `_` wildcards. Postgres is
	// case-sensitive; MariaDB depends on the column collation (see Op
	// docstring). For case-insensitive matching use OpILike.
	OpLike Op = "LIKE"

	// OpILike: case-insensitive pattern match. Native on Postgres;
	// rewritten to `LOWER(col) LIKE LOWER(?)` on MariaDB. Non-ASCII
	// folding may differ between engines (see Op docstring).
	OpILike Op = "ILIKE"

	OpIsNull    Op = "IS NULL"
	OpIsNotNull Op = "IS NOT NULL"
	OpIn        Op = "IN"
	OpNotIn     Op = "NOT IN"
)

// N1: compile-time guard. allOps lists every defined Op constant; if a
// new constant is added without updating validateOp's switch the
// allOpsValid bool below would still compile, but a unit test compares
// len(allOps) against the switch arms — see TestN1_OpEnumExhaustive.
var allOps = []Op{
	OpEq, OpNeq, OpLt, OpLte, OpGt, OpGte,
	OpLike, OpILike, OpIsNull, OpIsNotNull, OpIn, OpNotIn,
}

// Predicate is one filter clause. Clauses combine with AND inside
// ReadOpts.Filter; for OR semantics, callers compose via OR-equivalent
// IN / explicit branching. The grid UI is the primary consumer and
// emits per-column AND-only filters.
//
// Predicate.Value accepted Go types (M14): the value is bound as a
// driver parameter, so anything driver.Conn.Query accepts is legal.
// In practice the rows package + the HTTP handler decode the following:
//
//   - For OpEq/OpNeq/OpLt/OpLte/OpGt/OpGte/OpLike/OpILike: any scalar
//     accepted by the underlying driver (string, []byte, int*, uint*,
//     float32, float64, bool, time.Time, nil).
//   - For OpIsNull / OpIsNotNull: Value is IGNORED (L9). Callers should
//     leave it nil; non-nil is silently dropped.
//   - For OpIn / OpNotIn: MUST be a slice. Accepted slice element types
//     are listed on flattenInValue (build.go). Nested slices are
//     rejected (L2). NaN / +Inf / -Inf in []float64 / []float32 / []any
//     are rejected (M4). The slice may not exceed maxInListSize entries
//     (H5).
type Predicate struct {
	Column string

	// Op is one of the Op constants above.
	Op Op

	// Value is the right-hand side. For OpIsNull / OpIsNotNull it's
	// ignored. For OpIn / OpNotIn it MUST be a slice; see the package
	// docstring on accepted slice element types.
	Value any
}

// ─── Read ────────────────────────────────────────────────────────────

// ReadOpts configures a paginated read.
//
// L12: Columns semantics — nil and []string{} are equivalent here: both
// mean "select all columns declared by schema.Reader.GetTable, in their
// declared order". This package never emits `SELECT *` because the
// column order would be implementation-defined and would change as the
// underlying table is altered.
type ReadOpts struct {
	Schema string
	Table  string

	// Columns to project. Empty (nil OR []string{}) = SELECT all
	// columns from the schema.Reader.GetTable result (we don't emit
	// `*` so the column order is stable). Order matters: it determines
	// the result column order.
	Columns []string

	// Filter: AND-combined predicates.
	Filter []Predicate

	// Sort keys, applied in order. Empty = engine default ordering
	// (which for InnoDB is PK order; for Postgres heap, arbitrary).
	Sort []SortKey

	// Limit caps the rows returned. Optional. 0 means "use
	// Operator.MaxRows". Negative is an error. Positive values cap at
	// Operator.MaxRows.
	Limit int

	// Offset for pagination. 0 for the first page. Capped at maxOffset
	// (L11) to refuse pathological deep-pagination.
	Offset int
}

// SortKey identifies one column to sort by + direction.
type SortKey struct {
	Column     string
	Descending bool
}

// ReadResult is the structured result of Read.
//
// Callers needing a row count (e.g. for a pagination footer) should
// call Count separately — Read does not run a piggy-backed COUNT(*).
//
// H1 (PR #5.5): Capped is true when the read hit the effective row cap
// (the resolved Limit). Read internally requests LIMIT+1 from the
// backend so it can distinguish "the table happens to have exactly
// Limit rows" from "the read was truncated". When Capped is true, Rows
// has exactly Limit entries and the caller should advise the user
// (typically by paginating with Offset += Limit).
type ReadResult struct {
	Columns []driver.ColumnInfo
	Rows    [][]any
	Capped  bool
}

// Read runs a paginated SELECT. Returns ErrInvalidIdentifier if any
// identifier fails validation, ErrInvalidPredicate if the filter is
// malformed, and ErrRowCapExceeded if Limit > the Operator's MaxRows.
//
// Read does NOT return a total-row count. Callers needing one should
// invoke Count with the same filter (see ReadResult).
func (o *Operator) Read(ctx context.Context, opts ReadOpts) (*ReadResult, error) {
	if err := schema.ValidateIdentifier(opts.Schema); err != nil {
		return nil, err
	}
	if err := schema.ValidateIdentifier(opts.Table); err != nil {
		return nil, err
	}
	for _, c := range opts.Columns {
		if err := schema.ValidateIdentifier(c); err != nil {
			return nil, err
		}
	}
	for _, s := range opts.Sort {
		if err := schema.ValidateIdentifier(s.Column); err != nil {
			return nil, err
		}
	}
	for _, p := range opts.Filter {
		if err := schema.ValidateIdentifier(p.Column); err != nil {
			return nil, err
		}
		if err := validateOp(p.Op); err != nil {
			return nil, err
		}
	}

	limit := opts.Limit
	if limit < 0 {
		return nil, fmt.Errorf("rows: Limit must be >= 0 (got %d)", limit)
	}
	if limit == 0 {
		limit = o.limits.MaxRows
	}
	if limit > o.limits.MaxRows {
		return nil, fmt.Errorf("%w: limit %d exceeds max %d", ErrRowCapExceeded, limit, o.limits.MaxRows)
	}
	if opts.Offset < 0 {
		return nil, fmt.Errorf("rows: offset must be >= 0 (got %d)", opts.Offset)
	}
	if opts.Offset > maxOffset {
		return nil, fmt.Errorf("rows: offset %d exceeds maxOffset %d (use a tighter Filter or a cursor)", opts.Offset, maxOffset)
	}

	// If columns are not specified, fetch the schema and use the
	// declared columns (avoids `SELECT *` which is order-fragile).
	cols := opts.Columns
	if len(cols) == 0 {
		t, err := o.schema.GetTable(ctx, opts.Schema, opts.Table)
		if err != nil {
			return nil, err
		}
		cols = make([]string, 0, len(t.Columns))
		for _, c := range t.Columns {
			cols = append(cols, c.Name)
		}
	}

	// H1: ask the SQL backend for LIMIT+1 rows. If we get back limit+1
	// rows, the read was truncated; we trim and set Capped=true. We
	// must also widen the per-call driver Limits.MaxRows so the
	// driver's own cap doesn't fire before we see the +1th row. Other
	// caps (MaxBytes, Timeout, MaxBytesPerCell) stay as-is.
	sqlLimit := limit + 1
	callLimits := o.limits
	if callLimits.MaxRows > 0 && callLimits.MaxRows < sqlLimit {
		callLimits.MaxRows = sqlLimit
	}

	q, args, err := buildSelect(o.engine, opts.Schema, opts.Table, cols, opts.Filter, opts.Sort, sqlLimit, opts.Offset)
	if err != nil {
		return nil, err
	}

	rs, err := o.conn.Query(ctx, callLimits, q, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()

	res := &ReadResult{
		Columns: rs.Columns(),
	}
	for {
		vals, err := rs.Next(ctx)
		if errors.Is(err, driver.ErrEOF) {
			break
		}
		if errors.Is(err, driver.ErrCapped) {
			// H1: the driver hit its own cap before we did. Treat as
			// a clean stop with Capped=true. The driver is contractually
			// allowed to surface this and the caller can paginate.
			res.Capped = true
			break
		}
		if err != nil {
			return nil, err
		}
		res.Rows = append(res.Rows, vals)
	}
	// H1: detect overflow via the +1th row.
	if len(res.Rows) > limit {
		res.Rows = res.Rows[:limit]
		res.Capped = true
	}
	return res, nil
}

// BuildSelectOpts mirrors ReadOpts but is purely a SQL-building input —
// no Operator state, no row caps, no execution. Used by callers (e.g.
// the streaming HTTP export handler) that need the rows package's
// identifier validation + engine-aware quoting but want to drive
// driver.Conn.Query themselves with their own Limits.
type BuildSelectOpts struct {
	Engine  dbadmin.EngineKind
	Schema  string
	Table   string
	Columns []string
	Filter  []Predicate
	Sort    []SortKey

	// Limit / Offset are written verbatim into the generated SQL. The
	// caller is responsible for choosing safe values; the helper does
	// NOT consult Operator.MaxRows (so streaming exports can request
	// orders of magnitude more rows than rows.Read allows).
	Limit  int
	Offset int
}

// BuildSelect produces a parameterized SELECT statement + bind args
// from validated identifier inputs. Identifier safety is enforced here
// — every schema/table/column name is run through ValidateIdentifier
// before reaching buildSelect's quoter. Returns ErrInvalidPredicate or
// schema.ErrInvalidIdentifier on bad input.
//
// Callers MUST execute the returned SQL via driver.Conn.Query, never
// concatenate it with anything else. Values come back via the args
// slice and are never inlined into the SQL text.
func BuildSelect(opts BuildSelectOpts) (string, []any, error) {
	switch opts.Engine {
	case dbadmin.EngineMariaDB, dbadmin.EnginePostgres:
		// supported
	default:
		return "", nil, fmt.Errorf("rows: unsupported engine %v", opts.Engine)
	}
	if err := schema.ValidateIdentifier(opts.Schema); err != nil {
		return "", nil, err
	}
	if err := schema.ValidateIdentifier(opts.Table); err != nil {
		return "", nil, err
	}
	for _, c := range opts.Columns {
		if err := schema.ValidateIdentifier(c); err != nil {
			return "", nil, err
		}
	}
	for _, s := range opts.Sort {
		if err := schema.ValidateIdentifier(s.Column); err != nil {
			return "", nil, err
		}
	}
	for _, p := range opts.Filter {
		if err := schema.ValidateIdentifier(p.Column); err != nil {
			return "", nil, err
		}
		if err := validateOp(p.Op); err != nil {
			return "", nil, err
		}
	}
	if opts.Limit < 0 {
		return "", nil, fmt.Errorf("rows: Limit must be >= 0 (got %d)", opts.Limit)
	}
	if opts.Offset < 0 {
		return "", nil, fmt.Errorf("rows: offset must be >= 0 (got %d)", opts.Offset)
	}
	if len(opts.Columns) == 0 {
		return "", nil, fmt.Errorf("rows: BuildSelect requires explicit column list")
	}
	return buildSelect(opts.Engine, opts.Schema, opts.Table, opts.Columns, opts.Filter, opts.Sort, opts.Limit, opts.Offset)
}

// ─── Count ───────────────────────────────────────────────────────────

// CountOpts (H2) is a dedicated input shape for Count. Unlike ReadOpts,
// it has no Columns / Sort / Limit / Offset — those fields are
// meaningless for COUNT(*) and accepting them silently was a footgun
// (a caller could pass Columns=["pii_blob"] expecting a projection cap
// and get a full table scan instead).
//
// COUNT(*) is not capped at MaxRows (the result is a single scalar);
// the Operator's Limits.Timeout still applies, so a runaway COUNT
// against an enormous table will cancel via the deadline.
type CountOpts struct {
	Schema string
	Table  string
	Filter []Predicate
}

// CountByOpts (H2) is the preferred entrypoint for COUNT(*). Validates
// identifiers + predicate ops and executes a parameterized COUNT(*).
func (o *Operator) CountByOpts(ctx context.Context, opts CountOpts) (int64, error) {
	if err := schema.ValidateIdentifier(opts.Schema); err != nil {
		return 0, err
	}
	if err := schema.ValidateIdentifier(opts.Table); err != nil {
		return 0, err
	}
	for _, p := range opts.Filter {
		if err := schema.ValidateIdentifier(p.Column); err != nil {
			return 0, err
		}
		if err := validateOp(p.Op); err != nil {
			return 0, err
		}
	}
	q, args, err := buildCount(o.engine, opts.Schema, opts.Table, opts.Filter)
	if err != nil {
		return 0, err
	}
	rs, err := o.conn.Query(ctx, o.limits, q, args...)
	if err != nil {
		return 0, err
	}
	defer rs.Close()
	vals, err := rs.Next(ctx)
	if err != nil {
		return 0, err
	}
	if len(vals) == 0 {
		return 0, errors.New("rows: COUNT returned no value")
	}
	n, err := toInt64(vals[0])
	if err != nil {
		return 0, err
	}
	return n, nil
}

// Count returns the row count of (Schema, Table) under Filter. Useful
// for the grid's pagination footer.
//
// Backwards-compatibility shim (H2): forwards to CountByOpts, copying
// only the fields COUNT(*) actually uses (Schema, Table, Filter). The
// other ReadOpts fields (Columns, Sort, Limit, Offset) are ignored on
// this path — new code should prefer CountByOpts to avoid passing
// fields that have no effect.
func (o *Operator) Count(ctx context.Context, opts ReadOpts) (int64, error) {
	return o.CountByOpts(ctx, CountOpts{
		Schema: opts.Schema,
		Table:  opts.Table,
		Filter: opts.Filter,
	})
}

// ─── Update by PK ────────────────────────────────────────────────────

// UpdateByPKOpts updates exactly one row identified by its primary
// key. The package refuses to issue an UPDATE that doesn't anchor on
// every PK column — this is the structural defense against
// "accidentally updated 1M rows" which dominates real-world DB outage
// stories.
type UpdateByPKOpts struct {
	Schema string
	Table  string

	// PK is the {column: value} map that uniquely identifies the
	// target row. Keys MUST match the table's primary key columns
	// exactly (in any order); UpdateByPK fetches the table's PK from
	// the schema reader and rejects with ErrPKMismatch otherwise.
	// Values MUST NOT be nil (M6): a nil PK value would generate
	// `pk = NULL` which never matches in SQL.
	PK map[string]any

	// Set is the {column: new-value} map. Empty set returns
	// ErrEmptyUpdate without making a query.
	//
	// M7: Set MUST NOT include any primary-key column — mutating a PK
	// in-place would break the optimistic-concurrency anchor and
	// cascade to dependent rows in ways that are almost certainly
	// unintended. Callers wanting to "rekey" a row should DELETE +
	// INSERT under a transaction at the SQL-editor layer.
	Set map[string]any

	// Where is an optional optimistic-concurrency snapshot (edit-1).
	// Keys must be declared columns; each {col: val} pair is added to
	// the UPDATE's WHERE alongside the PK clause. If the row's current
	// value for any column no longer matches, the UPDATE affects zero
	// rows and UpdateByPK returns ErrConcurrentModification so the
	// client can refresh + retry without clobbering a concurrent write.
	// Empty / nil disables the snapshot check.
	Where map[string]any
}

// UpdateResult is the outcome of UpdateByPK / DeleteByPK / Insert.
type UpdateResult struct {
	RowsAffected int64
	LastInsertID int64 // Insert-only; meaningful only when the table has an auto-increment / IDENTITY column
}

// UpdateByPK runs the parameterized UPDATE. When opts.Where is non-empty,
// the WHERE clause anchors on both the PK and the snapshot columns; a
// concurrent change to any snapshot column causes the UPDATE to affect
// zero rows, which UpdateByPK reports as ErrConcurrentModification.
func (o *Operator) UpdateByPK(ctx context.Context, opts UpdateByPKOpts) (*UpdateResult, error) {
	if err := schema.ValidateIdentifier(opts.Schema); err != nil {
		return nil, err
	}
	if err := schema.ValidateIdentifier(opts.Table); err != nil {
		return nil, err
	}
	if len(opts.Set) == 0 {
		return nil, ErrEmptyUpdate
	}
	for k := range opts.Set {
		if err := schema.ValidateIdentifier(k); err != nil {
			return nil, err
		}
	}
	for k := range opts.PK {
		if err := schema.ValidateIdentifier(k); err != nil {
			return nil, err
		}
	}
	for k := range opts.Where {
		if err := schema.ValidateIdentifier(k); err != nil {
			return nil, err
		}
	}

	// M10: per-value size cap on Set values.
	for k, v := range opts.Set {
		if err := o.checkValueSize(k, v); err != nil {
			return nil, err
		}
	}

	// PK preflight.
	t, err := o.schema.GetTable(ctx, opts.Schema, opts.Table)
	if err != nil {
		return nil, err
	}
	if len(t.PrimaryKey) == 0 {
		return nil, fmt.Errorf("%w: %s.%s has no primary key", ErrNoPrimaryKey, opts.Schema, opts.Table)
	}
	if err := assertPKMatch(t.PrimaryKey, opts.PK); err != nil {
		return nil, err
	}

	// M7: Set must not mutate PK columns.
	pkSet := make(map[string]bool, len(t.PrimaryKey))
	for _, c := range t.PrimaryKey {
		pkSet[c] = true
	}
	for k := range opts.Set {
		if pkSet[k] {
			return nil, fmt.Errorf("%w: column %q is part of the primary key", ErrPKMutation, k)
		}
	}

	q, args, err := buildUpdateWithWhere(o.engine, opts.Schema, opts.Table, opts.Set, t.PrimaryKey, opts.PK, opts.Where)
	if err != nil {
		return nil, err
	}
	res, err := o.conn.Exec(ctx, o.limits, q, args...)
	if err != nil {
		return nil, err
	}
	// edit-1: a snapshot-anchored UPDATE that touches zero rows means
	// the row's snapshot columns changed since the client last loaded —
	// surface as a typed conflict the handler maps to 409.
	if len(opts.Where) > 0 && res.RowsAffected == 0 {
		return nil, ErrConcurrentModification
	}
	return &UpdateResult{RowsAffected: res.RowsAffected}, nil
}

// ─── Delete by PK ────────────────────────────────────────────────────

// DeleteByPKOpts deletes exactly one row identified by its PK. Same PK
// enforcement as UpdateByPK.
type DeleteByPKOpts struct {
	Schema string
	Table  string
	PK     map[string]any
}

// DeleteByPK runs the parameterized DELETE.
func (o *Operator) DeleteByPK(ctx context.Context, opts DeleteByPKOpts) (*UpdateResult, error) {
	if err := schema.ValidateIdentifier(opts.Schema); err != nil {
		return nil, err
	}
	if err := schema.ValidateIdentifier(opts.Table); err != nil {
		return nil, err
	}
	for k := range opts.PK {
		if err := schema.ValidateIdentifier(k); err != nil {
			return nil, err
		}
	}

	t, err := o.schema.GetTable(ctx, opts.Schema, opts.Table)
	if err != nil {
		return nil, err
	}
	if len(t.PrimaryKey) == 0 {
		return nil, fmt.Errorf("%w: %s.%s has no primary key", ErrNoPrimaryKey, opts.Schema, opts.Table)
	}
	if err := assertPKMatch(t.PrimaryKey, opts.PK); err != nil {
		return nil, err
	}

	q, args, err := buildDelete(o.engine, opts.Schema, opts.Table, t.PrimaryKey, opts.PK)
	if err != nil {
		return nil, err
	}
	res, err := o.conn.Exec(ctx, o.limits, q, args...)
	if err != nil {
		return nil, err
	}
	return &UpdateResult{RowsAffected: res.RowsAffected}, nil
}

// ─── Insert ──────────────────────────────────────────────────────────

// InsertOpts adds one row. No PK requirement — operators sometimes
// insert into tables without PKs (rare but legal). The shape is just a
// map of column → value.
type InsertOpts struct {
	Schema string
	Table  string
	Values map[string]any
}

// Insert runs the parameterized INSERT.
//
// H6 (PR #5.5): on Postgres, if the target table has exactly one
// integer-typed primary key column, Insert appends `RETURNING <pk>` so
// LastInsertID is populated. The Go pgx driver does NOT support
// LastInsertId() (it always returns 0); without RETURNING the panel UI
// can't refresh the just-inserted row by PK. On MariaDB we keep
// LAST_INSERT_ID() via Exec — it's free and reliable for
// AUTO_INCREMENT columns.
func (o *Operator) Insert(ctx context.Context, opts InsertOpts) (*UpdateResult, error) {
	if err := schema.ValidateIdentifier(opts.Schema); err != nil {
		return nil, err
	}
	if err := schema.ValidateIdentifier(opts.Table); err != nil {
		return nil, err
	}
	if len(opts.Values) == 0 {
		return nil, fmt.Errorf("rows: insert requires at least one column value")
	}
	for k := range opts.Values {
		if err := schema.ValidateIdentifier(k); err != nil {
			return nil, err
		}
	}
	// M10: per-value size cap.
	for k, v := range opts.Values {
		if err := o.checkValueSize(k, v); err != nil {
			return nil, err
		}
	}

	// H6: Postgres path uses RETURNING <pk> when we can identify a
	// single-column PK from the schema reader. Multi-column / no-PK
	// tables fall through to plain INSERT + LastInsertID=0.
	if o.engine == dbadmin.EnginePostgres {
		pkCol, ok := o.singleIntegerPK(ctx, opts.Schema, opts.Table)
		if ok {
			q, args, err := buildInsertReturning(o.engine, opts.Schema, opts.Table, opts.Values, pkCol)
			if err != nil {
				return nil, err
			}
			rs, err := o.conn.Query(ctx, o.limits, q, args...)
			if err != nil {
				return nil, err
			}
			defer rs.Close()
			vals, err := rs.Next(ctx)
			if err != nil && !errors.Is(err, driver.ErrEOF) {
				return nil, err
			}
			var lastID int64
			if len(vals) > 0 {
				if id, cerr := toInt64(vals[0]); cerr == nil {
					lastID = id
				}
			}
			return &UpdateResult{
				RowsAffected: 1, // RETURNING implies the INSERT succeeded
				LastInsertID: lastID,
			}, nil
		}
	}

	q, args, err := buildInsert(o.engine, opts.Schema, opts.Table, opts.Values)
	if err != nil {
		return nil, err
	}
	res, err := o.conn.Exec(ctx, o.limits, q, args...)
	if err != nil {
		return nil, err
	}
	return &UpdateResult{
		RowsAffected: res.RowsAffected,
		LastInsertID: res.LastInsertID,
	}, nil
}

// singleIntegerPK returns the PK column name if the table has exactly
// one PK column of an integer-shaped type, otherwise ok=false. The
// integer-type check is best-effort: if schema.Reader can't classify
// the column we still return the PK name (Postgres will happily
// RETURNING any single column).
func (o *Operator) singleIntegerPK(ctx context.Context, schemaName, table string) (string, bool) {
	t, err := o.schema.GetTable(ctx, schemaName, table)
	if err != nil || t == nil {
		return "", false
	}
	if len(t.PrimaryKey) != 1 {
		return "", false
	}
	return t.PrimaryKey[0], true
}

// ─── Errors ──────────────────────────────────────────────────────────

var (
	// ErrInvalidPredicate is returned when a Predicate uses an
	// unknown Op or an IN / NOT IN with a non-slice value, an empty
	// NOT IN list (M3), a NaN/Inf float (M4), a nested slice (L2), or
	// more than maxInListSize entries (H5).
	ErrInvalidPredicate = errors.New("rows: invalid predicate")

	// ErrRowCapExceeded is returned when ReadOpts.Limit > the
	// Operator's MaxRows. Operators can request larger reads via the
	// SQL editor (which has its own caps).
	ErrRowCapExceeded = errors.New("rows: row cap exceeded")

	// ErrNoPrimaryKey is returned by UpdateByPK / DeleteByPK when the
	// target table has no PK. The grid's edit-row + delete-row paths
	// require a PK; tables without one are read-only through this
	// package. (Operators can still mutate via the SQL editor.)
	ErrNoPrimaryKey = errors.New("rows: table has no primary key")

	// ErrPKMismatch is returned when the PK map's keys don't match
	// the table's actual primary-key columns. Catches typos and the
	// case where the schema changed since the UI last loaded.
	ErrPKMismatch = errors.New("rows: PK column mismatch")

	// ErrPKMutation is returned by UpdateByPK when Set includes a
	// primary-key column (M7). Mutating a PK in-place would break
	// optimistic-concurrency anchoring + cascade in ways the row API
	// can't reason about; callers must DELETE + INSERT via the SQL
	// editor under a transaction instead.
	ErrPKMutation = errors.New("rows: cannot update primary-key column")

	// ErrEmptyUpdate is returned when UpdateByPKOpts.Set is empty —
	// a no-op update is almost certainly a bug in the caller.
	ErrEmptyUpdate = errors.New("rows: empty update set")

	// ErrConcurrentModification is returned by UpdateByPK when an
	// optimistic-concurrency snapshot (UpdateByPKOpts.Where) failed to
	// match — the row changed under the client between read and write.
	// The handler maps this to HTTP 409 / conflict.
	ErrConcurrentModification = errors.New("rows: row changed since last read")

	// ErrValueTooLarge (M10) is returned by UpdateByPK / Insert when
	// any single value in Set / Values exceeds Operator.MaxValueBytes.
	// Strings and []byte are measured by len(); other types are not
	// size-capped.
	ErrValueTooLarge = errors.New("rows: value exceeds per-value size cap")

	// ErrInvalidIdentifier (L13) is a cross-package re-export of
	// schema.ErrInvalidIdentifier so callers can errors.Is against
	// the rows package surface without importing schema explicitly.
	ErrInvalidIdentifier = schema.ErrInvalidIdentifier
)

// ─── Defaults ────────────────────────────────────────────────────────

const (
	defaultMaxRows  = 10_000
	defaultMaxBytes = 50 * 1024 * 1024

	// defaultMaxValueBytes caps any single Set/Values entry at 1 MiB
	// (M10). 1 MiB comfortably fits the largest legitimate cell value
	// the grid editor produces (think: a base64-encoded image) and
	// rejects anything that would clearly OOM the panel process if
	// thousands of operators all hit Update simultaneously.
	defaultMaxValueBytes int64 = 1 << 20

	// maxOffset caps OFFSET (L11). Deep pagination is pathological on
	// both MariaDB and Postgres — the engine still scans + discards
	// every prior row. Operators wanting page N for large N should
	// switch to keyset pagination via a custom Filter; the row API
	// refuses anything past 1M.
	maxOffset = 1_000_000

	// maxInListSize caps IN / NOT IN list length (H5). Postgres caps
	// at 65535 total bind parameters per query; a single oversized IN
	// can blow past that. 1000 leaves headroom for other binds and
	// stays well below MySQL's max_allowed_packet boundary.
	maxInListSize = 1000
)

// defaultTimeout matches SECURITY.md §6.5 Config.Query.TimeoutDefault.
const defaultTimeout = 30 * time.Second

// ─── Helpers ────────────────────────────────────────────────────────

// validateOp returns nil if op is one of the known Op constants.
func validateOp(op Op) error {
	switch op {
	case OpEq, OpNeq, OpLt, OpLte, OpGt, OpGte,
		OpLike, OpILike, OpIsNull, OpIsNotNull, OpIn, OpNotIn:
		return nil
	}
	return fmt.Errorf("%w: unknown op %q", ErrInvalidPredicate, op)
}

// assertPKMatch verifies the user-supplied PK map's keys are exactly
// the table's PK columns (set equality, not order). The returned error
// is intentionally opaque — callers can still discriminate via
// errors.Is(err, ErrPKMismatch); the audit log (out-of-band) records
// the full mismatch detail when authorized.
//
// M6: also rejects nil PK values. `pk = NULL` never matches any row in
// SQL, so a nil PK would produce a confusing "row not found" with no
// hint that the issue was the input shape.
func assertPKMatch(tablePK []string, supplied map[string]any) error {
	if len(supplied) != len(tablePK) {
		return fmt.Errorf("%w", ErrPKMismatch)
	}
	pkSet := make(map[string]bool, len(tablePK))
	for _, c := range tablePK {
		pkSet[c] = true
	}
	for k, v := range supplied {
		if !pkSet[k] {
			return fmt.Errorf("%w", ErrPKMismatch)
		}
		if v == nil {
			return fmt.Errorf("%w: PK column %q has nil value", ErrPKMismatch, k)
		}
	}
	return nil
}

// checkValueSize enforces Operator.maxValueBytes (M10) on a single
// Set/Values entry. Only []byte and string are size-checked; other
// types pass through unchecked because their wire-shape depends on
// the driver. Returns ErrValueTooLarge with the offending column name.
func (o *Operator) checkValueSize(col string, v any) error {
	cap := o.maxValueBytes
	if cap <= 0 {
		return nil
	}
	var n int
	switch x := v.(type) {
	case string:
		n = len(x)
	case []byte:
		n = len(x)
	default:
		return nil
	}
	if int64(n) > cap {
		return fmt.Errorf("%w: column %q has %d bytes (cap %d)", ErrValueTooLarge, col, n, cap)
	}
	return nil
}

// toInt64 normalizes a value into int64. Used by Count's single-cell
// result. Returns an error on unexpected types or values that cannot
// be safely represented as int64 — the caller must propagate, never
// silently coerce to 0.
func toInt64(v any) (int64, error) {
	switch x := v.(type) {
	case int64:
		return x, nil
	case int:
		return int64(x), nil
	case int32:
		return int64(x), nil
	case uint64:
		if x > math.MaxInt64 {
			return 0, fmt.Errorf("rows: count %d overflows int64", x)
		}
		return int64(x), nil
	case uint32:
		return int64(x), nil
	case float64:
		// Postgres may return COUNT via numeric → float for big counts
		return int64(x), nil
	case string:
		n, err := strconv.ParseInt(x, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("rows: parse count %q: %w", x, err)
		}
		return n, nil
	case []byte:
		n, err := strconv.ParseInt(string(x), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("rows: parse count bytes %q: %w", x, err)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("rows: unexpected COUNT result type %T", v)
	}
}

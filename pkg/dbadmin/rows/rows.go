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
type Operator struct {
	conn   driver.Conn
	schema schema.Reader
	engine dbadmin.EngineKind
	limits driver.Limits
}

// Options configure a new Operator.
type Options struct {
	// Limits applies to every query the operator issues. The engine
	// passes Config.Query-derived limits here. Zero value gets a
	// conservative default (10s timeout, 10K rows, 50MB).
	Limits driver.Limits
}

// New constructs an Operator. conn and reader are required; the engine
// is derived from reader.Engine() so the quoting strategy is always
// consistent with the schema metadata source.
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
	if l.Timeout == 0 {
		l.Timeout = defaultTimeout
	}
	if l.MaxRows == 0 {
		l.MaxRows = defaultMaxRows
	}
	if l.MaxBytes == 0 {
		l.MaxBytes = defaultMaxBytes
	}
	return &Operator{
		conn:   conn,
		schema: reader,
		engine: engine,
		limits: l,
	}, nil
}

// ─── Predicate model ─────────────────────────────────────────────────

// Op enumerates the operators a Predicate may use. Operator strings are
// constants here so an attacker can't smuggle in ";" or other SQL via
// the operator field.
type Op string

const (
	OpEq         Op = "="
	OpNeq        Op = "!="
	OpLt         Op = "<"
	OpLte        Op = "<="
	OpGt         Op = ">"
	OpGte        Op = ">="
	OpLike       Op = "LIKE"
	OpILike      Op = "ILIKE" // case-insensitive; rewritten to LOWER(...)LIKE LOWER(...) for MySQL
	OpIsNull     Op = "IS NULL"
	OpIsNotNull  Op = "IS NOT NULL"
	OpIn         Op = "IN"
	OpNotIn      Op = "NOT IN"
)

// Predicate is one filter clause. Clauses combine with AND inside
// ReadOpts.Filter; for OR semantics, callers compose via OR-equivalent
// IN / explicit branching. The grid UI is the primary consumer and
// emits per-column AND-only filters.
type Predicate struct {
	Column string

	// Op is one of the Op constants above.
	Op Op

	// Value is the right-hand side. For OpIsNull / OpIsNotNull it's
	// ignored. For OpIn / OpNotIn it MUST be a slice ([]any,
	// []string, []int64) — non-slice rejects with ErrInvalidPredicate.
	Value any
}

// ─── Read ────────────────────────────────────────────────────────────

// ReadOpts configures a paginated read.
type ReadOpts struct {
	Schema string
	Table  string

	// Columns to project. Empty = SELECT all columns from the
	// schema.GetTable result (we don't emit `*` so the column order
	// is stable). Order matters: it determines the result column
	// order.
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

	// Offset for pagination. 0 for the first page.
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
type ReadResult struct {
	Columns []driver.ColumnInfo
	Rows    [][]any
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

	q, args, err := buildSelect(o.engine, opts.Schema, opts.Table, cols, opts.Filter, opts.Sort, limit, opts.Offset)
	if err != nil {
		return nil, err
	}

	rs, err := o.conn.Query(ctx, o.limits, q, args...)
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
		if err != nil {
			return nil, err
		}
		res.Rows = append(res.Rows, vals)
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

// Count returns the row count of (Schema, Table) under Filter. Useful
// for the grid's pagination footer.
func (o *Operator) Count(ctx context.Context, opts ReadOpts) (int64, error) {
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
	PK map[string]any

	// Set is the {column: new-value} map. Empty set returns
	// ErrEmptyUpdate without making a query.
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

// ─── Errors ──────────────────────────────────────────────────────────

var (
	// ErrInvalidPredicate is returned when a Predicate uses an
	// unknown Op or an IN / NOT IN with a non-slice value.
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

	// ErrEmptyUpdate is returned when UpdateByPKOpts.Set is empty —
	// a no-op update is almost certainly a bug in the caller.
	ErrEmptyUpdate = errors.New("rows: empty update set")

	// ErrConcurrentModification is returned by UpdateByPK when an
	// optimistic-concurrency snapshot (UpdateByPKOpts.Where) failed to
	// match — the row changed under the client between read and write.
	// The handler maps this to HTTP 409 / conflict.
	ErrConcurrentModification = errors.New("rows: row changed since last read")
)

// ─── Defaults ────────────────────────────────────────────────────────

const (
	defaultMaxRows  = 10_000
	defaultMaxBytes = 50 * 1024 * 1024
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
func assertPKMatch(tablePK []string, supplied map[string]any) error {
	if len(supplied) != len(tablePK) {
		return fmt.Errorf("%w", ErrPKMismatch)
	}
	pkSet := make(map[string]bool, len(tablePK))
	for _, c := range tablePK {
		pkSet[c] = true
	}
	for k := range supplied {
		if !pkSet[k] {
			return fmt.Errorf("%w", ErrPKMismatch)
		}
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

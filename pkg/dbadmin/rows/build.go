package rows

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// This file builds the actual SQL statements. Identifiers are quoted
// engine-aware (backtick for MySQL, double-quote for Postgres) AFTER
// being validated by schema.ValidateIdentifier in rows.go. Values are
// always passed as bind parameters; we NEVER stringify them into the
// SQL text.

// quoteIdent escapes an already-validated identifier. Since
// ValidateIdentifier ensures the name matches [a-zA-Z_][a-zA-Z0-9_$]{0,62},
// the only character that needs escaping per engine is the surrounding
// quote character itself. The validator's allowlist guarantees no
// quote character can appear; this function only needs to add the
// boundary.
//
// L8: the default branch is unreachable in practice because every
// public entrypoint validates the engine. Returning the bare name
// rather than panicking keeps a regression from corrupting SQL into
// a syntactically valid-but-wrong query; the caller will see whatever
// runtime error the unquoted name triggers (parse error / reserved
// word collision), which is loud and recoverable.
func quoteIdent(name string, engine dbadmin.EngineKind) string {
	switch engine {
	case dbadmin.EngineMariaDB:
		return "`" + name + "`"
	case dbadmin.EnginePostgres:
		return `"` + name + `"`
	default:
		// Unreachable in practice (callers validate engine).
		return name
	}
}

// qualifyTable returns the engine-correct "schema"."table" form.
func qualifyTable(schema, table string, engine dbadmin.EngineKind) string {
	return quoteIdent(schema, engine) + "." + quoteIdent(table, engine)
}

// placeholder returns the engine-specific bind-parameter token for the
// nth (1-based) parameter.
//   - MySQL/MariaDB: always "?"
//   - PostgreSQL: "$1", "$2", ...
func placeholder(n int, engine dbadmin.EngineKind) string {
	if engine == dbadmin.EnginePostgres {
		return fmt.Sprintf("$%d", n)
	}
	return "?"
}

// L1: opSQL maps simple binary ops to their canonical SQL token.
// Centralises the table so future engine-specific overrides (e.g.
// rendering NEQ as `<>` on some legacy DBs) land in one place.
var opSQL = map[Op]string{
	OpEq:  "=",
	OpNeq: "!=",
	OpLt:  "<",
	OpLte: "<=",
	OpGt:  ">",
	OpGte: ">=",
	OpLike: "LIKE",
}

// ─── SELECT ──────────────────────────────────────────────────────────

func buildSelect(
	engine dbadmin.EngineKind,
	schemaName, table string,
	cols []string,
	filter []Predicate,
	sortKeys []SortKey,
	limit, offset int,
) (string, []any, error) {
	var b strings.Builder
	args := make([]any, 0, len(filter)+2)

	b.WriteString("SELECT ")
	if len(cols) == 0 {
		// Should never happen; rows.go.Read populates from schema.GetTable
		// when caller passes empty. Defensive: refuse SELECT * which
		// would have unstable column order.
		return "", nil, fmt.Errorf("rows/build: SELECT requires explicit column list")
	}
	for i, c := range cols {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(quoteIdent(c, engine))
	}
	b.WriteString(" FROM ")
	b.WriteString(qualifyTable(schemaName, table, engine))

	whereClause, whereArgs, err := buildWhere(engine, filter, 1)
	if err != nil {
		return "", nil, err
	}
	if whereClause != "" {
		b.WriteString(" WHERE ")
		b.WriteString(whereClause)
		args = append(args, whereArgs...)
	}

	if len(sortKeys) > 0 {
		b.WriteString(" ORDER BY ")
		for i, sk := range sortKeys {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(quoteIdent(sk.Column, engine))
			if sk.Descending {
				b.WriteString(" DESC")
			} else {
				b.WriteString(" ASC")
			}
		}
	}

	// LIMIT + OFFSET. Both engines accept this form; pgx maps to
	// the same parse tree. Values are operator-supplied INTs validated
	// up-front (>= 0 + cap); Go's int type cannot encode a `;` or
	// quote character so inlining is safe — auditors should still
	// read this branch knowing the upstream validators in rows.go +
	// BuildSelect run first.
	b.WriteString(fmt.Sprintf(" LIMIT %d", limit))
	if offset > 0 {
		b.WriteString(fmt.Sprintf(" OFFSET %d", offset))
	}

	return b.String(), args, nil
}

// buildCount produces a parameterized COUNT(*) over the same filter as
// buildSelect.
func buildCount(
	engine dbadmin.EngineKind,
	schemaName, table string,
	filter []Predicate,
) (string, []any, error) {
	var b strings.Builder
	b.WriteString("SELECT COUNT(*) FROM ")
	b.WriteString(qualifyTable(schemaName, table, engine))

	whereClause, args, err := buildWhere(engine, filter, 1)
	if err != nil {
		return "", nil, err
	}
	if whereClause != "" {
		b.WriteString(" WHERE ")
		b.WriteString(whereClause)
	}
	return b.String(), args, nil
}

// ─── WHERE ───────────────────────────────────────────────────────────

// buildWhere returns the WHERE clause body (without the leading
// "WHERE ") and the args. startIdx is the 1-based parameter index to
// start from — UPDATE's WHERE comes AFTER the SET parameters, so the
// caller passes the right starting offset.
func buildWhere(engine dbadmin.EngineKind, filter []Predicate, startIdx int) (string, []any, error) {
	if len(filter) == 0 {
		return "", nil, nil
	}

	var parts []string
	args := make([]any, 0, len(filter))
	idx := startIdx

	for _, p := range filter {
		clause, ps, err := buildPredicate(engine, p, idx)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, clause)
		args = append(args, ps...)
		idx += len(ps)
	}

	return strings.Join(parts, " AND "), args, nil
}

// buildPredicate produces one filter clause + its args.
func buildPredicate(engine dbadmin.EngineKind, p Predicate, idx int) (string, []any, error) {
	col := quoteIdent(p.Column, engine)
	switch p.Op {
	case OpEq, OpNeq, OpLt, OpLte, OpGt, OpGte, OpLike:
		return fmt.Sprintf("%s %s %s", col, opSQL[p.Op], placeholder(idx, engine)),
			[]any{p.Value}, nil

	case OpILike:
		if engine == dbadmin.EnginePostgres {
			return fmt.Sprintf("%s ILIKE %s", col, placeholder(idx, engine)),
				[]any{p.Value}, nil
		}
		// MySQL has no ILIKE; rewrite to LOWER(col) LIKE LOWER(?).
		// L3: this rewrite is correct for ASCII but locale-dependent
		// for multibyte. See Op docstring.
		return fmt.Sprintf("LOWER(%s) LIKE LOWER(%s)", col, placeholder(idx, engine)),
			[]any{p.Value}, nil

	case OpIsNull:
		// L9: Value is ignored on IS NULL / IS NOT NULL.
		return fmt.Sprintf("%s IS NULL", col), nil, nil
	case OpIsNotNull:
		return fmt.Sprintf("%s IS NOT NULL", col), nil, nil

	case OpIn, OpNotIn:
		values, err := flattenInValue(p.Value)
		if err != nil {
			return "", nil, fmt.Errorf("%w: %v", ErrInvalidPredicate, err)
		}
		// H5: cap the IN list to keep us safely below Postgres's
		// 65535-bind-parameter ceiling and MySQL's max_allowed_packet.
		if len(values) > maxInListSize {
			return "", nil, fmt.Errorf("%w: IN list has %d entries (max %d)",
				ErrInvalidPredicate, len(values), maxInListSize)
		}
		if len(values) == 0 {
			// M3: empty NOT IN previously emitted 1=1, which silently
			// turns a blocklist filter into "match everything" — a
			// dangerous footgun if the operator's intent was to
			// exclude. Reject explicitly; callers can branch upstream
			// and omit the predicate when the slice is empty.
			if p.Op == OpNotIn {
				return "", nil, fmt.Errorf("%w: NOT IN with empty list (would match every row)",
					ErrInvalidPredicate)
			}
			// Empty IN: emit literal FALSE. The bind list is empty by
			// design; this still keeps the WHERE arity stable.
			return "1=0", nil, nil
		}
		// Build (?, ?, ?) or ($1, $2, $3).
		ph := make([]string, len(values))
		for i := range values {
			ph[i] = placeholder(idx+i, engine)
		}
		op := "IN"
		if p.Op == OpNotIn {
			op = "NOT IN"
		}
		return fmt.Sprintf("%s %s (%s)", col, op, strings.Join(ph, ", ")), values, nil
	}
	return "", nil, fmt.Errorf("%w: unknown op %q", ErrInvalidPredicate, p.Op)
}

// flattenInValue accepts a slice of one of the supported element types
// and returns []any for placeholder binding. Returns an error for nil,
// non-slice, nested slice (L2), or NaN/Inf inside []float64/[]float32/
// []any (M4).
//
// Supported element types (M5):
//
//	[]any, []string, []bool,
//	[]int, []int8, []int16, []int32, []int64,
//	[]uint, []uint8 (== []byte, NOT supported — ambiguous, rejected),
//	[]uint16, []uint32, []uint64,
//	[]float32, []float64,
//	[]time.Time
//
// []byte is intentionally rejected: it is the wire shape of a single
// blob value, not an IN list — accepting it would silently turn one
// blob into N single-byte predicates.
func flattenInValue(v any) ([]any, error) {
	switch x := v.(type) {
	case nil:
		return nil, fmt.Errorf("IN value is nil")

	case []any:
		out := make([]any, len(x))
		for i, el := range x {
			if err := rejectNonScalar(el); err != nil {
				return nil, err
			}
			out[i] = el
		}
		return out, nil

	case []string:
		out := make([]any, len(x))
		for i, s := range x {
			out[i] = s
		}
		return out, nil

	case []bool:
		out := make([]any, len(x))
		for i, b := range x {
			out[i] = b
		}
		return out, nil

	case []int:
		out := make([]any, len(x))
		for i, n := range x {
			out[i] = n
		}
		return out, nil
	case []int8:
		out := make([]any, len(x))
		for i, n := range x {
			out[i] = n
		}
		return out, nil
	case []int16:
		out := make([]any, len(x))
		for i, n := range x {
			out[i] = n
		}
		return out, nil
	case []int32:
		out := make([]any, len(x))
		for i, n := range x {
			out[i] = n
		}
		return out, nil
	case []int64:
		out := make([]any, len(x))
		for i, n := range x {
			out[i] = n
		}
		return out, nil

	case []uint:
		out := make([]any, len(x))
		for i, n := range x {
			out[i] = n
		}
		return out, nil
	case []uint16:
		out := make([]any, len(x))
		for i, n := range x {
			out[i] = n
		}
		return out, nil
	case []uint32:
		out := make([]any, len(x))
		for i, n := range x {
			out[i] = n
		}
		return out, nil
	case []uint64:
		out := make([]any, len(x))
		for i, n := range x {
			out[i] = n
		}
		return out, nil

	case []float32:
		out := make([]any, len(x))
		for i, n := range x {
			if math.IsNaN(float64(n)) || math.IsInf(float64(n), 0) {
				return nil, fmt.Errorf("IN value contains non-finite float32 at index %d", i)
			}
			out[i] = n
		}
		return out, nil
	case []float64:
		out := make([]any, len(x))
		for i, n := range x {
			if math.IsNaN(n) || math.IsInf(n, 0) {
				return nil, fmt.Errorf("IN value contains non-finite float64 at index %d", i)
			}
			out[i] = n
		}
		return out, nil

	case []time.Time:
		out := make([]any, len(x))
		for i, t := range x {
			out[i] = t
		}
		return out, nil

	case []byte:
		// Explicit reject (L2 / M5 corner case). []byte is the wire
		// shape of a single blob value, not an IN list of bytes.
		return nil, fmt.Errorf("IN value of type []byte is ambiguous; pass [][]byte or wrap in []any")

	default:
		return nil, fmt.Errorf("IN value must be a slice, got %T", v)
	}
}

// rejectNonScalar inspects one element of a []any IN list and returns
// an error if it's a nested slice (L2) or a non-finite float (M4).
// Other types are accepted on the assumption the driver can bind them.
func rejectNonScalar(el any) error {
	switch x := el.(type) {
	case nil:
		return nil
	case []any, []string, []bool,
		[]int, []int8, []int16, []int32, []int64,
		[]uint, []uint16, []uint32, []uint64,
		[]float32, []float64, []time.Time:
		return fmt.Errorf("IN value element is a nested slice (%T); flatten before passing", el)
	case float32:
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return fmt.Errorf("IN value element is non-finite float32")
		}
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return fmt.Errorf("IN value element is non-finite float64")
		}
	}
	return nil
}

// ─── UPDATE ──────────────────────────────────────────────────────────

func buildUpdate(
	engine dbadmin.EngineKind,
	schemaName, table string,
	set map[string]any,
	pkCols []string,
	pkValues map[string]any,
) (string, []any, error) {
	return buildUpdateWithWhere(engine, schemaName, table, set, pkCols, pkValues, nil)
}

// buildUpdateWithWhere extends buildUpdate with optional
// optimistic-concurrency snapshot columns (edit-1). When `where` is
// non-empty, each {col: val} pair is added to the WHERE clause alongside
// the PK; the SQL still hits at most one row because the PK is unique.
func buildUpdateWithWhere(
	engine dbadmin.EngineKind,
	schemaName, table string,
	set map[string]any,
	pkCols []string,
	pkValues map[string]any,
	where map[string]any,
) (string, []any, error) {
	if len(set) == 0 {
		return "", nil, ErrEmptyUpdate
	}

	// Deterministic column ordering for SET — makes generated SQL
	// stable across runs (useful for testing + audit-log diffs).
	setKeys := make([]string, 0, len(set))
	for k := range set {
		setKeys = append(setKeys, k)
	}
	sort.Strings(setKeys)

	var b strings.Builder
	b.WriteString("UPDATE ")
	b.WriteString(qualifyTable(schemaName, table, engine))
	b.WriteString(" SET ")

	args := make([]any, 0, len(set)+len(pkValues)+len(where))
	for i, k := range setKeys {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(fmt.Sprintf("%s = %s",
			quoteIdent(k, engine), placeholder(i+1, engine)))
		args = append(args, set[k])
	}

	// PK clause. Use the table's declared PK order from pkCols.
	b.WriteString(" WHERE ")
	idx := len(setKeys) + 1
	for i, c := range pkCols {
		if i > 0 {
			b.WriteString(" AND ")
		}
		v, ok := pkValues[c]
		if !ok {
			return "", nil, fmt.Errorf("%w", ErrPKMismatch)
		}
		b.WriteString(fmt.Sprintf("%s = %s",
			quoteIdent(c, engine), placeholder(idx, engine)))
		args = append(args, v)
		idx++
	}

	// edit-1: snapshot clauses. Sort keys for deterministic SQL.
	if len(where) > 0 {
		whereKeys := make([]string, 0, len(where))
		for k := range where {
			whereKeys = append(whereKeys, k)
		}
		sort.Strings(whereKeys)
		for _, c := range whereKeys {
			b.WriteString(" AND ")
			v := where[c]
			// Match-by-NULL needs IS NULL, not = NULL.
			if v == nil {
				b.WriteString(fmt.Sprintf("%s IS NULL", quoteIdent(c, engine)))
				continue
			}
			b.WriteString(fmt.Sprintf("%s = %s",
				quoteIdent(c, engine), placeholder(idx, engine)))
			args = append(args, v)
			idx++
		}
	}

	return b.String(), args, nil
}

// ─── DELETE ──────────────────────────────────────────────────────────

func buildDelete(
	engine dbadmin.EngineKind,
	schemaName, table string,
	pkCols []string,
	pkValues map[string]any,
) (string, []any, error) {
	var b strings.Builder
	b.WriteString("DELETE FROM ")
	b.WriteString(qualifyTable(schemaName, table, engine))
	b.WriteString(" WHERE ")

	args := make([]any, 0, len(pkValues))
	for i, c := range pkCols {
		if i > 0 {
			b.WriteString(" AND ")
		}
		v, ok := pkValues[c]
		if !ok {
			return "", nil, fmt.Errorf("%w", ErrPKMismatch)
		}
		b.WriteString(fmt.Sprintf("%s = %s",
			quoteIdent(c, engine), placeholder(i+1, engine)))
		args = append(args, v)
	}
	return b.String(), args, nil
}

// ─── INSERT ──────────────────────────────────────────────────────────

func buildInsert(
	engine dbadmin.EngineKind,
	schemaName, table string,
	values map[string]any,
) (string, []any, error) {
	if len(values) == 0 {
		return "", nil, fmt.Errorf("rows/build: INSERT requires at least one value")
	}

	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("INSERT INTO ")
	b.WriteString(qualifyTable(schemaName, table, engine))
	b.WriteString(" (")
	for i, k := range keys {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(quoteIdent(k, engine))
	}
	b.WriteString(") VALUES (")
	args := make([]any, 0, len(values))
	for i, k := range keys {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(placeholder(i+1, engine))
		args = append(args, values[k])
	}
	b.WriteString(")")
	return b.String(), args, nil
}

// buildInsertReturning is the Postgres-only variant of buildInsert that
// appends `RETURNING <pk>` so the driver path can populate LastInsertID
// (H6). The pgx driver does NOT support LastInsertId() — the Postgres
// protocol doesn't even have an out-of-band "last id" channel — so
// without RETURNING the panel UI gets LastInsertID=0 and can't refresh
// the just-inserted row by PK. We call this only when the schema
// reader confirms exactly one PK column.
func buildInsertReturning(
	engine dbadmin.EngineKind,
	schemaName, table string,
	values map[string]any,
	pkCol string,
) (string, []any, error) {
	q, args, err := buildInsert(engine, schemaName, table, values)
	if err != nil {
		return "", nil, err
	}
	return q + " RETURNING " + quoteIdent(pkCol, engine), args, nil
}

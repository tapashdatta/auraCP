package rows

import (
	"fmt"
	"sort"
	"strings"

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
	// the same parse tree.
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
		return fmt.Sprintf("%s %s %s", col, p.Op, placeholder(idx, engine)),
			[]any{p.Value}, nil

	case OpILike:
		if engine == dbadmin.EnginePostgres {
			return fmt.Sprintf("%s ILIKE %s", col, placeholder(idx, engine)),
				[]any{p.Value}, nil
		}
		// MySQL has no ILIKE; rewrite to LOWER(col) LIKE LOWER(?).
		return fmt.Sprintf("LOWER(%s) LIKE LOWER(%s)", col, placeholder(idx, engine)),
			[]any{p.Value}, nil

	case OpIsNull:
		return fmt.Sprintf("%s IS NULL", col), nil, nil
	case OpIsNotNull:
		return fmt.Sprintf("%s IS NOT NULL", col), nil, nil

	case OpIn, OpNotIn:
		values, err := flattenInValue(p.Value)
		if err != nil {
			return "", nil, fmt.Errorf("%w: %v", ErrInvalidPredicate, err)
		}
		if len(values) == 0 {
			// Empty IN list: in SQL this means "match nothing" (IN)
			// or "match everything" (NOT IN). We emit a literal
			// FALSE / TRUE clause respectively to keep the bind
			// parameter list non-empty.
			if p.Op == OpIn {
				return "1=0", nil, nil
			}
			return "1=1", nil, nil
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

// flattenInValue accepts []any, []string, []int, []int64, etc., and
// returns []any for placeholder binding.
func flattenInValue(v any) ([]any, error) {
	switch x := v.(type) {
	case nil:
		return nil, fmt.Errorf("IN value is nil")
	case []any:
		return x, nil
	case []string:
		out := make([]any, len(x))
		for i, s := range x {
			out[i] = s
		}
		return out, nil
	case []int:
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
	case []float64:
		out := make([]any, len(x))
		for i, n := range x {
			out[i] = n
		}
		return out, nil
	default:
		return nil, fmt.Errorf("IN value must be a slice, got %T", v)
	}
}

// ─── UPDATE ──────────────────────────────────────────────────────────

func buildUpdate(
	engine dbadmin.EngineKind,
	schemaName, table string,
	set map[string]any,
	pkCols []string,
	pkValues map[string]any,
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

	args := make([]any, 0, len(set)+len(pkValues))
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

package schema

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/driver"
)

// postgresReader queries PostgreSQL's pg_catalog + information_schema
// via driver.Conn. Identifiers are bound as $1, $2, ... — never
// concatenated into SQL.
type postgresReader struct {
	conn driver.Conn
}

func (r *postgresReader) Engine() dbadmin.EngineKind { return dbadmin.EnginePostgres }

func (r *postgresReader) ListDatabases(ctx context.Context) ([]string, error) {
	// Excludes the templates + filters to databases the connecting
	// role can CONNECT to. has_database_privilege handles cross-tenant
	// installs where the operator only has access to a subset.
	const q = `
		SELECT datname
		FROM pg_database
		WHERE NOT datistemplate
		  AND datallowconn
		  AND has_database_privilege(datname, 'CONNECT')
		ORDER BY datname`
	return r.fetchStrings(ctx, q)
}

func (r *postgresReader) ListSchemas(ctx context.Context, database string) ([]string, error) {
	if err := ValidateIdentifier(database); err != nil {
		return nil, err
	}
	// We can only query the CURRENT database via pg_namespace; the
	// `database` argument is informational for symmetry with the
	// MySQL reader. Excludes system schemas.
	const q = `
		SELECT nspname
		FROM pg_namespace
		WHERE nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
		  AND nspname NOT LIKE 'pg_temp_%'
		  AND nspname NOT LIKE 'pg_toast_temp_%'
		  AND has_schema_privilege(nspname, 'USAGE')
		ORDER BY nspname`
	return r.fetchStrings(ctx, q)
}

func (r *postgresReader) ListTables(ctx context.Context, schema string) ([]TableSummary, error) {
	if err := ValidateIdentifier(schema); err != nil {
		return nil, err
	}
	const q = `
		SELECT c.relname,
		       c.relkind,
		       coalesce(obj_description(c.oid, 'pg_class'), ''),
		       coalesce(c.reltuples, 0)::bigint
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1
		  AND c.relkind IN ('r', 'p', 'v', 'm')
		ORDER BY c.relname`

	rows, err := r.conn.Query(ctx, defaultLimits(), q, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TableSummary
	for {
		vals, err := rows.Next(ctx)
		if errors.Is(err, driver.ErrEOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(vals) < 4 {
			continue
		}
		kind := classifyRelkind(toString(vals[1]))
		out = append(out, TableSummary{
			Schema:       schema,
			Name:         toString(vals[0]),
			Kind:         kind,
			Comment:      toString(vals[2]),
			RowsEstimate: toInt64(vals[3]),
		})
	}
	return out, nil
}

func (r *postgresReader) GetTable(ctx context.Context, schema, table string) (*Table, error) {
	if err := ValidateIdentifier(schema); err != nil {
		return nil, err
	}
	if err := ValidateIdentifier(table); err != nil {
		return nil, err
	}

	// Step 1: relation metadata.
	tbl, err := r.getTableMeta(ctx, schema, table)
	if err != nil {
		return nil, err
	}

	// Step 2: columns + PK detection (PK info via pg_index).
	if err := r.fillColumns(ctx, tbl); err != nil {
		return nil, err
	}

	// Step 3: indexes.
	if err := r.fillIndexes(ctx, tbl); err != nil {
		return nil, err
	}

	// Step 4: foreign keys.
	if err := r.fillForeignKeys(ctx, tbl); err != nil {
		return nil, err
	}

	// Step 5: triggers.
	if err := r.fillTriggers(ctx, tbl); err != nil {
		tbl.Triggers = nil
	}

	return tbl, nil
}

func (r *postgresReader) getTableMeta(ctx context.Context, schema, table string) (*Table, error) {
	const q = `
		SELECT c.relkind,
		       coalesce(obj_description(c.oid, 'pg_class'), ''),
		       coalesce(ts.spcname, ''),
		       coalesce(am.amname, '')
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_tablespace ts ON ts.oid = c.reltablespace
		LEFT JOIN pg_am am ON am.oid = c.relam
		WHERE n.nspname = $1 AND c.relname = $2`

	rows, err := r.conn.Query(ctx, defaultLimits(), q, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	vals, err := rows.Next(ctx)
	if errors.Is(err, driver.ErrEOF) {
		return nil, ErrTableNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(vals) < 4 {
		return nil, fmt.Errorf("schema/postgres: getTable: unexpected column count")
	}

	tbl := &Table{
		Schema:  schema,
		Name:    table,
		Kind:    classifyRelkind(toString(vals[0])),
		Comment: toString(vals[1]),
		Extras: map[string]string{
			"tablespace":   toString(vals[2]),
			"access_method": toString(vals[3]),
		},
	}
	return tbl, nil
}

func (r *postgresReader) fillColumns(ctx context.Context, tbl *Table) error {
	const q = `
		SELECT a.attname,
		       a.attnum,
		       format_type(a.atttypid, a.atttypmod) AS data_type,
		       NOT a.attnotnull AS nullable,
		       coalesce(pg_get_expr(d.adbin, d.adrelid), '') AS default_expr,
		       coalesce(col_description(a.attrelid, a.attnum), '') AS comment,
		       a.attidentity != '' AS is_identity,
		       a.attgenerated != '' AS is_generated
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		WHERE n.nspname = $1 AND c.relname = $2
		  AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum`

	rows, err := r.conn.Query(ctx, defaultLimits(), q, tbl.Schema, tbl.Name)
	if err != nil {
		return err
	}
	defer rows.Close()

	for {
		vals, err := rows.Next(ctx)
		if errors.Is(err, driver.ErrEOF) {
			break
		}
		if err != nil {
			return err
		}
		if len(vals) < 8 {
			continue
		}
		col := Column{
			Name:            toString(vals[0]),
			Position:        int(toInt64(vals[1])),
			DataType:        toString(vals[2]),
			Nullable:        toBool(vals[3]),
			Default:         toString(vals[4]),
			Comment:         toString(vals[5]),
			IsAutoIncrement: toBool(vals[6]),
			IsGenerated:     toBool(vals[7]),
		}
		tbl.Columns = append(tbl.Columns, col)
	}

	// PK detection — separate query against pg_index.
	pkCols, err := r.fetchPrimaryKey(ctx, tbl.Schema, tbl.Name)
	if err != nil {
		return err
	}
	tbl.PrimaryKey = pkCols
	pkSet := map[string]bool{}
	for _, c := range pkCols {
		pkSet[c] = true
	}
	for i := range tbl.Columns {
		if pkSet[tbl.Columns[i].Name] {
			tbl.Columns[i].IsPrimaryKey = true
		}
	}
	return nil
}

func (r *postgresReader) fetchPrimaryKey(ctx context.Context, schema, table string) ([]string, error) {
	const q = `
		SELECT a.attname
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = ANY(i.indkey)
		WHERE n.nspname = $1 AND c.relname = $2 AND i.indisprimary
		ORDER BY array_position(i.indkey, a.attnum)`

	rows, err := r.conn.Query(ctx, defaultLimits(), q, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for {
		vals, err := rows.Next(ctx)
		if errors.Is(err, driver.ErrEOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(vals) == 0 {
			continue
		}
		out = append(out, toString(vals[0]))
	}
	return out, nil
}

func (r *postgresReader) fillIndexes(ctx context.Context, tbl *Table) error {
	const q = `
		SELECT i.relname AS index_name,
		       idx.indisunique,
		       idx.indisprimary,
		       coalesce(am.amname, ''),
		       coalesce(pg_get_expr(idx.indpred, idx.indrelid), ''),
		       array_to_string(array_agg(a.attname ORDER BY array_position(idx.indkey, a.attnum)), ',')
		FROM pg_index idx
		JOIN pg_class c ON c.oid = idx.indrelid
		JOIN pg_class i ON i.oid = idx.indexrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_am am ON am.oid = i.relam
		LEFT JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = ANY(idx.indkey)
		WHERE n.nspname = $1 AND c.relname = $2
		GROUP BY i.relname, idx.indisunique, idx.indisprimary, am.amname, idx.indpred, idx.indrelid
		ORDER BY i.relname`

	rows, err := r.conn.Query(ctx, defaultLimits(), q, tbl.Schema, tbl.Name)
	if err != nil {
		return err
	}
	defer rows.Close()

	for {
		vals, err := rows.Next(ctx)
		if errors.Is(err, driver.ErrEOF) {
			break
		}
		if err != nil {
			return err
		}
		if len(vals) < 6 {
			continue
		}
		cols := strings.Split(toString(vals[5]), ",")
		// Trim and drop empty (which can happen when ANY() expands an
		// indkey containing 0 for expression-indexes).
		cleaned := cols[:0]
		for _, c := range cols {
			c = strings.TrimSpace(c)
			if c != "" {
				cleaned = append(cleaned, c)
			}
		}
		tbl.Indexes = append(tbl.Indexes, Index{
			Name:      toString(vals[0]),
			Unique:    toBool(vals[1]),
			Primary:   toBool(vals[2]),
			Method:    strings.ToUpper(toString(vals[3])),
			Predicate: toString(vals[4]),
			Columns:   cleaned,
		})
	}
	return nil
}

func (r *postgresReader) fillForeignKeys(ctx context.Context, tbl *Table) error {
	const q = `
		SELECT con.conname,
		       array_agg(a.attname ORDER BY u.ord) AS cols,
		       fn.nspname,
		       fc.relname,
		       array_agg(fa.attname ORDER BY u.ord) AS refcols,
		       con.confdeltype, con.confupdtype
		FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_class fc ON fc.oid = con.confrelid
		JOIN pg_namespace fn ON fn.oid = fc.relnamespace
		JOIN unnest(con.conkey, con.confkey) WITH ORDINALITY AS u(local_attnum, ref_attnum, ord) ON true
		JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = u.local_attnum
		JOIN pg_attribute fa ON fa.attrelid = con.confrelid AND fa.attnum = u.ref_attnum
		WHERE con.contype = 'f' AND n.nspname = $1 AND c.relname = $2
		GROUP BY con.conname, fn.nspname, fc.relname, con.confdeltype, con.confupdtype
		ORDER BY con.conname`

	rows, err := r.conn.Query(ctx, defaultLimits(), q, tbl.Schema, tbl.Name)
	if err != nil {
		return err
	}
	defer rows.Close()

	for {
		vals, err := rows.Next(ctx)
		if errors.Is(err, driver.ErrEOF) {
			break
		}
		if err != nil {
			return err
		}
		if len(vals) < 7 {
			continue
		}
		tbl.ForeignKeys = append(tbl.ForeignKeys, ForeignKey{
			Name:              toString(vals[0]),
			Columns:           toStringSlice(vals[1]),
			ReferencedSchema:  toString(vals[2]),
			ReferencedTable:   toString(vals[3]),
			ReferencedColumns: toStringSlice(vals[4]),
			OnDelete:          fkActionCode(toString(vals[5])),
			OnUpdate:          fkActionCode(toString(vals[6])),
		})
	}
	return nil
}

func (r *postgresReader) fillTriggers(ctx context.Context, tbl *Table) error {
	// Triggers can fire on multiple events (e.g. "INSERT OR UPDATE").
	// Use array_to_string over array_remove(ARRAY[...], NULL) so all
	// matching event bits are returned, joined by ' OR '.
	const q = `
		SELECT t.tgname,
		       case when t.tgtype & 2 = 2 then 'BEFORE'
		            when t.tgtype & 64 = 64 then 'INSTEAD OF'
		            else 'AFTER' end AS timing,
		       array_to_string(array_remove(ARRAY[
		           CASE WHEN t.tgtype & 4 = 4 THEN 'INSERT' END,
		           CASE WHEN t.tgtype & 8 = 8 THEN 'DELETE' END,
		           CASE WHEN t.tgtype & 16 = 16 THEN 'UPDATE' END,
		           CASE WHEN t.tgtype & 32 = 32 THEN 'TRUNCATE' END
		       ], NULL), ' OR ') AS event,
		       coalesce(pg_get_triggerdef(t.oid, true), '')
		FROM pg_trigger t
		JOIN pg_class c ON c.oid = t.tgrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2 AND NOT t.tgisinternal
		ORDER BY t.tgname`

	rows, err := r.conn.Query(ctx, defaultLimits(), q, tbl.Schema, tbl.Name)
	if err != nil {
		return err
	}
	defer rows.Close()

	for {
		vals, err := rows.Next(ctx)
		if errors.Is(err, driver.ErrEOF) {
			break
		}
		if err != nil {
			return err
		}
		if len(vals) < 4 {
			continue
		}
		tbl.Triggers = append(tbl.Triggers, TriggerSummary{
			Schema:     tbl.Schema,
			Table:      tbl.Name,
			Name:       toString(vals[0]),
			Timing:     toString(vals[1]),
			Event:      toString(vals[2]),
			Definition: toString(vals[3]),
		})
	}
	return nil
}

func (r *postgresReader) ListViews(ctx context.Context, schema string) ([]ViewSummary, error) {
	if err := ValidateIdentifier(schema); err != nil {
		return nil, err
	}
	const q = `
		SELECT c.relname,
		       coalesce(pg_get_viewdef(c.oid, true), ''),
		       coalesce(obj_description(c.oid, 'pg_class'), '')
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind IN ('v', 'm')
		ORDER BY c.relname`

	rows, err := r.conn.Query(ctx, defaultLimits(), q, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ViewSummary
	for {
		vals, err := rows.Next(ctx)
		if errors.Is(err, driver.ErrEOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(vals) < 3 {
			continue
		}
		out = append(out, ViewSummary{
			Schema:     schema,
			Name:       toString(vals[0]),
			Definition: toString(vals[1]),
			Comment:    toString(vals[2]),
			// Postgres views are updatable depending on the
			// definition; we don't currently extract is_updatable.
			Updatable: false,
		})
	}
	return out, nil
}

func (r *postgresReader) ListFunctions(ctx context.Context, schema string) ([]FunctionSummary, error) {
	if err := ValidateIdentifier(schema); err != nil {
		return nil, err
	}
	const q = `
		SELECT p.proname,
		       l.lanname,
		       pg_get_function_result(p.oid),
		       pg_get_function_arguments(p.oid),
		       coalesce(obj_description(p.oid, 'pg_proc'), ''),
		       p.prokind = 'a'
		FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		JOIN pg_language l ON l.oid = p.prolang
		WHERE n.nspname = $1 AND p.prokind IN ('f', 'a', 'w')
		ORDER BY p.proname`

	rows, err := r.conn.Query(ctx, defaultLimits(), q, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []FunctionSummary
	for {
		vals, err := rows.Next(ctx)
		if errors.Is(err, driver.ErrEOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(vals) < 6 {
			continue
		}
		out = append(out, FunctionSummary{
			Schema:      schema,
			Name:        toString(vals[0]),
			Language:    toString(vals[1]),
			ReturnType:  toString(vals[2]),
			Arguments:   toString(vals[3]),
			Comment:     toString(vals[4]),
			IsAggregate: toBool(vals[5]),
		})
	}
	return out, nil
}

func (r *postgresReader) ListProcedures(ctx context.Context, schema string) ([]ProcedureSummary, error) {
	if err := ValidateIdentifier(schema); err != nil {
		return nil, err
	}
	const q = `
		SELECT p.proname,
		       l.lanname,
		       pg_get_function_arguments(p.oid),
		       coalesce(obj_description(p.oid, 'pg_proc'), '')
		FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		JOIN pg_language l ON l.oid = p.prolang
		WHERE n.nspname = $1 AND p.prokind = 'p'
		ORDER BY p.proname`

	rows, err := r.conn.Query(ctx, defaultLimits(), q, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ProcedureSummary
	for {
		vals, err := rows.Next(ctx)
		if errors.Is(err, driver.ErrEOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(vals) < 4 {
			continue
		}
		out = append(out, ProcedureSummary{
			Schema:    schema,
			Name:      toString(vals[0]),
			Language:  toString(vals[1]),
			Arguments: toString(vals[2]),
			Comment:   toString(vals[3]),
		})
	}
	return out, nil
}

func (r *postgresReader) ListTriggers(ctx context.Context, schema string) ([]TriggerSummary, error) {
	if err := ValidateIdentifier(schema); err != nil {
		return nil, err
	}
	// See fillTriggers for the array-based event detection rationale.
	const q = `
		SELECT t.tgname, c.relname,
		       case when t.tgtype & 2 = 2 then 'BEFORE'
		            when t.tgtype & 64 = 64 then 'INSTEAD OF'
		            else 'AFTER' end AS timing,
		       array_to_string(array_remove(ARRAY[
		           CASE WHEN t.tgtype & 4 = 4 THEN 'INSERT' END,
		           CASE WHEN t.tgtype & 8 = 8 THEN 'DELETE' END,
		           CASE WHEN t.tgtype & 16 = 16 THEN 'UPDATE' END,
		           CASE WHEN t.tgtype & 32 = 32 THEN 'TRUNCATE' END
		       ], NULL), ' OR ') AS event,
		       coalesce(pg_get_triggerdef(t.oid, true), '')
		FROM pg_trigger t
		JOIN pg_class c ON c.oid = t.tgrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND NOT t.tgisinternal
		ORDER BY c.relname, t.tgname`

	rows, err := r.conn.Query(ctx, defaultLimits(), q, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TriggerSummary
	for {
		vals, err := rows.Next(ctx)
		if errors.Is(err, driver.ErrEOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(vals) < 5 {
			continue
		}
		out = append(out, TriggerSummary{
			Schema:     schema,
			Table:      toString(vals[1]),
			Name:       toString(vals[0]),
			Timing:     toString(vals[2]),
			Event:      toString(vals[3]),
			Definition: toString(vals[4]),
		})
	}
	return out, nil
}

// ─── Helpers ────────────────────────────────────────────────────────

func (r *postgresReader) fetchStrings(ctx context.Context, sqlText string, args ...any) ([]string, error) {
	rows, err := r.conn.Query(ctx, defaultLimits(), sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for {
		vals, err := rows.Next(ctx)
		if errors.Is(err, driver.ErrEOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(vals) == 0 {
			continue
		}
		out = append(out, toString(vals[0]))
	}
	return out, nil
}

// classifyRelkind maps Postgres pg_class.relkind to our TableKind enum.
func classifyRelkind(relkind string) TableKind {
	switch relkind {
	case "r", "p": // r=regular table, p=partitioned table
		return KindTable
	case "v":
		return KindView
	case "m":
		return KindMaterializedView
	default:
		return KindTable // best-effort fallback
	}
}

// fkActionCode translates pg_constraint.confdeltype / confupdtype codes
// into human-readable form (CASCADE / SET NULL / NO ACTION / RESTRICT /
// SET DEFAULT).
func fkActionCode(code string) string {
	switch code {
	case "a":
		return "NO ACTION"
	case "r":
		return "RESTRICT"
	case "c":
		return "CASCADE"
	case "n":
		return "SET NULL"
	case "d":
		return "SET DEFAULT"
	default:
		return code
	}
}

// toBool defensively converts a driver-returned value to bool. pgx
// generally returns bool directly for boolean columns; fall back to
// string for cases where the value comes through as text.
func toBool(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		s := strings.ToLower(x)
		return s == "t" || s == "true" || s == "yes" || s == "1"
	case []byte:
		return toBool(string(x))
	case int64:
		return x != 0
	case int:
		return x != 0
	default:
		return false
	}
}

// toStringSlice converts pgx's array-of-strings (returned as []any from
// array_agg) into a Go []string.
func toStringSlice(v any) []string {
	switch x := v.(type) {
	case nil:
		return nil
	case []string:
		out := make([]string, len(x))
		copy(out, x)
		return out
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			out = append(out, toString(e))
		}
		return out
	case string:
		// Single string (postgres sometimes folds 1-element arrays).
		if x == "" {
			return nil
		}
		return strings.Split(x, ",")
	default:
		return nil
	}
}

package schema

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/driver"
)

// mysqlReader queries MariaDB / MySQL's information_schema via
// driver.Conn. All filter values pass as bind parameters; we NEVER
// concatenate operator-supplied identifiers into SQL.
type mysqlReader struct {
	conn driver.Conn
}

func (r *mysqlReader) Engine() dbadmin.EngineKind { return dbadmin.EngineMariaDB }

func (r *mysqlReader) ListDatabases(ctx context.Context) ([]string, error) {
	// Filter out the system schemas the operator can't (and shouldn't)
	// administer through Aura DB. Operators with elevated privs can
	// still address them by typing the name explicitly; this filter is
	// just the default list view.
	const q = `
		SELECT schema_name
		FROM information_schema.schemata
		WHERE schema_name NOT IN ('information_schema', 'mysql', 'performance_schema', 'sys')
		ORDER BY schema_name`
	return r.fetchStrings(ctx, q)
}

func (r *mysqlReader) ListSchemas(ctx context.Context, database string) ([]string, error) {
	// MySQL conflates schema and database. The "schemas" of a database
	// is just the database itself; return a single-element slice so
	// the caller can use the same shape as Postgres without branching.
	if err := ValidateIdentifier(database); err != nil {
		return nil, err
	}
	return []string{database}, nil
}

func (r *mysqlReader) ListTables(ctx context.Context, schema string) ([]TableSummary, error) {
	if err := ValidateIdentifier(schema); err != nil {
		return nil, err
	}
	const q = `
		SELECT table_name, table_type, ifnull(table_comment, ''),
		       ifnull(table_rows, 0), ifnull(engine, '')
		FROM information_schema.tables
		WHERE table_schema = ?
		ORDER BY table_name`

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
		if len(vals) < 5 {
			continue
		}
		kind := KindTable
		ttype := toString(vals[1])
		if strings.EqualFold(ttype, "VIEW") {
			kind = KindView
		}
		out = append(out, TableSummary{
			Schema:       schema,
			Name:         toString(vals[0]),
			Kind:         kind,
			Comment:      toString(vals[2]),
			RowsEstimate: toInt64(vals[3]),
			Engine:       toString(vals[4]),
		})
	}
	return out, nil
}

func (r *mysqlReader) GetTable(ctx context.Context, schema, table string) (*Table, error) {
	if err := ValidateIdentifier(schema); err != nil {
		return nil, err
	}
	if err := ValidateIdentifier(table); err != nil {
		return nil, err
	}

	// Step 1: table-level metadata.
	tbl, err := r.getTableMeta(ctx, schema, table)
	if err != nil {
		return nil, err
	}

	// Step 2: columns + PK detection.
	if err := r.fillColumns(ctx, tbl); err != nil {
		return nil, err
	}

	// Step 3: indexes (folds PK detection back into Column.IsPrimaryKey
	// for the rare DBs that have a PK with a non-standard name).
	if err := r.fillIndexes(ctx, tbl); err != nil {
		return nil, err
	}

	// Step 4: foreign keys.
	if err := r.fillForeignKeys(ctx, tbl); err != nil {
		return nil, err
	}

	// Step 5: triggers (best-effort; some MySQL configs hide triggers
	// without SUPER privilege).
	if err := r.fillTriggers(ctx, tbl); err != nil {
		// Don't fail the whole call for trigger-list permission
		// issues; surface what we have.
		tbl.Triggers = nil
	}

	return tbl, nil
}

func (r *mysqlReader) getTableMeta(ctx context.Context, schema, table string) (*Table, error) {
	const q = `
		SELECT table_type, ifnull(table_comment, ''), ifnull(engine, ''),
		       ifnull(table_collation, ''), ifnull(create_options, '')
		FROM information_schema.tables
		WHERE table_schema = ? AND table_name = ?`

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
	if len(vals) < 5 {
		return nil, fmt.Errorf("schema/mysql: getTable: unexpected column count")
	}

	kind := KindTable
	if strings.EqualFold(toString(vals[0]), "VIEW") {
		kind = KindView
	}
	tbl := &Table{
		Schema:  schema,
		Name:    table,
		Kind:    kind,
		Comment: toString(vals[1]),
		Extras: map[string]string{
			"engine":         toString(vals[2]),
			"collation":      toString(vals[3]),
			"create_options": toString(vals[4]),
		},
	}
	return tbl, nil
}

func (r *mysqlReader) fillColumns(ctx context.Context, tbl *Table) error {
	const q = `
		SELECT column_name, ordinal_position, column_type, is_nullable,
		       ifnull(column_default, ''), ifnull(column_comment, ''),
		       column_key, extra,
		       ifnull(character_set_name, ''), ifnull(collation_name, '')
		FROM information_schema.columns
		WHERE table_schema = ? AND table_name = ?
		ORDER BY ordinal_position`

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
		if len(vals) < 10 {
			continue
		}
		extra := toString(vals[7])
		colKey := toString(vals[6])
		col := Column{
			Name:            toString(vals[0]),
			Position:        int(toInt64(vals[1])),
			DataType:        toString(vals[2]),
			Nullable:        strings.EqualFold(toString(vals[3]), "YES"),
			Default:         toString(vals[4]),
			Comment:         toString(vals[5]),
			// IsPrimaryKey is a per-column flag (column participates in
			// the PK). The PK column ORDER lives in the PRIMARY index's
			// seq_in_index ordering, which fillIndexes folds back into
			// tbl.PrimaryKey.
			IsPrimaryKey:    colKey == "PRI",
			IsAutoIncrement: strings.Contains(strings.ToLower(extra), "auto_increment"),
			IsGenerated:     strings.Contains(strings.ToLower(extra), "generated"),
			CharacterSet:    toString(vals[8]),
			Collation:       toString(vals[9]),
		}
		tbl.Columns = append(tbl.Columns, col)
	}
	return nil
}

func (r *mysqlReader) fillIndexes(ctx context.Context, tbl *Table) error {
	const q = `
		SELECT index_name, column_name, non_unique, seq_in_index,
		       ifnull(index_type, ''), ifnull(index_comment, '')
		FROM information_schema.statistics
		WHERE table_schema = ? AND table_name = ?
		ORDER BY index_name, seq_in_index`

	rows, err := r.conn.Query(ctx, defaultLimits(), q, tbl.Schema, tbl.Name)
	if err != nil {
		return err
	}
	defer rows.Close()

	// Indexes are normalized by (index_name) — multiple rows per index,
	// one per column.
	idxByName := map[string]*Index{}
	var order []string
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
		name := toString(vals[0])
		col := toString(vals[1])
		nonUnique := toInt64(vals[2])
		idx, ok := idxByName[name]
		if !ok {
			idx = &Index{
				Name:    name,
				Unique:  nonUnique == 0,
				Primary: name == "PRIMARY",
				Method:  toString(vals[4]),
				Comment: toString(vals[5]),
			}
			idxByName[name] = idx
			order = append(order, name)
		}
		idx.Columns = append(idx.Columns, col)
	}
	for _, name := range order {
		tbl.Indexes = append(tbl.Indexes, *idxByName[name])
	}
	// PK column order comes from the PRIMARY index's seq_in_index, not
	// from the column loop. Fold it back into tbl.PrimaryKey here.
	if pk, ok := idxByName["PRIMARY"]; ok && pk != nil {
		tbl.PrimaryKey = append([]string(nil), pk.Columns...)
	}
	return nil
}

func (r *mysqlReader) fillForeignKeys(ctx context.Context, tbl *Table) error {
	const q = `
		SELECT k.constraint_name, k.column_name,
		       k.referenced_table_schema, k.referenced_table_name,
		       k.referenced_column_name,
		       ifnull(rc.delete_rule, ''), ifnull(rc.update_rule, '')
		FROM information_schema.key_column_usage k
		LEFT JOIN information_schema.referential_constraints rc
		  ON k.constraint_schema = rc.constraint_schema
		 AND k.constraint_name   = rc.constraint_name
		WHERE k.table_schema = ?
		  AND k.table_name = ?
		  AND k.referenced_table_name IS NOT NULL
		ORDER BY k.constraint_name, k.ordinal_position`

	rows, err := r.conn.Query(ctx, defaultLimits(), q, tbl.Schema, tbl.Name)
	if err != nil {
		return err
	}
	defer rows.Close()

	fkByName := map[string]*ForeignKey{}
	var order []string
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
		name := toString(vals[0])
		fk, ok := fkByName[name]
		if !ok {
			fk = &ForeignKey{
				Name:             name,
				ReferencedSchema: toString(vals[2]),
				ReferencedTable:  toString(vals[3]),
				OnDelete:         toString(vals[5]),
				OnUpdate:         toString(vals[6]),
			}
			fkByName[name] = fk
			order = append(order, name)
		}
		fk.Columns = append(fk.Columns, toString(vals[1]))
		fk.ReferencedColumns = append(fk.ReferencedColumns, toString(vals[4]))
	}
	for _, name := range order {
		tbl.ForeignKeys = append(tbl.ForeignKeys, *fkByName[name])
	}
	return nil
}

func (r *mysqlReader) fillTriggers(ctx context.Context, tbl *Table) error {
	// CONCAT synthesizes a full CREATE TRIGGER statement to match
	// Postgres's pg_get_triggerdef output shape. See H13 in the PR #4
	// review.
	const q = `
		SELECT trigger_name, event_manipulation, action_timing,
		       CONCAT('CREATE TRIGGER ', trigger_name, ' ', action_timing, ' ',
		              event_manipulation, ' ON ', event_object_table,
		              ' FOR EACH ROW ', ifnull(action_statement, ''))
		FROM information_schema.triggers
		WHERE event_object_schema = ? AND event_object_table = ?
		ORDER BY trigger_name`

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
			Event:      toString(vals[1]),
			Timing:     toString(vals[2]),
			Definition: toString(vals[3]),
		})
	}
	return nil
}

func (r *mysqlReader) ListViews(ctx context.Context, schema string) ([]ViewSummary, error) {
	if err := ValidateIdentifier(schema); err != nil {
		return nil, err
	}
	const q = `
		SELECT table_name, ifnull(view_definition, ''),
		       case is_updatable when 'YES' then 1 else 0 end
		FROM information_schema.views
		WHERE table_schema = ?
		ORDER BY table_name`

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
			Updatable:  toInt64(vals[2]) == 1,
		})
	}
	return out, nil
}

func (r *mysqlReader) ListFunctions(ctx context.Context, schema string) ([]FunctionSummary, error) {
	if err := ValidateIdentifier(schema); err != nil {
		return nil, err
	}
	const q = `
		SELECT routine_name, ifnull(external_language, routine_body),
		       ifnull(data_type, ''),
		       ifnull(dtd_identifier, '')
		FROM information_schema.routines
		WHERE routine_schema = ? AND routine_type = 'FUNCTION'
		ORDER BY routine_name`

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
		if len(vals) < 4 {
			continue
		}
		out = append(out, FunctionSummary{
			Schema:     schema,
			Name:       toString(vals[0]),
			Language:   toString(vals[1]),
			ReturnType: toString(vals[2]),
			Arguments:  toString(vals[3]),
		})
	}
	return out, nil
}

func (r *mysqlReader) ListProcedures(ctx context.Context, schema string) ([]ProcedureSummary, error) {
	if err := ValidateIdentifier(schema); err != nil {
		return nil, err
	}
	const q = `
		SELECT routine_name, ifnull(external_language, routine_body),
		       ifnull(dtd_identifier, '')
		FROM information_schema.routines
		WHERE routine_schema = ? AND routine_type = 'PROCEDURE'
		ORDER BY routine_name`

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
		if len(vals) < 3 {
			continue
		}
		out = append(out, ProcedureSummary{
			Schema:    schema,
			Name:      toString(vals[0]),
			Language:  toString(vals[1]),
			Arguments: toString(vals[2]),
		})
	}
	return out, nil
}

func (r *mysqlReader) ListTriggers(ctx context.Context, schema string) ([]TriggerSummary, error) {
	if err := ValidateIdentifier(schema); err != nil {
		return nil, err
	}
	// Synthesize full CREATE TRIGGER ... DDL to match Postgres parity
	// (see TriggerSummary.Definition doc and PR #4 review H13).
	const q = `
		SELECT trigger_name, event_object_table, event_manipulation,
		       action_timing,
		       CONCAT('CREATE TRIGGER ', trigger_name, ' ', action_timing, ' ',
		              event_manipulation, ' ON ', event_object_table,
		              ' FOR EACH ROW ', ifnull(action_statement, ''))
		FROM information_schema.triggers
		WHERE trigger_schema = ?
		ORDER BY event_object_table, trigger_name`

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
			Event:      toString(vals[2]),
			Timing:     toString(vals[3]),
			Definition: toString(vals[4]),
		})
	}
	return out, nil
}

// ─── Helpers ────────────────────────────────────────────────────────

// fetchStrings is a convenience for queries that return a single-column
// list of strings (ListDatabases, ListSchemas-ish helpers).
func (r *mysqlReader) fetchStrings(ctx context.Context, sqlText string, args ...any) ([]string, error) {
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

// toString defensively converts a driver-returned value to string. The
// MySQL driver maps TEXT/VARCHAR columns to string already, but some
// metadata columns come back as []byte; this handles both.
func toString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	default:
		return fmt.Sprintf("%v", x)
	}
}

// toInt64 defensively converts a driver-returned value to int64.
func toInt64(v any) int64 {
	switch x := v.(type) {
	case nil:
		return 0
	case int64:
		return x
	case int:
		return int64(x)
	case int32:
		return int64(x)
	case uint64:
		return int64(x)
	case uint32:
		return int64(x)
	case []byte:
		// information_schema sometimes returns numeric columns as
		// bytes when the column is unsigned BIGINT. Trying to convert
		// is the conservative path; bad values yield 0.
		var n int64
		for _, b := range x {
			if b < '0' || b > '9' {
				return n
			}
			n = n*10 + int64(b-'0')
		}
		return n
	default:
		return 0
	}
}

package export

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// sqlEncoder writes one INSERT statement per row. Identifiers + values
// are engine-quoted: MariaDB uses backticks for idents and X'...' for
// binary literals; Postgres uses double-quotes and '\\x...'::bytea.
type sqlEncoder struct {
	w             *bufio.Writer
	opts          Options
	engine        dbadmin.EngineKind
	schema, table string
	columns       []string
	rowCount      int64
	headerWritten bool
	closed        bool
	// truncated, when set via MarkTruncated, causes Close to emit the
	// "-- truncated at N rows" comment BEFORE the "-- end" marker so a
	// downstream reader scanning for "-- end" can rely on it as the
	// terminal token (C10, PR #16.5).
	truncated bool
}

// MarkTruncated records that the underlying source was capped before
// EOF. Close emits the "-- truncated at N rows" line ahead of the
// "-- end: N rows" line (C10, PR #16.5).
func (e *sqlEncoder) MarkTruncated() { e.truncated = true }

func newSQLEncoder(w io.Writer, opts Options) *sqlEncoder {
	return &sqlEncoder{
		w:      bufio.NewWriter(w),
		opts:   opts,
		engine: opts.Engine,
		schema: opts.SchemaName,
		table:  opts.TableName,
	}
}

func (e *sqlEncoder) WriteHeader(columns []string) error {
	if e.closed {
		return fmt.Errorf("export/sql: encoder closed")
	}
	if e.headerWritten {
		return fmt.Errorf("export/sql: header already written")
	}
	// MongoDB cannot replay SQL INSERT statements; refuse the dump
	// path entirely here. The HTTP layer surfaces the error and the
	// UI hides the "SQL dump" export option for Mongo connections
	// (operators get JSON / CSV via the structured row exporter
	// instead). v0.3.2-F.
	if e.engine == dbadmin.EngineMongo {
		return fmt.Errorf("export/sql: SQL dump format is not supported for MongoDB connections; use JSON or CSV export")
	}
	e.columns = append(e.columns[:0], columns...)
	e.headerWritten = true

	// Header comment block.
	now := "unknown"
	if e.opts.NowFunc != nil {
		now = e.opts.NowFunc()
	} else {
		now = time.Now().UTC().Format(time.RFC3339)
	}
	if _, err := fmt.Fprintf(e.w, "-- Aura DB export\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(e.w, "-- generated: %s\n", now); err != nil {
		return err
	}
	if e.opts.ConnectionName != "" {
		if _, err := fmt.Fprintf(e.w, "-- connection: %s (%s)\n", e.opts.ConnectionName, e.engine.String()); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(e.w, "-- engine: %s\n", e.engine.String()); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(e.w, "-- table: %s.%s\n\n", e.schema, e.table); err != nil {
		return err
	}
	// MariaDB portability: disable backslash escapes so our '...' quoting
	// rules are unambiguous across SQL_MODE settings.
	if e.engine == dbadmin.EngineMariaDB {
		if _, err := e.w.WriteString("SET sql_mode = CONCAT(@@sql_mode,',NO_BACKSLASH_ESCAPES');\n\n"); err != nil {
			return err
		}
	}
	// C8 (PR #16.5): Postgres portability — modern Postgres defaults to
	// standard_conforming_strings = on, but a legacy 9.0-era replay
	// target would mis-interpret backslash escapes inside our '...'
	// literals (\x bytea casts in particular). Emit the pragma so the
	// dump is unambiguous regardless of the target's session default.
	if e.engine == dbadmin.EnginePostgres {
		if _, err := e.w.WriteString("SET standard_conforming_strings = on;\n\n"); err != nil {
			return err
		}
	}
	return nil
}

func (e *sqlEncoder) WriteRow(values []any) error {
	if e.closed {
		return fmt.Errorf("export/sql: encoder closed")
	}
	if !e.headerWritten {
		return fmt.Errorf("export/sql: WriteHeader must precede WriteRow")
	}
	if len(values) != len(e.columns) {
		return fmt.Errorf("export/sql: row arity %d != header arity %d", len(values), len(e.columns))
	}
	if _, err := e.w.WriteString("INSERT INTO "); err != nil {
		return err
	}
	if _, err := e.w.WriteString(quoteSQLIdent(e.schema, e.engine)); err != nil {
		return err
	}
	if err := e.w.WriteByte('.'); err != nil {
		return err
	}
	if _, err := e.w.WriteString(quoteSQLIdent(e.table, e.engine)); err != nil {
		return err
	}
	if _, err := e.w.WriteString(" ("); err != nil {
		return err
	}
	for i, c := range e.columns {
		if i > 0 {
			if _, err := e.w.WriteString(", "); err != nil {
				return err
			}
		}
		if _, err := e.w.WriteString(quoteSQLIdent(c, e.engine)); err != nil {
			return err
		}
	}
	if _, err := e.w.WriteString(") VALUES ("); err != nil {
		return err
	}
	for i, v := range values {
		if i > 0 {
			if _, err := e.w.WriteString(", "); err != nil {
				return err
			}
		}
		if _, err := e.w.WriteString(sqlLiteral(v, e.engine)); err != nil {
			return err
		}
	}
	if _, err := e.w.WriteString(");\n"); err != nil {
		return err
	}
	e.rowCount++
	return nil
}

// Flush pushes any buffered INSERTs out through bufio.Writer. Used by
// the streaming export handler to feed http.Flusher mid-stream so the
// browser receives bytes incrementally.
func (e *sqlEncoder) Flush() error {
	if e.closed {
		return nil
	}
	if !e.headerWritten {
		return nil
	}
	return e.w.Flush()
}

func (e *sqlEncoder) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	if e.headerWritten {
		// C10 (PR #16.5): emit the "-- truncated" marker BEFORE the
		// terminal "-- end" line so consumers can rely on "-- end" as
		// the final token of a complete dump.
		if e.truncated {
			if _, err := fmt.Fprintf(e.w, "\n-- truncated at %d rows\n", e.rowCount); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(e.w, "-- end: %d rows\n", e.rowCount); err != nil {
				return err
			}
		} else {
			if _, err := fmt.Fprintf(e.w, "\n-- end: %d rows\n", e.rowCount); err != nil {
				return err
			}
		}
	}
	return e.w.Flush()
}

// quoteSQLIdent quotes an identifier engine-appropriately. The
// identifier MUST have been validated by schema.ValidateIdentifier
// upstream; this function relies on that and does NOT defend against
// embedded quote characters in the unvalidated case.
func quoteSQLIdent(name string, engine dbadmin.EngineKind) string {
	switch engine {
	case dbadmin.EngineMariaDB:
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	case dbadmin.EnginePostgres:
		return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	default:
		return name
	}
}

// sqlLiteral renders one cell value as a SQL literal.
func sqlLiteral(v any, engine dbadmin.EngineKind) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case bool:
		if engine == dbadmin.EnginePostgres {
			if x {
				return "TRUE"
			}
			return "FALSE"
		}
		if x {
			return "1"
		}
		return "0"
	case string:
		return quoteSQLString(x)
	case int:
		return strconv.FormatInt(int64(x), 10)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	case int64:
		return strconv.FormatInt(x, 10)
	case uint:
		return strconv.FormatUint(uint64(x), 10)
	case uint32:
		return strconv.FormatUint(uint64(x), 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case float32:
		f := float64(x)
		if !isFiniteFloat(f) {
			return "NULL"
		}
		return strconv.FormatFloat(f, 'g', -1, 32)
	case float64:
		if !isFiniteFloat(x) {
			return "NULL"
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	case []byte:
		if engine == dbadmin.EnginePostgres {
			return "'\\x" + hex.EncodeToString(x) + "'::bytea"
		}
		return "X'" + hex.EncodeToString(x) + "'"
	case time.Time:
		return quoteSQLString(x.UTC().Format(time.RFC3339Nano))
	default:
		return quoteSQLString(fmt.Sprintf("%v", v))
	}
}

// quoteSQLString wraps a string in single-quotes, escaping embedded
// single quotes by doubling them. ANSI mode; no backslash escapes —
// the MariaDB pragma in the header disables backslash interpretation.
func quoteSQLString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

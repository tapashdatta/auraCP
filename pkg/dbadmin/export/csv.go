package export

import (
	"encoding/base64"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"strconv"
	"time"
)

// csvEncoder writes RFC-4180 CSV: comma separator, CRLF line ends,
// double-quoted fields when needed. Header row optional via
// Options.IncludeHeader (default true).
type csvEncoder struct {
	w             *csv.Writer
	opts          Options
	columns       []string
	headerWritten bool
	closed        bool
}

// newCSVEncoder constructs a csvEncoder. opts.IncludeHeader controls
// whether WriteHeader emits a header row (default true; the caller sets
// it explicitly today).
//
// C13 (PR #16.5): the previous dead `if !opts.IncludeHeader {}` branch
// was a leftover from earlier plumbing; the IncludeHeader gate now lives
// in WriteHeader itself.
func newCSVEncoder(w io.Writer, opts Options) *csvEncoder {
	cw := csv.NewWriter(w)
	cw.UseCRLF = true
	return &csvEncoder{w: cw, opts: opts}
}

func (e *csvEncoder) WriteHeader(columns []string) error {
	if e.closed {
		return fmt.Errorf("export/csv: encoder closed")
	}
	if e.headerWritten {
		return fmt.Errorf("export/csv: header already written")
	}
	e.columns = append(e.columns[:0], columns...)
	e.headerWritten = true
	if e.opts.IncludeHeader {
		if err := e.w.Write(columns); err != nil {
			return err
		}
	}
	return nil
}

func (e *csvEncoder) WriteRow(values []any) error {
	if e.closed {
		return fmt.Errorf("export/csv: encoder closed")
	}
	if !e.headerWritten {
		return fmt.Errorf("export/csv: WriteHeader must precede WriteRow")
	}
	if len(values) != len(e.columns) {
		return fmt.Errorf("export/csv: row arity %d != header arity %d", len(values), len(e.columns))
	}
	cells := make([]string, len(values))
	for i, v := range values {
		cells[i] = csvCell(v)
	}
	return e.w.Write(cells)
}

// Flush pushes any buffered rows out through csv.Writer to the
// underlying writer. Used by the streaming export handler to feed
// http.Flusher mid-stream. After Flush, csv.Writer.Error() captures
// any pending write error.
func (e *csvEncoder) Flush() error {
	if e.closed {
		return nil
	}
	if !e.headerWritten {
		return nil
	}
	e.w.Flush()
	return e.w.Error()
}

func (e *csvEncoder) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	e.w.Flush()
	return e.w.Error()
}

// csvCell formats one cell per the type-mapping rules. NULL → empty
// string (the csv.Writer never quotes it).
//
// SEC-1 (PR #16): CSV formula injection defence. Spreadsheet applications
// (Excel / Google Sheets / Numbers) interpret a cell whose stringified
// value begins with one of `= + - @ \t \r` as a formula. An attacker who
// can land a controlled string into a database column can therefore
// trigger arbitrary computation / data exfiltration when the export is
// opened. Per OWASP guidance we prefix any such cell with a single
// apostrophe unconditionally — the apostrophe is stripped by spreadsheet
// apps on display but neutralizes formula interpretation.
func csvCell(v any) string {
	return csvSanitizeFormula(csvCellRaw(v))
}

// csvCellRaw is the type-mapping side of csvCell, without the
// formula-injection sanitizer. Split out so the sanitizer is unit-
// testable in isolation.
func csvCellRaw(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case bool:
		if x {
			return "true"
		}
		return "false"
	case string:
		return x
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
		return csvFloat(float64(x), 32)
	case float64:
		return csvFloat(x, 64)
	case []byte:
		return base64.StdEncoding.EncodeToString(x)
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// csvFloat formats a float for CSV output.
//
// C6 (PR #16.5): NaN / +Inf / -Inf are not valid CSV cell values — most
// spreadsheet tools either render them literally as the meaningless
// strings "NaN" / "+Inf" or refuse to import the cell. The export
// convention is:
//   - NaN → empty cell (matches NULL handling).
//   - +Inf / -Inf → the quoted string "Infinity" / "-Infinity" so
//     readers see a value rather than a missing cell, while staying
//     within the RFC 4180 "any text" cell contract.
//
// Finite floats fall through to FormatFloat with 'g' precision -1 to
// keep round-trip identity with the driver-returned value.
func csvFloat(f float64, bits int) string {
	if math.IsNaN(f) {
		return ""
	}
	if math.IsInf(f, 1) {
		return "Infinity"
	}
	if math.IsInf(f, -1) {
		return "-Infinity"
	}
	return strconv.FormatFloat(f, 'g', -1, bits)
}

// csvSanitizeFormula prefixes cells that begin with a formula-trigger
// character with a single apostrophe. Numeric stringifications begin
// with digits (or a minus sign for negatives) — the minus-sign case is
// intentionally covered because the OWASP guidance is unconditional:
// the apostrophe-prefixed minus still parses as a number in downstream
// pipelines, and the trade-off avoids round-tripping risk if an
// attacker-controlled string starts with `-`.
func csvSanitizeFormula(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

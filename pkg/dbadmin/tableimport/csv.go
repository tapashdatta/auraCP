package tableimport

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
)

// csvDecoder wraps encoding/csv.Reader and streams one row at a time
// off the underlying io.Reader.
//
// RFC 4180 compliance is delegated to encoding/csv (which is permissive
// about LF vs CR-LF and quoted embedded delimiters). The reader is
// configured FieldsPerRecord=-1 — we enforce arity in ReadRow against
// the header so a 5-cell row in a 4-column file surfaces as a typed
// arityError instead of csv.ErrFieldCount mid-pull (which gives a
// less actionable line number).
type csvDecoder struct {
	r            *csv.Reader
	opts         Options
	columns      []string
	rowIndex     int64 // 1-based data row counter (excludes header)
	headerRead   bool
	closed       bool
}

func newCSVDecoder(r io.Reader, opts Options) *csvDecoder {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1 // we enforce arity in ReadRow
	cr.ReuseRecord = false  // we map per-row; the buffer escape is fine
	cr.LazyQuotes = false   // strict — bad quoting fails the row
	return &csvDecoder{r: cr, opts: opts}
}

func (d *csvDecoder) ReadHeader() ([]string, error) {
	if d.closed {
		return nil, fmt.Errorf("tableimport/csv: decoder closed")
	}
	if d.headerRead {
		return nil, fmt.Errorf("tableimport/csv: header already read")
	}
	d.headerRead = true
	rec, err := d.r.Read()
	if err != nil {
		// Zero-byte input → io.EOF. Surface verbatim so the handler
		// can distinguish "empty upload" from "first row was malformed".
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		return nil, fmt.Errorf("tableimport/csv: read header: %w", err)
	}
	if len(rec) == 0 {
		return nil, fmt.Errorf("tableimport/csv: empty header row")
	}
	for i, c := range rec {
		// Strip BOM on first cell — Excel-saved CSV starts with U+FEFF.
		if i == 0 && len(c) >= 3 && c[0] == 0xEF && c[1] == 0xBB && c[2] == 0xBF {
			c = c[3:]
		}
		if c == "" {
			return nil, fmt.Errorf("tableimport/csv: empty header cell at index %d", i)
		}
		rec[i] = c
	}
	d.columns = append(d.columns[:0], rec...)
	return d.columns, nil
}

func (d *csvDecoder) ReadRow() (map[string]any, error) {
	if d.closed {
		return nil, fmt.Errorf("tableimport/csv: decoder closed")
	}
	if !d.headerRead {
		return nil, fmt.Errorf("tableimport/csv: ReadHeader must precede ReadRow")
	}
	rec, err := d.r.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		return nil, fmt.Errorf("tableimport/csv: read row %d: %w", d.rowIndex+1, err)
	}
	d.rowIndex++
	if len(rec) != len(d.columns) {
		return nil, fmt.Errorf("tableimport/csv: row %d arity %d != header arity %d",
			d.rowIndex, len(rec), len(d.columns))
	}
	out := make(map[string]any, len(d.columns))
	for i, col := range d.columns {
		cell := rec[i]
		if len(cell) > d.opts.MaxCellBytes {
			return nil, fmt.Errorf("tableimport/csv: row %d cell %q exceeds max %d bytes",
				d.rowIndex, col, d.opts.MaxCellBytes)
		}
		// SEC-1 (symmetric inverse): export/csv.go prefixes cells whose
		// first byte is a formula trigger (= + - @ \t \r) with a single
		// apostrophe. Round-trip identity for export→import is preserved
		// by stripping the apostrophe when it precedes one of those
		// triggers. Non-prefixed apostrophes pass through unchanged so
		// a legitimate cell value like "'twas" stays intact.
		if len(cell) >= 2 && cell[0] == '\'' {
			switch cell[1] {
			case '=', '+', '-', '@', '\t', '\r':
				cell = cell[1:]
			}
		}
		// Empty cell → nil (NULL). CSV cannot distinguish an explicit
		// empty string from an absent value; the convention here matches
		// the export-side which renders nil as "".
		if cell == "" {
			out[col] = nil
			continue
		}
		out[col] = cell
	}
	return out, nil
}

func (d *csvDecoder) Close() error {
	d.closed = true
	return nil
}

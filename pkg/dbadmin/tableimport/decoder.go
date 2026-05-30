package tableimport

import (
	"fmt"
	"io"
	"strings"
)

// Format identifies an import serialization. Mirrors export.Format but
// SQL is intentionally absent — see package doc.
type Format string

const (
	FormatCSV    Format = "csv"
	FormatNDJSON Format = "ndjson"
)

// IsValid reports whether the format is one this package can decode.
// CSV + NDJSON are accepted; "sql" is rejected (security boundary —
// arbitrary statement execution belongs to the SQL editor path).
func (f Format) IsValid() bool {
	switch f {
	case FormatCSV, FormatNDJSON:
		return true
	}
	return false
}

// FormatFromString normalises and validates a wire-format token. Empty
// input + unknown tokens both return ("", false) so the handler can
// return a single "format must be csv or ndjson" error.
func FormatFromString(s string) (Format, bool) {
	f := Format(strings.ToLower(strings.TrimSpace(s)))
	if !f.IsValid() {
		return "", false
	}
	return f, true
}

// Decoder is the streaming pull-side interface — symmetric with
// export.Encoder's WriteHeader / WriteRow / Close.
//
// Lifecycle:
//
//  1. ReadHeader is called exactly once before any ReadRow. It returns
//     the column names defined by the first record (CSV header line /
//     first NDJSON object's keys). Empty header is a hard error.
//  2. ReadRow is called repeatedly until it returns io.EOF. Each call
//     returns one row as a column-keyed map; the value types are the
//     "natural" Go shapes the JSON / CSV decoders produce. The handler
//     hands these into rows.Insert which performs the per-cell size cap
//     + driver-side type coercion.
//  3. Close releases any buffered state. Safe to call multiple times.
//
// Implementations are NOT safe for concurrent use.
type Decoder interface {
	// ReadHeader returns the column ordering for subsequent rows. For
	// NDJSON it captures the first row's keys (sparse-key handling on
	// subsequent rows is documented in the package doc). Returns
	// io.EOF on a zero-byte input.
	ReadHeader() ([]string, error)

	// ReadRow returns the next row as {column: value}. Returns
	// io.EOF on clean termination. Per-row decode errors (arity
	// mismatch, malformed JSON, ...) are surfaced as non-EOF errors;
	// the caller decides whether to stop or skip the row.
	ReadRow() (map[string]any, error)

	// Close releases any buffered state. Idempotent.
	Close() error
}

// Options carries per-format knobs.
type Options struct {
	// MaxCellBytes caps the byte length of a single decoded cell value.
	// Zero falls back to defaultMaxCellBytes. The rows.Insert call
	// further caps the encoded byte size via Operator.maxValueBytes —
	// this guard exists so a single malformed CSV cell that contains
	// embedded quotes cannot allocate hundreds of MiB before reaching
	// the rows layer.
	MaxCellBytes int

	// HasHeader (CSV-only) tells the decoder whether the first row is
	// a column-name header (true, default) or a data row (false). When
	// false, ReadHeader synthesises column names "c0", "c1", ... by
	// peeking the first row's arity — but the import handler rejects
	// the request before getting that far because it requires named
	// columns to drive rows.Insert.
	HasHeader bool
}

// defaultMaxCellBytes is the package-default cap on a single cell value.
// 1 MiB matches rows.defaultMaxValueBytes so the cell-level reject
// happens at roughly the same shape as the per-value reject downstream.
const defaultMaxCellBytes = 1 << 20

// NewDecoder constructs a Decoder for the given format. The caller
// retains ownership of r; the decoder will NOT close it.
func NewDecoder(r io.Reader, format Format, opts Options) (Decoder, error) {
	if r == nil {
		return nil, fmt.Errorf("tableimport: nil reader")
	}
	if opts.MaxCellBytes <= 0 {
		opts.MaxCellBytes = defaultMaxCellBytes
	}
	switch format {
	case FormatCSV:
		return newCSVDecoder(r, opts), nil
	case FormatNDJSON:
		return newNDJSONDecoder(r, opts), nil
	default:
		return nil, fmt.Errorf("tableimport: unknown format %q (use csv or ndjson)", format)
	}
}

// OnConflict enumerates the strategies the import handler may apply to
// a row whose primary key already exists in the target table.
//
//   - OnConflictError: surface a per-row "duplicate key" error; stop
//     processing the file when the handler is configured to fail-fast,
//     or accumulate + skip the offending row when it is configured to
//     continue. This is the default.
//   - OnConflictSkip: silently skip the row + bump the "skipped"
//     counter on the response.
//   - OnConflictUpdate: run rows.UpdateByPK with the row's non-PK
//     columns as the Set map. The schema reader must declare a primary
//     key on the target table; otherwise the request is rejected at
//     handler entry.
type OnConflict string

const (
	OnConflictError  OnConflict = "error"
	OnConflictSkip   OnConflict = "skip"
	OnConflictUpdate OnConflict = "update"
)

// IsValid reports whether the strategy is one this package recognises.
func (c OnConflict) IsValid() bool {
	switch c {
	case OnConflictError, OnConflictSkip, OnConflictUpdate:
		return true
	}
	return false
}

// OnConflictFromString normalises + validates a wire-strategy token.
// Empty input maps to OnConflictError (the safe default).
func OnConflictFromString(s string) (OnConflict, bool) {
	if s == "" {
		return OnConflictError, true
	}
	c := OnConflict(strings.ToLower(strings.TrimSpace(s)))
	if !c.IsValid() {
		return "", false
	}
	return c, true
}

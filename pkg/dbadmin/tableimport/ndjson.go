package tableimport

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ndjsonDecoder reads one JSON object per LF-delimited line. Empty
// lines are skipped. The decoder buffers a single line at a time via
// bufio.Reader; it never holds more than one row + the line scanner's
// internal slice in memory.
//
// JSON-number policy: json.Decoder defaults to float64 for every JSON
// number. We promote the unmarshal to json.Number when the column type
// is unknown (it is, at decode time), then attempt int64 first + fall
// back to float64. The downstream rows.Insert binds the resulting
// concrete Go type as a driver parameter; the driver coerces.
type ndjsonDecoder struct {
	br         *bufio.Reader
	opts       Options
	columns    []string
	rowIndex   int64 // 1-based; header counts as row 0
	headerRead bool
	closed     bool
	// peekRow holds the first decoded row when ReadHeader consumed it
	// to discover the column set. ReadRow returns this row first.
	peekRow map[string]any
}

func newNDJSONDecoder(r io.Reader, opts Options) *ndjsonDecoder {
	// The MaxCellBytes guard caps a single CELL; a single LINE may be
	// much larger (an object with N columns each near the cap). We
	// size the bufio reader generously and rely on per-cell checks
	// after JSON decoding for the real ceiling.
	return &ndjsonDecoder{br: bufio.NewReaderSize(r, 64<<10), opts: opts}
}

// ReadHeader decodes the FIRST non-empty line + extracts its keys as
// the column ordering. The decoded row is stashed so ReadRow returns
// it on the next call rather than re-parsing the line.
//
// Key ordering follows insertion order from the source JSON. Go's
// encoding/json does not preserve key order across map[string]any, so
// we re-tokenise the line via json.Decoder to recover the ordering.
func (d *ndjsonDecoder) ReadHeader() ([]string, error) {
	if d.closed {
		return nil, fmt.Errorf("tableimport/ndjson: decoder closed")
	}
	if d.headerRead {
		return nil, fmt.Errorf("tableimport/ndjson: header already read")
	}
	d.headerRead = true

	line, err := d.readLine()
	if err != nil {
		return nil, err
	}

	cols, err := orderedKeys(line)
	if err != nil {
		return nil, fmt.Errorf("tableimport/ndjson: read header: %w", err)
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("tableimport/ndjson: first row has zero columns")
	}
	d.columns = cols
	row, err := d.decodeRow(line)
	if err != nil {
		return nil, fmt.Errorf("tableimport/ndjson: read header row: %w", err)
	}
	d.peekRow = row
	return d.columns, nil
}

func (d *ndjsonDecoder) ReadRow() (map[string]any, error) {
	if d.closed {
		return nil, fmt.Errorf("tableimport/ndjson: decoder closed")
	}
	if !d.headerRead {
		return nil, fmt.Errorf("tableimport/ndjson: ReadHeader must precede ReadRow")
	}
	if d.peekRow != nil {
		row := d.peekRow
		d.peekRow = nil
		d.rowIndex++
		return row, nil
	}
	line, err := d.readLine()
	if err != nil {
		return nil, err
	}
	d.rowIndex++
	row, err := d.decodeRow(line)
	if err != nil {
		return nil, fmt.Errorf("tableimport/ndjson: row %d: %w", d.rowIndex, err)
	}
	return row, nil
}

func (d *ndjsonDecoder) Close() error {
	d.closed = true
	d.peekRow = nil
	return nil
}

// readLine returns the next non-empty trimmed line. io.EOF on clean
// end-of-stream (matches Decoder contract).
func (d *ndjsonDecoder) readLine() ([]byte, error) {
	for {
		line, err := d.br.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			line = bytes.TrimRight(line, "\r\n")
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				return nil, io.EOF
			}
			return line, nil
		}
		if err != nil {
			return nil, err
		}
		line = bytes.TrimRight(line, "\r\n")
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		// Strip UTF-8 BOM on the first line — text editors save NDJSON
		// with one occasionally.
		if !d.headerReadStarted() && len(line) >= 3 && line[0] == 0xEF && line[1] == 0xBB && line[2] == 0xBF {
			line = line[3:]
		}
		return line, nil
	}
}

// headerReadStarted reports whether ReadHeader has consumed any input.
// True after we've successfully decoded the first line; used by readLine
// to gate BOM stripping to the first line only.
func (d *ndjsonDecoder) headerReadStarted() bool {
	return d.columns != nil
}

// decodeRow parses the line into a {column: value} map applying the
// per-cell byte cap. The token stream is walked via json.Decoder with
// UseNumber so JSON numbers round-trip to int64 when integral and fit
// signed 64-bit; otherwise to float64.
func (d *ndjsonDecoder) decodeRow(line []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		nv, err := normalizeNDJSONValue(v, d.opts.MaxCellBytes, k)
		if err != nil {
			return nil, err
		}
		out[k] = nv
	}
	return out, nil
}

// orderedKeys walks the JSON tokens of an object literal + returns its
// keys in source order. json.Unmarshal to map[string]any loses order;
// we re-tokenise to recover it. Only the OUTER object is walked;
// nested arrays / objects are skipped past via depth tracking.
func orderedKeys(line []byte) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(line))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	d, ok := tok.(json.Delim)
	if !ok || d != '{' {
		return nil, fmt.Errorf("expected JSON object, got %v", tok)
	}
	var keys []string
	depth := 0
	expectKey := true
	for dec.More() || depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{', '[':
				depth++
				expectKey = false
			case '}', ']':
				if depth == 0 {
					return keys, nil
				}
				depth--
				expectKey = depth == 0
			}
		default:
			if depth == 0 && expectKey {
				if s, ok := t.(string); ok {
					keys = append(keys, s)
				}
				expectKey = false
			} else if depth == 0 {
				expectKey = true
			}
		}
	}
	return keys, nil
}

// normalizeNDJSONValue walks one decoded value and (a) promotes
// json.Number to int64 / float64, (b) enforces MaxCellBytes on the
// stringified form of a leaf, (c) leaves nested objects / arrays as-is
// (the rows package will reject them unless the driver accepts the
// shape).
func normalizeNDJSONValue(v any, maxBytes int, col string) (any, error) {
	switch x := v.(type) {
	case nil, bool:
		return x, nil
	case string:
		if maxBytes > 0 && len(x) > maxBytes {
			return nil, fmt.Errorf("cell %q exceeds max %d bytes", col, maxBytes)
		}
		return x, nil
	case json.Number:
		// Prefer int64 when the literal is integral. ParseInt fails on
		// "1.5" / "1e3" — those fall through to float64.
		if i, err := x.Int64(); err == nil {
			return i, nil
		}
		if f, err := x.Float64(); err == nil {
			return f, nil
		}
		// Fall back to the raw string so the driver sees the number
		// literal — better than dropping the cell.
		return string(x), nil
	case map[string]any, []any:
		// Re-encode + check byte length so a nested object cannot
		// hide unbounded growth. The downstream rows.Insert rejects
		// non-scalar values for non-JSONB columns.
		if maxBytes > 0 {
			enc, err := json.Marshal(v)
			if err != nil {
				return nil, fmt.Errorf("cell %q: re-encode: %w", col, err)
			}
			if len(enc) > maxBytes {
				return nil, fmt.Errorf("cell %q exceeds max %d bytes", col, maxBytes)
			}
		}
		return v, nil
	default:
		return v, nil
	}
}

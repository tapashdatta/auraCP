package export

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"time"
)

// ndjsonEncoder writes one JSON object per row, separated by LF. The
// object key order follows the header column ordering — we use a
// hand-rolled writer (not json.Encoder over a map) because map iteration
// would shuffle keys per-row.
type ndjsonEncoder struct {
	w             *bufio.Writer
	columns       []string
	headerWritten bool
	closed        bool
}

func newNDJSONEncoder(w io.Writer, _ Options) *ndjsonEncoder {
	return &ndjsonEncoder{w: bufio.NewWriter(w)}
}

func (e *ndjsonEncoder) WriteHeader(columns []string) error {
	if e.closed {
		return fmt.Errorf("export/ndjson: encoder closed")
	}
	if e.headerWritten {
		return fmt.Errorf("export/ndjson: header already written")
	}
	e.columns = append(e.columns[:0], columns...)
	e.headerWritten = true
	return nil
}

func (e *ndjsonEncoder) WriteRow(values []any) error {
	if e.closed {
		return fmt.Errorf("export/ndjson: encoder closed")
	}
	if !e.headerWritten {
		return fmt.Errorf("export/ndjson: WriteHeader must precede WriteRow")
	}
	if len(values) != len(e.columns) {
		return fmt.Errorf("export/ndjson: row arity %d != header arity %d", len(values), len(e.columns))
	}
	if err := e.w.WriteByte('{'); err != nil {
		return err
	}
	for i, c := range e.columns {
		if i > 0 {
			if err := e.w.WriteByte(','); err != nil {
				return err
			}
		}
		key, err := json.Marshal(c)
		if err != nil {
			return err
		}
		if _, err := e.w.Write(key); err != nil {
			return err
		}
		if err := e.w.WriteByte(':'); err != nil {
			return err
		}
		if err := writeNDJSONValue(e.w, values[i]); err != nil {
			return err
		}
	}
	if err := e.w.WriteByte('}'); err != nil {
		return err
	}
	return e.w.WriteByte('\n')
}

// Flush pushes any buffered bytes out through bufio.Writer to the
// underlying writer. Used by the streaming export handler to feed
// http.Flusher mid-stream.
func (e *ndjsonEncoder) Flush() error {
	if e.closed {
		return nil
	}
	if !e.headerWritten {
		return nil
	}
	return e.w.Flush()
}

func (e *ndjsonEncoder) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	return e.w.Flush()
}

// writeNDJSONValue serializes one cell per the type-mapping rules. The
// implementation hand-encodes the common types (no reflection) and falls
// back to json.Marshal for anything else.
func writeNDJSONValue(w *bufio.Writer, v any) error {
	switch x := v.(type) {
	case nil:
		_, err := w.WriteString("null")
		return err
	case bool:
		if x {
			_, err := w.WriteString("true")
			return err
		}
		_, err := w.WriteString("false")
		return err
	case string:
		b, err := json.Marshal(x)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	case int:
		_, err := w.WriteString(strconv.FormatInt(int64(x), 10))
		return err
	case int32:
		_, err := w.WriteString(strconv.FormatInt(int64(x), 10))
		return err
	case int64:
		_, err := w.WriteString(strconv.FormatInt(x, 10))
		return err
	case uint:
		_, err := w.WriteString(strconv.FormatUint(uint64(x), 10))
		return err
	case uint32:
		_, err := w.WriteString(strconv.FormatUint(uint64(x), 10))
		return err
	case uint64:
		_, err := w.WriteString(strconv.FormatUint(x, 10))
		return err
	case float32:
		f := float64(x)
		if !isFiniteFloat(f) {
			_, err := w.WriteString("null")
			return err
		}
		_, err := w.WriteString(strconv.FormatFloat(f, 'g', -1, 32))
		return err
	case float64:
		if !isFiniteFloat(x) {
			_, err := w.WriteString("null")
			return err
		}
		_, err := w.WriteString(strconv.FormatFloat(x, 'g', -1, 64))
		return err
	case []byte:
		// SEC-6 (PR #16.5): JSON / JSONB columns surface as []byte from
		// the MariaDB + Postgres drivers. The previous behaviour base64-
		// encoded all []byte unconditionally, which left operators
		// staring at a Base64 blob when they expected the nested object.
		// Heuristic: if the byte slice is valid JSON (starts with a
		// JSON-structural byte and parses cleanly with json.Valid) we
		// emit it as-is so jq / NDJSON consumers see the nested value.
		// Binary payloads (images, BSON, packed-row blobs) do not parse
		// as JSON and fall through to the base64 path, preserving the
		// safe round-trip for non-JSON bytes.
		if looksLikeJSON(x) && json.Valid(x) {
			_, err := w.Write(x)
			return err
		}
		enc := base64.StdEncoding.EncodeToString(x)
		b, err := json.Marshal(enc)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	case json.RawMessage:
		// Driver-typed JSON column (some drivers return json.RawMessage
		// when the table column is declared JSON / JSONB). Pass through
		// directly without re-encoding so jq sees the nested object.
		if len(x) == 0 {
			_, err := w.WriteString("null")
			return err
		}
		_, err := w.Write(x)
		return err
	case time.Time:
		b, err := json.Marshal(x.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	default:
		b, err := json.Marshal(v)
		if err != nil {
			// Best-effort fallback: stringify.
			b, _ = json.Marshal(fmt.Sprintf("%v", v))
		}
		_, err = w.Write(b)
		return err
	}
}

// isFiniteFloat reports whether f is a finite (non-NaN, non-Inf) float.
func isFiniteFloat(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}

// looksLikeJSON reports whether the byte slice's first non-whitespace
// rune is a JSON-structural byte ({, [, ", t, f, n, -, 0..9). Cheaper
// than json.Valid for the common case where the answer is "no"; we use
// it as a guard before paying the json.Valid scan (SEC-6, PR #16.5).
func looksLikeJSON(b []byte) bool {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		case '{', '[', '"', 't', 'f', 'n', '-',
			'0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
			return true
		default:
			return false
		}
	}
	return false
}

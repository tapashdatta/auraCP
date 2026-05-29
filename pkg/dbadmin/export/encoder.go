package export

import (
	"fmt"
	"io"
	"strings"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// Format identifies an output serialization.
type Format string

const (
	FormatCSV    Format = "csv"
	FormatNDJSON Format = "ndjson"
	FormatSQL    Format = "sql"
)

// IsValid reports whether the format is recognized.
func (f Format) IsValid() bool {
	switch f {
	case FormatCSV, FormatNDJSON, FormatSQL:
		return true
	}
	return false
}

// ContentType returns the canonical MIME type for the format.
func (f Format) ContentType() string {
	switch f {
	case FormatCSV:
		return "text/csv; charset=utf-8"
	case FormatNDJSON:
		return "application/x-ndjson"
	case FormatSQL:
		return "application/sql; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

// FileExt returns the canonical file extension (no dot).
func (f Format) FileExt() string {
	switch f {
	case FormatCSV:
		return "csv"
	case FormatNDJSON:
		return "ndjson"
	case FormatSQL:
		return "sql"
	default:
		return "bin"
	}
}

// Encoder is the streaming wire-format interface. WriteHeader is called
// exactly once before any WriteRow; Close is called exactly once after
// the last WriteRow (even on error paths, to release any internal
// buffer state).
type Encoder interface {
	// WriteHeader announces the column ordering for subsequent
	// WriteRow calls. For NDJSON it captures the key ordering only;
	// no bytes are written until the first row. For CSV it writes
	// the header row when includeHeader is true. For SQL it writes
	// the comment preamble + (when emitted) the engine-specific
	// pragma.
	WriteHeader(columns []string) error

	// WriteRow serializes one row. The values slice MUST be the same
	// length as the columns slice passed to WriteHeader.
	WriteRow(values []any) error

	// Flush forces any buffered bytes out to the underlying writer
	// without ending the stream. This is the encoder-side counterpart
	// to http.Flusher.Flush — until the encoder pushes bytes to the
	// writer, http.Flusher has nothing to deliver. Implementations:
	// csv.Writer.Flush for CSV; bufio.Writer.Flush for NDJSON / SQL.
	//
	// Calling Flush after Close is a no-op. Calling Flush before
	// WriteHeader is a no-op.
	Flush() error

	// Close flushes any pending buffer and writes a trailing summary
	// comment (SQL only). After Close the encoder is unusable.
	Close() error
}

// Options carries per-format knobs. Not every option applies to every
// format — fields with cross-format relevance are documented per-field.
type Options struct {
	// IncludeHeader controls whether the CSV writer emits a header row.
	// Default true. Ignored by NDJSON / SQL.
	IncludeHeader bool

	// Engine identifies the target database for SQL format quoting +
	// literal rendering. Ignored by CSV / NDJSON. Required for SQL.
	Engine dbadmin.EngineKind

	// SchemaName and TableName are used by the SQL writer to qualify
	// INSERT statements. Required for SQL.
	SchemaName string
	TableName  string

	// ConnectionName is used by the SQL writer for the header comment
	// block. Optional.
	ConnectionName string

	// Now is a clock for the SQL writer's generated-at comment.
	// Tests inject a deterministic value; production passes time.Now.
	NowFunc func() string
}

// NewEncoder constructs an Encoder for the given format. Caller
// retains ownership of w; the encoder will NOT close it.
func NewEncoder(w io.Writer, format Format, opts Options) (Encoder, error) {
	if w == nil {
		return nil, fmt.Errorf("export: nil writer")
	}
	switch format {
	case FormatCSV:
		return newCSVEncoder(w, opts), nil
	case FormatNDJSON:
		return newNDJSONEncoder(w, opts), nil
	case FormatSQL:
		if opts.Engine != dbadmin.EngineMariaDB && opts.Engine != dbadmin.EnginePostgres {
			return nil, fmt.Errorf("export: SQL format requires Engine in opts")
		}
		if opts.SchemaName == "" || opts.TableName == "" {
			return nil, fmt.Errorf("export: SQL format requires SchemaName + TableName in opts")
		}
		return newSQLEncoder(w, opts), nil
	default:
		return nil, fmt.Errorf("export: unknown format %q", format)
	}
}

// SanitizeFilename returns a safe filename component for a
// Content-Disposition header. Strips path separators + control chars +
// quotes; collapses runs of whitespace to underscores; clips length.
// Empty input returns "export".
func SanitizeFilename(name string) string {
	const maxLen = 200
	var b strings.Builder
	b.Grow(len(name))
	prevSpace := false
	for _, r := range name {
		switch {
		case r == '/' || r == '\\' || r == 0 || r == '"' || r == '\'':
			// drop
		case r < 0x20:
			// drop control chars
		case r == ' ' || r == '\t':
			if !prevSpace {
				b.WriteByte('_')
				prevSpace = true
			}
		default:
			b.WriteRune(r)
			prevSpace = false
		}
		if b.Len() >= maxLen {
			break
		}
	}
	out := strings.Trim(b.String(), "._-")
	if out == "" {
		return "export"
	}
	return out
}

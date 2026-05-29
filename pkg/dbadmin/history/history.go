package history

import (
	"context"
	"errors"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/classifier"
)

// Entry is one recorded query attempt. Stored as one row.
//
// Field-level notes:
//   - SQL is the operator's raw SQL with credentials redacted (see
//     classifier.RedactSensitiveInline). The Store applies the
//     redaction; callers pass the verbatim SQL and trust the Store.
//   - DurationMS is end-to-end wall-clock time from when the
//     classifier returned to when the driver's Rows.Close completed.
//   - RowsReturned is non-zero only when the statement actually
//     produced a result set; UPDATEs / DELETEs / DDL leave it 0.
//   - Error is empty on success. For failed queries Class is still
//     populated (the classifier ran before the driver did) but
//     RowsReturned and DurationMS reflect the partial work. Error
//     text is run through the same redactor as SQL — driver errors
//     routinely echo the failing statement.
//   - Tags and Starred are operator-mutable post-write via Tag() and
//     Star(); Append() seeds them empty / false.
//   - Engine selects the dialect used for redaction. When zero
//     (EngineUnknown) the Store's defaultEngine is used; when both
//     are zero Append returns ErrInvalidInput.
//
// JSON wire format is lowerCamelCase per the project HTTP convention
// (matches what explain.go fixed in PR #6).
type Entry struct {
	ID           int64                 `json:"id"`
	UserID       string                `json:"userId"`
	ConnectionID dbadmin.ConnectionID  `json:"connectionId"`
	SQL          string                `json:"sql"`
	Class        classifier.QueryClass `json:"class"`
	Tags         []string              `json:"tags"`
	Starred      bool                  `json:"starred"`
	DurationMS   int64                 `json:"durationMs"`
	RowsReturned int64                 `json:"rowsReturned"`
	Error        string                `json:"error"`
	Executed     time.Time             `json:"executed"`
	Engine       dbadmin.EngineKind    `json:"engine"`
}

// ListOpts paginates / filters a List or Search call.
type ListOpts struct {
	// UserID scopes the result to one operator. Required: empty
	// UserID is rejected by every Store operation. The engine layer
	// must populate this with the calling operator's ID before
	// invoking the Store.
	UserID string `json:"userId"`

	// ConnectionID, when non-empty, returns only entries against
	// this connection.
	ConnectionID dbadmin.ConnectionID `json:"connectionId"`

	// OnlyStarred filters to starred entries.
	OnlyStarred bool `json:"onlyStarred"`

	// Tag, when non-empty, filters to entries carrying this tag.
	Tag string `json:"tag"`

	// Class, when non-zero, filters to a specific query class.
	// Zero (ClassRead) is a valid filter too; callers use
	// IncludeClass to disambiguate.
	Class        classifier.QueryClass `json:"class"`
	IncludeClass bool                  `json:"includeClass"`

	// Since / Until bound the Executed timestamp. Zero values mean
	// "open-ended on that side."
	Since time.Time `json:"since"`
	Until time.Time `json:"until"`

	// Limit caps result rows. Defaults to 100 when <= 0.
	// Hard ceiling: 10,000.
	Limit int `json:"limit"`

	// Offset for pagination.
	Offset int `json:"offset"`
}

// SearchResult adds a relevance score for FTS5-backed search; degrades
// to constant 1.0 for LIKE fallback.
type SearchResult struct {
	Entry
	Score float64 `json:"score"`
}

// Store is the persistence interface.
type Store interface {
	// Append records one query. Returns the assigned ID. SQL +
	// Error are redacted before storage; the returned ID is
	// monotonic and safe to use as a cursor.
	Append(ctx context.Context, e Entry) (int64, error)

	// Get fetches one entry by ID. Returns ErrNotFound if absent
	// or if it belongs to a different user. userID is required;
	// empty values return ErrInvalidInput (default-deny on scope).
	Get(ctx context.Context, id int64, userID string) (*Entry, error)

	// List returns entries matching ListOpts, ordered by Executed
	// DESC. Pagination via Limit + Offset. opts.UserID is required.
	List(ctx context.Context, opts ListOpts) ([]Entry, error)

	// Search runs an FTS5-or-LIKE search across SQL text and tags.
	// Same ListOpts pagination. opts.UserID is required.
	Search(ctx context.Context, query string, opts ListOpts) ([]SearchResult, error)

	// Star pins / unpins an entry. userID is required.
	Star(ctx context.Context, id int64, userID string, starred bool) error

	// Tag replaces the tag set on an entry. userID is required.
	// Tag values containing commas are rejected with ErrInvalidInput
	// (commas are the on-disk separator).
	Tag(ctx context.Context, id int64, userID string, tags []string) error

	// Delete removes one entry. userID is required.
	Delete(ctx context.Context, id int64, userID string) error

	// DeleteOlderThan removes every entry whose Executed is strictly
	// before the cutoff. Returns the count removed. Engine layer
	// calls this from a periodic goroutine; admin-scoped so no
	// userID arg.
	DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error)

	// Close releases any underlying resources (SQLite handle, etc.).
	// Idempotent.
	Close() error
}

// ─── Errors ──────────────────────────────────────────────────────────

var (
	// ErrNotFound is returned by Get/Star/Tag/Delete when the
	// addressed entry doesn't exist (or belongs to a different
	// user when userID is non-empty).
	ErrNotFound = errors.New("history: not found")

	// ErrInvalidInput is returned on malformed input (negative
	// Limit, empty SQL, etc.).
	ErrInvalidInput = errors.New("history: invalid input")

	// ErrClosed is returned by every operation after Close.
	ErrClosed = errors.New("history: store closed")
)

// ─── Defaults ────────────────────────────────────────────────────────

const (
	// DefaultListLimit is the row cap when ListOpts.Limit is <= 0.
	DefaultListLimit = 100

	// MaxListLimit is the hard ceiling on ListOpts.Limit. Larger
	// requests get clamped down — the caller still gets a partial
	// result; we don't error.
	MaxListLimit = 10_000

	// MaxSQLLength caps the byte size of stored SQL. Anything longer
	// is truncated with a "...[truncated]" suffix. Prevents pasted-
	// novel queries from bloating the store.
	MaxSQLLength = 256 * 1024
)

// ─── Helpers ─────────────────────────────────────────────────────────

// clampLimit applies the default + ceiling.
func clampLimit(n int) int {
	if n <= 0 {
		return DefaultListLimit
	}
	if n > MaxListLimit {
		return MaxListLimit
	}
	return n
}

// redactSQL applies classifier-based redaction and length truncation
// before persistence. Engine arg picks the dialect for redaction
// (different placeholder shapes; CREATE USER syntax differs).
func redactSQL(sql string, engine dbadmin.EngineKind) string {
	dialect := classifier.DialectMySQL
	if engine == dbadmin.EnginePostgres {
		dialect = classifier.DialectPostgres
	}
	out := classifier.RedactSensitiveInline(sql, dialect)
	if len(out) > MaxSQLLength {
		out = out[:MaxSQLLength] + "...[truncated]"
	}
	return out
}

// validateTags returns ErrInvalidInput if any tag contains a comma.
// Commas are the on-disk separator in the serialized tag column;
// allowing them silently splits a single tag into two.
func validateTags(tags []string) error {
	for _, t := range tags {
		for i := 0; i < len(t); i++ {
			if t[i] == ',' {
				return errors.New("history: tag must not contain comma")
			}
		}
	}
	return nil
}

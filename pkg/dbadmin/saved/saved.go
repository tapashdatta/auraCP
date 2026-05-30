package saved

import (
	"context"
	"errors"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// Record is one durable saved query. Mirrors httpapi.savedQueryDTO on the
// wire (id, name, statement, tags, createdAt) and adds fields the v0.3.2-A
// schema persists: ConnectionID (key with OwnerID), OwnerID, Description,
// Starred, UpdatedAt.
//
// Field-level notes:
//
//   - ID is opaque text supplied by the caller (the httpapi handler
//     mints it via newRequestID). The Store treats it as a primary key
//     and does not regenerate it.
//   - ConnectionID + OwnerID together scope every read/write. Empty
//     values on either are rejected with ErrInvalidInput
//     (default-deny on scope, same posture as history).
//   - Name uniqueness is enforced per (ConnectionID, OwnerID) by a
//     UNIQUE INDEX; Create returns ErrConflict on duplicate Name. The
//     httpapi "replace" path deletes the existing row first.
//   - Tags are validated to reject any tag containing "," (the
//     on-disk separator). Empty/nil is fine.
//   - CreatedAt is set by the caller; UpdatedAt is set by the Store on
//     Create and on every Update/Star/Tag.
type Record struct {
	ID           string               `json:"id"`
	ConnectionID dbadmin.ConnectionID `json:"connectionId"`
	OwnerID      string               `json:"ownerId"`
	Name         string               `json:"name"`
	Statement    string               `json:"statement"`
	Description  string               `json:"description"`
	Tags         []string             `json:"tags"`
	Starred      bool                 `json:"starred"`
	CreatedAt    time.Time            `json:"createdAt"`
	UpdatedAt    time.Time            `json:"updatedAt"`
}

// ListOpts filters / paginates List + Search.
type ListOpts struct {
	// ConnectionID scopes the result to one connection. Required:
	// empty is rejected. The Store does NOT support cross-connection
	// listing — the httpapi layer already wires one connection per
	// request.
	ConnectionID dbadmin.ConnectionID `json:"connectionId"`

	// OwnerID scopes the result to one operator. Required.
	OwnerID string `json:"ownerId"`

	// StarOnly filters to starred entries. Matches the optional
	// star_only=1 query parameter on the List handler.
	StarOnly bool `json:"starOnly"`

	// Tag, when non-empty, filters to entries carrying this tag.
	Tag string `json:"tag"`

	// Limit caps result rows. Defaults to 256 when <= 0 (matches the
	// in-memory store's per-(conn, owner) cap so the default list
	// page returns every entry the user owns). Hard ceiling: 10,000.
	Limit int `json:"limit"`

	// Offset for pagination.
	Offset int `json:"offset"`
}

// SearchResult is a Record plus the FTS5 relevance score (degrades to
// constant 1.0 for LIKE fallback).
type SearchResult struct {
	Record
	Score float64 `json:"score"`
}

// Store is the persistence interface.
type Store interface {
	// Append (Create) inserts one saved query. Returns ErrConflict
	// when a row with the same (ConnectionID, OwnerID, Name) already
	// exists. Enforces OpenOpts.MaxPerOwner: when the create would
	// push the count over the cap, the OLDEST row for the same
	// (conn, owner) is deleted in the same transaction.
	Append(ctx context.Context, r Record) error

	// Get fetches one record by ID, scoped to (connID, ownerID).
	// Returns ErrNotFound when the row doesn't exist OR belongs to
	// a different owner (existence-leak guard mirroring SEC-1).
	Get(ctx context.Context, connID dbadmin.ConnectionID, ownerID, id string) (*Record, error)

	// List returns records matching opts. Ordering: starred DESC,
	// updated_at DESC, id DESC (deterministic tiebreaker).
	List(ctx context.Context, opts ListOpts) ([]Record, error)

	// Search runs an FTS5-or-LIKE search across name + statement +
	// description + tags. Same ListOpts filters.
	Search(ctx context.Context, query string, opts ListOpts) ([]SearchResult, error)

	// Star pins / unpins an entry. ownerID is required.
	Star(ctx context.Context, connID dbadmin.ConnectionID, ownerID, id string, starred bool) error

	// Tag replaces the tag set on an entry. ownerID is required;
	// values containing commas are rejected with ErrInvalidInput.
	Tag(ctx context.Context, connID dbadmin.ConnectionID, ownerID, id string, tags []string) error

	// Update mutates Name + Statement + Description + Tags in one
	// transaction. ownerID is required; the row is updated only if
	// it belongs to ownerID.
	Update(ctx context.Context, connID dbadmin.ConnectionID, ownerID, id string, fields UpdateFields) error

	// Delete removes one entry. ownerID is required. Returns
	// (found, owned) so the httpapi layer can collapse "not found"
	// and "not owned" into a single 404 (SEC-1 existence-leak guard).
	Delete(ctx context.Context, connID dbadmin.ConnectionID, ownerID, id string) (found, owned bool, err error)

	// HasFTS reports whether Search uses FTS5 (true) or LIKE
	// fallback (false). Mirrors history.Store.HasFTS.
	HasFTS() bool

	// Close releases any underlying resources. Idempotent.
	Close() error
}

// UpdateFields holds the mutable subset of Record. A nil pointer means
// "leave this field unchanged"; an empty string / empty slice means
// "set to empty". Per-field opt-in mirrors patchHistoryRequest.
type UpdateFields struct {
	Name        *string   `json:"name,omitempty"`
	Statement   *string   `json:"statement,omitempty"`
	Description *string   `json:"description,omitempty"`
	Tags        *[]string `json:"tags,omitempty"`
}

// ─── Errors ──────────────────────────────────────────────────────────

var (
	// ErrNotFound is returned by Get/Star/Tag/Update/Delete when the
	// addressed entry doesn't exist or belongs to a different owner.
	ErrNotFound = errors.New("saved: not found")

	// ErrInvalidInput is returned on malformed input (empty
	// ConnectionID/OwnerID/Name/Statement, comma-bearing tag, etc.).
	ErrInvalidInput = errors.New("saved: invalid input")

	// ErrConflict is returned by Create when a row with the same
	// (ConnectionID, OwnerID, Name) already exists.
	ErrConflict = errors.New("saved: name already exists")

	// ErrClosed is returned by every operation after Close.
	ErrClosed = errors.New("saved: store closed")
)

// ─── Defaults ────────────────────────────────────────────────────────

const (
	// DefaultListLimit is the row cap when ListOpts.Limit is <= 0.
	// Matches the per-(conn, owner) cap so a default list returns
	// everything the user owns on that connection.
	DefaultListLimit = 256

	// MaxListLimit is the hard ceiling on ListOpts.Limit.
	MaxListLimit = 10_000

	// DefaultMaxPerOwner caps stored entries per (connection, owner).
	// Matches the in-memory store's savedQueriesPerUser constant
	// (handlers_saved.go) so the cap-and-evict behavior is preserved
	// when swapping to durable storage.
	DefaultMaxPerOwner = 256
)

func clampLimit(n int) int {
	if n <= 0 {
		return DefaultListLimit
	}
	if n > MaxListLimit {
		return MaxListLimit
	}
	return n
}

// validateTags rejects tags containing commas (on-disk separator).
func validateTags(tags []string) error {
	for _, t := range tags {
		for i := 0; i < len(t); i++ {
			if t[i] == ',' {
				return errors.New("saved: tag must not contain comma")
			}
		}
	}
	return nil
}

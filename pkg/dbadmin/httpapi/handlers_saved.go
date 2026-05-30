package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/classifier"
	"github.com/auracp/auracp/pkg/dbadmin/saved"
)

// savedStoreAdapter wraps a saved.Store so it satisfies the narrow
// savedQueryStore interface this package's handlers consume. The
// adapter keeps the wider Store surface (Update, Star, Search, Tag,
// Get, HasFTS, Close) accessible to future endpoints without making
// the handler signatures depend on every method up front.
type savedStoreAdapter struct {
	store saved.Store
}

func (a savedStoreAdapter) List(ctx context.Context, opts saved.ListOpts) ([]saved.Record, error) {
	return a.store.List(ctx, opts)
}

func (a savedStoreAdapter) Append(ctx context.Context, r saved.Record) error {
	return a.store.Append(ctx, r)
}

func (a savedStoreAdapter) Delete(ctx context.Context, connID dbadmin.ConnectionID, ownerID, id string) (found, owned bool, err error) {
	return a.store.Delete(ctx, connID, ownerID, id)
}

// savedQueryStore is the narrow capability handlers_saved.go uses. The
// real implementation is saved.Store (durable, SQLite-backed) — see
// pkg/dbadmin/saved/. The in-memory fallback below is retained for
// tests that don't want to wire a SQLite DSN, and as the zero-value
// behavior when no store is supplied to NewWithOptions.
//
// SEC-1 (collapse not-found and not-owned into a single 404) is
// enforced by callers: the durable Delete returns (found, owned, err)
// so the handler can fold them; the in-memory adapter does the same.
type savedQueryStore interface {
	List(ctx context.Context, opts saved.ListOpts) ([]saved.Record, error)
	Append(ctx context.Context, r saved.Record) error
	Delete(ctx context.Context, connID dbadmin.ConnectionID, ownerID, id string) (found, owned bool, err error)
}

// savedQueriesPerUser is the legacy in-memory cap. The durable store
// uses saved.DefaultMaxPerOwner which is the same 256.
const savedQueriesPerUser = saved.DefaultMaxPerOwner

// savedRecord is the in-memory adapter's row. Mirrors saved.Record
// minus the persistence-only fields.
type savedRecord struct {
	dto     savedQueryDTO
	ownerID string
}

// savedQueriesStore is the in-memory fallback used when no durable
// saved.Store is wired. v0.3.2-A landed the SQLite store; this remains
// for tests and for the zero-config New() entry point.
type savedQueriesStore struct {
	mu      sync.RWMutex
	queries map[string][]savedRecord
}

func newSavedQueriesStore() *savedQueriesStore {
	return &savedQueriesStore{queries: map[string][]savedRecord{}}
}

func (s *savedQueriesStore) List(ctx context.Context, opts saved.ListOpts) ([]saved.Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	conn := string(opts.ConnectionID)
	out := make([]saved.Record, 0, len(s.queries[conn]))
	for _, rec := range s.queries[conn] {
		if rec.ownerID != opts.OwnerID {
			continue
		}
		if opts.StarOnly && !rec.dto.Starred {
			continue
		}
		out = append(out, recordFromDTO(opts.ConnectionID, rec.ownerID, rec.dto))
	}
	return out, nil
}

func (s *savedQueriesStore) Append(ctx context.Context, r saved.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	conn := string(r.ConnectionID)
	list := s.queries[conn]

	// Reject duplicate (conn, owner, name) so the in-memory adapter
	// matches the durable store's ErrConflict contract.
	for _, rec := range list {
		if rec.ownerID == r.OwnerID && rec.dto.Name == r.Name {
			return saved.ErrConflict
		}
	}

	var owned int
	oldestIdx := -1
	for i, rec := range list {
		if rec.ownerID != r.OwnerID {
			continue
		}
		if oldestIdx == -1 {
			oldestIdx = i
		}
		owned++
	}
	if owned >= savedQueriesPerUser && oldestIdx >= 0 {
		list = append(list[:oldestIdx], list[oldestIdx+1:]...)
	}
	dto := dtoFromRecord(r)
	s.queries[conn] = append(list, savedRecord{dto: dto, ownerID: r.OwnerID})
	return nil
}

// Delete reports both signals so SEC-1 folding stays handler-side.
func (s *savedQueriesStore) Delete(ctx context.Context, connID dbadmin.ConnectionID, ownerID, id string) (found, owned bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conn := string(connID)
	list := s.queries[conn]
	for i, rec := range list {
		if rec.dto.ID != id {
			continue
		}
		found = true
		if rec.ownerID != ownerID {
			return true, false, nil
		}
		s.queries[conn] = append(list[:i], list[i+1:]...)
		return true, true, nil
	}
	return false, false, nil
}

// dtoFromRecord adapts a saved.Record onto the wire DTO.
func dtoFromRecord(r saved.Record) savedQueryDTO {
	tags := r.Tags
	if tags == nil {
		tags = []string{}
	}
	return savedQueryDTO{
		ID:          r.ID,
		Name:        r.Name,
		Statement:   r.Statement,
		Description: r.Description,
		Tags:        tags,
		Starred:     r.Starred,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
}

func recordFromDTO(connID dbadmin.ConnectionID, ownerID string, d savedQueryDTO) saved.Record {
	return saved.Record{
		ID:           d.ID,
		ConnectionID: connID,
		OwnerID:      ownerID,
		Name:         d.Name,
		Statement:    d.Statement,
		Description: d.Description,
		Tags:         d.Tags,
		Starred:      d.Starred,
		CreatedAt:    d.CreatedAt,
		UpdatedAt:    d.UpdatedAt,
	}
}

func handleListSaved(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		setAuditAction(r.Context(), dbadmin.ActionConnView, dbadmin.Target{ConnectionID: connID})
		user, _ := userFrom(r.Context())
		if err := authorize(s, r.Context(), user, connID, dbadmin.ActionConnView); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		opts := saved.ListOpts{ConnectionID: connID, OwnerID: user.ID}
		// star_only=1 narrows the result to starred entries. Truthy
		// strings ("1", "true", "yes") all opt in.
		switch r.URL.Query().Get("star_only") {
		case "1", "true", "yes":
			opts.StarOnly = true
		}
		records, err := s.saved.List(r.Context(), opts)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		out := make([]savedQueryDTO, 0, len(records))
		for _, rec := range records {
			out = append(out, dtoFromRecord(rec))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func handleCreateSaved(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		setAuditAction(r.Context(), dbadmin.ActionConnUpdate, dbadmin.Target{ConnectionID: connID})
		user, _ := userFrom(r.Context())
		if err := authorize(s, r.Context(), user, connID, dbadmin.ActionConnUpdate); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		var in savedQueryInput
		if err := readJSON(w, r, &in, 1<<20); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		if in.Name == "" || in.Statement == "" {
			writeError(w, r, http.StatusBadRequest, CodeInvalidInput, "name and statement required")
			return
		}
		c, err := s.engine.Conns().Get(r.Context(), connID)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		parsed, err := classifier.Classify(c.Engine, in.Statement)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		if parsed.Class == classifier.ClassForbidden {
			writeError(w, r, http.StatusUnprocessableEntity, CodeForbiddenStatement, "forbidden statement")
			return
		}
		tags := in.Tags
		if tags == nil {
			tags = []string{}
		}
		now := time.Now().UTC()
		rec := saved.Record{
			ID:           newRequestID(),
			ConnectionID: connID,
			OwnerID:      user.ID,
			Name:         in.Name,
			Statement:    in.Statement,
			Description:  in.Description,
			Tags:         tags,
			Starred:      in.Starred,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := s.saved.Append(r.Context(), rec); err != nil {
			if errors.Is(err, saved.ErrConflict) {
				writeError(w, r, http.StatusConflict, CodeConflict, "saved query name already exists")
				return
			}
			if errors.Is(err, saved.ErrInvalidInput) {
				writeError(w, r, http.StatusBadRequest, CodeInvalidInput, "invalid saved query input")
				return
			}
			writeMappedErr(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, dtoFromRecord(rec))
	}
}

func handleDeleteSaved(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		sid := r.PathValue("sid")
		setAuditAction(r.Context(), dbadmin.ActionConnUpdate, dbadmin.Target{ConnectionID: connID})
		user, _ := userFrom(r.Context())
		if err := authorize(s, r.Context(), user, connID, dbadmin.ActionConnUpdate); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		found, owned, err := s.saved.Delete(r.Context(), connID, user.ID, sid)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		// SEC-1: collapse not-found and not-owned into a single 404 so
		// the existence of another user's saved query cannot be probed.
		if !found || !owned {
			writeError(w, r, http.StatusNotFound, CodeNotFound, "saved query not found")
			return
		}
		writeJSON(w, http.StatusOK, emptyResponse{})
	}
}

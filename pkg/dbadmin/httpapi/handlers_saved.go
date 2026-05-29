package httpapi

import (
	"net/http"
	"sync"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/classifier"
)

// savedQueriesStore is a minimal in-memory store for saved queries. The
// project's plan defers persistent saved-query storage to a later PR;
// this package wires the HTTP shape today.
//
// SEC-1: entries are keyed by (connID, ownerID) so a different operator
// cannot list or delete another operator's saved queries. The compound
// key avoids cross-user disclosure within a shared connection while
// preserving the per-connection scoping the UI expects.
//
// DEF-26: per-(conn, user) cap is savedQueriesPerUser. When a create
// would exceed the cap, the oldest entry for the same (conn, user) is
// evicted. Without the cap, the in-memory store grew without bound
// under an automated client that re-saves on every keystroke.
type savedRecord struct {
	dto     savedQueryDTO
	ownerID string
}

// savedQueriesPerUser is the cap on entries per (conn, user) tuple.
// 256 matches the SDK's documented limit; clients that need more
// should migrate to the durable saved-query store landing in a later
// PR.
const savedQueriesPerUser = 256

type savedQueriesStore struct {
	mu      sync.RWMutex
	queries map[string][]savedRecord // keyed by connection id
}

func newSavedQueriesStore() *savedQueriesStore {
	return &savedQueriesStore{queries: map[string][]savedRecord{}}
}

func (s *savedQueriesStore) listForUser(conn, ownerID string) []savedQueryDTO {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]savedQueryDTO, 0, len(s.queries[conn]))
	for _, rec := range s.queries[conn] {
		if rec.ownerID == ownerID {
			out = append(out, rec.dto)
		}
	}
	return out
}

// create appends a new saved query. When the per-(conn, user) tuple
// already holds savedQueriesPerUser entries, the OLDEST entry owned by
// the same user is evicted before the append. This preserves the
// "latest savedQueriesPerUser saves" invariant the SDK documents.
func (s *savedQueriesStore) create(conn, ownerID string, q savedQueryDTO) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.queries[conn]

	// Count how many entries this user already owns and find the
	// index of their oldest. Append order is chronological because
	// every create path goes through this method.
	var owned int
	oldestIdx := -1
	for i, rec := range list {
		if rec.ownerID != ownerID {
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
	s.queries[conn] = append(list, savedRecord{dto: q, ownerID: ownerID})
}

// deleteForUser removes a query iff it exists AND belongs to ownerID.
// Returns:
//
//	(true,  true)  — found and owned, deleted
//	(true,  false) — found but owned by another user (caller should 404)
//	(false, false) — not present at all
//
// The handler folds both not-found cases into a single 404 response so
// the existence of another user's row is not disclosed.
func (s *savedQueriesStore) deleteForUser(conn, ownerID, id string) (found, owned bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.queries[conn]
	for i, rec := range list {
		if rec.dto.ID != id {
			continue
		}
		found = true
		if rec.ownerID != ownerID {
			return true, false
		}
		s.queries[conn] = append(list[:i], list[i+1:]...)
		return true, true
	}
	return false, false
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
		out := s.saved.listForUser(string(connID), user.ID)
		if out == nil {
			out = []savedQueryDTO{}
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
		dto := savedQueryDTO{
			ID:        newRequestID(),
			Name:      in.Name,
			Statement: in.Statement,
			Tags:      in.Tags,
			CreatedAt: time.Now().UTC(),
		}
		if dto.Tags == nil {
			dto.Tags = []string{}
		}
		s.saved.create(string(connID), user.ID, dto)
		writeJSON(w, http.StatusCreated, dto)
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
		found, owned := s.saved.deleteForUser(string(connID), user.ID, sid)
		// SEC-1: collapse not-found and not-owned into a single 404 so
		// the existence of another user's saved query cannot be probed.
		if !found || !owned {
			writeError(w, r, http.StatusNotFound, CodeNotFound, "saved query not found")
			return
		}
		writeJSON(w, http.StatusOK, emptyResponse{})
	}
}

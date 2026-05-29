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
type savedQueriesStore struct {
	mu      sync.RWMutex
	queries map[string][]savedQueryDTO // keyed by connection id
}

func newSavedQueriesStore() *savedQueriesStore {
	return &savedQueriesStore{queries: map[string][]savedQueryDTO{}}
}

func (s *savedQueriesStore) list(conn string) []savedQueryDTO {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]savedQueryDTO, len(s.queries[conn]))
	copy(out, s.queries[conn])
	return out
}

func (s *savedQueriesStore) create(conn string, q savedQueryDTO) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries[conn] = append(s.queries[conn], q)
}

func (s *savedQueriesStore) delete(conn, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.queries[conn]
	for i, q := range list {
		if q.ID == id {
			s.queries[conn] = append(list[:i], list[i+1:]...)
			return true
		}
	}
	return false
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
		out := s.saved.list(string(connID))
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
		s.saved.create(string(connID), dto)
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
		if !s.saved.delete(string(connID), sid) {
			writeError(w, r, http.StatusNotFound, CodeNotFound, "saved query not found")
			return
		}
		writeJSON(w, http.StatusOK, emptyResponse{})
	}
}

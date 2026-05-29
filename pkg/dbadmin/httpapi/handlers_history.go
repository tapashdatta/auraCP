package httpapi

import (
	"net/http"
	"strconv"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/history"
)

func handleListHistory(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		setAuditAction(r.Context(), dbadmin.ActionAuditRead, dbadmin.Target{ConnectionID: connID})
		user, _ := userFrom(r.Context())
		if err := authorize(s, r.Context(), user, connID, dbadmin.ActionAuditRead); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		store := s.historyStore
		if store == nil {
			writeJSON(w, http.StatusOK, listHistoryResponse{Entries: []historyEntryDTO{}, Total: 0})
			return
		}
		opts := history.ListOpts{
			UserID:       user.ID,
			ConnectionID: connID,
		}
		q := r.URL.Query()
		if v := q.Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				opts.Limit = n
			}
		}
		if v := q.Get("offset"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				opts.Offset = n
			}
		}
		entries, err := store.List(r.Context(), opts)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		out := listHistoryResponse{
			Entries: make([]historyEntryDTO, 0, len(entries)),
			Total:   len(entries),
		}
		for _, e := range entries {
			out.Entries = append(out.Entries, historyEntryToDTO(e))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func handleSearchHistory(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		setAuditAction(r.Context(), dbadmin.ActionAuditRead, dbadmin.Target{ConnectionID: connID})
		user, _ := userFrom(r.Context())
		if err := authorize(s, r.Context(), user, connID, dbadmin.ActionAuditRead); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		store := s.historyStore
		if store == nil {
			writeJSON(w, http.StatusOK, searchHistoryResponse{Results: []searchResultDTO{}})
			return
		}
		query := r.URL.Query().Get("q")
		opts := history.ListOpts{UserID: user.ID, ConnectionID: connID}
		results, err := store.Search(r.Context(), query, opts)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		out := searchHistoryResponse{Results: make([]searchResultDTO, 0, len(results))}
		for _, res := range results {
			out.Results = append(out.Results, searchResultDTO{
				historyEntryDTO: historyEntryToDTO(res.Entry),
				Score:           res.Score,
			})
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func handlePatchHistory(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		eidStr := r.PathValue("eid")
		setAuditAction(r.Context(), dbadmin.ActionAuditConfig, dbadmin.Target{ConnectionID: connID})
		user, _ := userFrom(r.Context())
		if err := authorize(s, r.Context(), user, connID, dbadmin.ActionAuditConfig); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		eid, err := strconv.ParseInt(eidStr, 10, 64)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, CodeInvalidInput, "invalid history id")
			return
		}
		var in patchHistoryRequest
		if err := readJSON(w, r, &in, 1<<20); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		store := s.historyStore
		if store == nil {
			writeError(w, r, http.StatusNotFound, CodeNotFound, "history disabled")
			return
		}
		if in.Starred != nil {
			if err := store.Star(r.Context(), eid, user.ID, *in.Starred); err != nil {
				writeMappedErr(w, r, err)
				return
			}
		}
		if in.Tags != nil {
			if err := store.Tag(r.Context(), eid, user.ID, in.Tags); err != nil {
				writeMappedErr(w, r, err)
				return
			}
		}
		writeJSON(w, http.StatusOK, emptyResponse{})
	}
}

func handleDeleteHistory(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		eidStr := r.PathValue("eid")
		setAuditAction(r.Context(), dbadmin.ActionAuditConfig, dbadmin.Target{ConnectionID: connID})
		user, _ := userFrom(r.Context())
		if err := authorize(s, r.Context(), user, connID, dbadmin.ActionAuditConfig); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		eid, err := strconv.ParseInt(eidStr, 10, 64)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, CodeInvalidInput, "invalid history id")
			return
		}
		store := s.historyStore
		if store == nil {
			writeError(w, r, http.StatusNotFound, CodeNotFound, "history disabled")
			return
		}
		if err := store.Delete(r.Context(), eid, user.ID); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, emptyResponse{})
	}
}

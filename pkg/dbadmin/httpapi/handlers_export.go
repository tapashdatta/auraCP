package httpapi

import (
	"net/http"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/classifier"
)

func handleExport(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		setAuditAction(r.Context(), dbadmin.ActionExport, dbadmin.Target{ConnectionID: connID})
		user, _ := userFrom(r.Context())
		if err := authorize(s, r.Context(), user, connID, dbadmin.ActionExport); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		var in exportRequest
		if err := readJSON(w, r, &in, 1<<20); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		if in.Statement == "" || in.Format == "" {
			writeError(w, r, http.StatusBadRequest, CodeInvalidInput, "statement and format required")
			return
		}
		switch in.Format {
		case "csv", "json", "sql":
		default:
			writeError(w, r, http.StatusBadRequest, CodeInvalidInput, "format must be csv|json|sql")
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
		// PR #8 ships the wire shape only. Actual export job machinery
		// lands later; we return a stub signed URL.
		writeJSON(w, http.StatusOK, exportResponse{
			SignedURL: "/api/dbadmin/export/" + newRequestID(),
			Expires:   time.Now().Add(15 * time.Minute),
			JobID:     newRequestID(),
		})
	}
}

func handleImport(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		setAuditAction(r.Context(), dbadmin.ActionImport, dbadmin.Target{ConnectionID: connID})
		user, _ := userFrom(r.Context())
		if err := authorize(s, r.Context(), user, connID, dbadmin.ActionImport); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		// 64 MiB multipart upload ceiling. Best-effort parse — full
		// importer is a later PR.
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			writeError(w, r, http.StatusBadRequest, CodeInvalidInput, "invalid multipart body")
			return
		}
		writeJSON(w, http.StatusOK, importResponse{
			RowsImported: 0,
			JobID:        newRequestID(),
		})
	}
}

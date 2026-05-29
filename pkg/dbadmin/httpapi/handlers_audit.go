package httpapi

import (
	"net/http"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// handleListAudit returns the most recent audit events. PR #8 ships the
// wire shape; the real query path requires AuditSink to grow a query
// method (planned). For now we return an empty list when the sink does
// not implement an optional query interface.
type auditQueryable interface {
	Events() []dbadmin.Event
}

func handleListAudit(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		setAuditAction(r.Context(), dbadmin.ActionAuditRead, dbadmin.Target{ConnectionID: connID})
		user, _ := userFrom(r.Context())
		if err := authorize(s, r.Context(), user, connID, dbadmin.ActionAuditRead); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		sink := s.engine.Audit()
		q, ok := sink.(auditQueryable)
		if !ok {
			writeJSON(w, http.StatusOK, listAuditResponse{Events: []auditEventDTO{}})
			return
		}
		events := q.Events()
		out := listAuditResponse{Events: make([]auditEventDTO, 0, len(events))}
		for _, e := range events {
			if e.Target.ConnectionID != "" && e.Target.ConnectionID != connID {
				continue
			}
			out.Events = append(out.Events, auditEventToDTO(e))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

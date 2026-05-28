package api

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// GET /api/instance/update — cached check (1h TTL); ?refresh=1 forces a fresh
// probe against GitHub. Open to anyone with settings:read so the topbar badge
// can render for non-admin RBAC users too.
func (s *Server) instanceUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if s.updater == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"current":   "",
			"available": false,
			"error":     "updater not initialised",
		})
		return
	}
	refresh, _ := strconv.ParseBool(r.URL.Query().Get("refresh"))
	st := s.updater.Status(r.Context())
	if refresh {
		st = s.updater.Refresh(r.Context())
	}
	writeJSON(w, http.StatusOK, st)
}

// POST /api/instance/update — triggers the detached auracp-update script.
// Returns 202 immediately; the daemon is restarted by dpkg's postinst a few
// seconds later. UI is expected to poll /api/health until it comes back.
func (s *Server) instanceUpdateApply(w http.ResponseWriter, r *http.Request) {
	if s.updater == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "updater not initialised"})
		return
	}
	st := s.updater.Status(r.Context())
	if !st.Available {
		writeJSON(w, http.StatusOK, map[string]string{"message": "already on the latest version"})
		return
	}
	if err := s.updater.Apply(r.Context()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "instance.update", st.Latest)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"started": true,
		"target":  st.Latest,
		"hint":    "auracpd will restart shortly. Poll /api/health every 1s and reload when it returns 200.",
	})
}

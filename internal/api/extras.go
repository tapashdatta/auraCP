package api

import (
	"encoding/json"
	"net/http"

	"github.com/auracp/auracp/internal/ssl"
)

const (
	remoteBackupKey  = "remote_backup_target_enc" // encrypted "remote:path"
	remoteBackupType = "remote_backup_type"       // provider type, e.g. s3
)

// GET /api/sites/{domain}/ssl — live certificate status.
func (s *Server) siteSSL(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, ssl.Of(r.PathValue("domain")))
}

const panelDomainKey = "panel_domain"

// GET /api/settings/panel-domain
func (s *Server) getPanelDomain(w http.ResponseWriter, r *http.Request) {
	d, _ := s.store.GetSetting(panelDomainKey)
	writeJSON(w, http.StatusOK, map[string]string{"domain": d})
}

// POST /api/settings/panel-domain  {domain}  (empty domain → revert to IP:port)
func (s *Server) setPanelDomain(w http.ResponseWriter, r *http.Request) {
	var in struct{ Domain string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.Domain == "" {
		_ = s.store.SetSetting(panelDomainKey, "")
		if err := s.web.RemovePanelProxy(r.Context()); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		s.audit(r, "panel.domain.clear", "")
		writeJSON(w, http.StatusOK, map[string]string{"domain": ""})
		return
	}
	if err := s.web.ApplyPanelProxy(r.Context(), in.Domain, s.panelBackend); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	_ = s.store.SetSetting(panelDomainKey, in.Domain)
	s.audit(r, "panel.domain.set", in.Domain)
	writeJSON(w, http.StatusOK, map[string]string{"domain": in.Domain})
}

// GET /api/backups/remote — current remote config (no secrets).
func (s *Server) getRemoteBackup(w http.ResponseWriter, r *http.Request) {
	_, configured := s.store.GetSetting(remoteBackupKey)
	typ, _ := s.store.GetSetting(remoteBackupType)
	writeJSON(w, http.StatusOK, map[string]any{"configured": configured, "type": typ})
}

// POST /api/backups/remote — define an rclone remote and the target path.
//
//	{type, params:{k:v}, target}  e.g. type=s3, params={provider,access_key_id,...}, target="auracp:bucket/path"
func (s *Server) setRemoteBackup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Type   string            `json:"type"`
		Params map[string]string `json:"params"`
		Target string            `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.Type == "" || in.Target == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "type and target are required"})
		return
	}
	// rclone config create auracp <type> <k> <v> ...  (non-interactive)
	args := []string{"config", "create", "auracp", in.Type}
	for k, v := range in.Params {
		args = append(args, k, v)
	}
	if _, err := s.runner.Run(r.Context(), "rclone", args...); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "rclone config failed: " + err.Error()})
		return
	}
	enc, err := s.secret.Encrypt(in.Target)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	_ = s.store.SetSetting(remoteBackupKey, enc)
	_ = s.store.SetSetting(remoteBackupType, in.Type)
	s.audit(r, "backup.remote.configure", in.Type)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

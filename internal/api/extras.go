package api

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/auracp/auracp/internal/acme"
	"github.com/auracp/auracp/internal/ssl"
)

const (
	remoteBackupKey  = "remote_backup_target_enc" // encrypted "remote:path"
	remoteBackupType = "remote_backup_type"       // provider type, e.g. s3
)

// GET /api/sites/{domain}/ssl — live certificate status, combined with the
// stored issuance state from the certificates table.
//
// The live state (Status / Issuer / Expires / Domains) comes from dialling
// the domain on :443 and reading the cert nginx is actually serving — that's
// the source of truth for "what does a browser see right now". The stored
// state adds context the live dial can't (the last error from lego, the
// attempt count, whether CF DNS-01 is enabled). v0.2.40.
func (s *Server) siteSSL(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	live := ssl.Of(domain)

	out := map[string]any{
		"status":  live.Status,
		"issuer":  live.Issuer,
		"expires": live.Expires,
		"domains": live.Domains,
		"message": live.Message,
	}
	// Stored issuance history from the certificates table (populated by
	// internal/acme on every issuance/renewal attempt). Useful when the
	// live dial says "no cert" but the table has 'lastError' set.
	if cert, ok := s.store.Certificate(domain); ok {
		out["stored"] = map[string]any{
			"status":    cert.Status,
			"lastError": cert.LastError,
			"attempts":  cert.Attempts,
			"issuedAt":  nullInt(cert.IssuedAt),
			"expiresAt": nullInt(cert.ExpiresAt),
		}
	}
	// Whether the operator has flipped Cloudflare DNS-01 on (relevant for
	// orange-clouded domains where HTTP-01 can't reach the origin).
	if cfg, err := s.store.SiteConfig(domain); err == nil {
		out["cloudflareDNS"] = cfg["cloudflare_dns"] == "true"
		// Surface whether the CF token is configured at the instance level
		// so the UI can offer to send the operator to Settings → Cloudflare.
		_, cfTokenSet := s.store.GetSetting(cfTokenKey)
		out["cloudflareTokenSet"] = cfTokenSet
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /api/sites/{domain}/ssl/renew — site-scoped retry, gated on
// sites:update. The existing POST /api/certificates/{domain}/renew also
// works but needs settings:update which is admin-only; this lets a Site
// Manager retry a cert without granting them instance settings access.
//
// v0.2.41: if the site has cloudflare_dns=true (operator forced DNS-01),
// pass ForceDNS01 so we skip HTTP-01 entirely. Otherwise the default
// HTTP-01 → DNS-01 fallback path runs.
func (s *Server) siteRenewCert(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	if s.acme == nil {
		writeErr(w, http.StatusServiceUnavailable, errNoACMEManager)
		return
	}
	opts := acme.IssueOpts{}
	if cfg, err := s.store.SiteConfig(domain); err == nil && cfg["cloudflare_dns"] == "true" {
		opts.ForceDNS01 = true
	}
	if err := s.acme.IssueOnce(r.Context(), domain, opts); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "cert.renew", domain)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// nullInt — convert a nullable int64 column to either the int64 value or nil
// so JSON emits `null` instead of `0` (which the UI would misread as "issued
// at epoch").
func nullInt(n sql.NullInt64) any {
	if !n.Valid {
		return nil
	}
	return n.Int64
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

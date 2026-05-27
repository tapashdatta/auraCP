package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/auracp/auracp/internal/auth"
	"github.com/auracp/auracp/internal/store"
	"github.com/auracp/auracp/internal/webserver"
)

const cfTokenKey = "cloudflare_token_enc"

// reapplyWeb re-renders a site's Caddy config from its stored flags and reloads.
func (s *Server) reapplyWeb(ctx context.Context, domain string) error {
	st, err := s.store.SiteByDomain(domain)
	if err != nil {
		return err
	}
	cfg, err := s.store.SiteConfig(domain)
	if err != nil {
		return err
	}
	spec := webserver.Spec{
		Type: st.Type, Domain: domain, User: st.SiteUser, Upstream: st.Upstream,
		Cache: cfg["cache"] == "true", CacheTTL: cfg["cache_ttl"],
		BlockBots: cfg["block_bots"] == "true",
	}
	if cfg["basic_auth"] == "true" {
		spec.BasicAuthUser = cfg["basic_auth_user"]
		spec.BasicAuthHash = cfg["basic_auth_hash"]
	}
	if cfg["cloudflare_dns"] == "true" {
		if enc, ok := s.store.GetSetting(cfTokenKey); ok {
			if tok, derr := s.secret.Decrypt(enc); derr == nil {
				spec.CloudflareTok = tok
			}
		}
	}
	return s.web.Apply(ctx, spec)
}

// GET /api/sites/{domain}/config — flags only (no secrets).
func (s *Server) getSiteConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.store.SiteConfig(r.PathValue("domain"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	delete(cfg, "basic_auth_hash") // never expose
	writeJSON(w, http.StatusOK, cfg)
}

// PATCH /api/sites/{domain}/config — persist toggles, re-render, reload.
func (s *Server) patchSiteConfig(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	var in map[string]string
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	for k, v := range in {
		// Basic-auth password is hashed (bcrypt) before storage; never persisted plain.
		if k == "basic_auth_password" {
			if v == "" {
				continue
			}
			h, err := auth.HashPassword(v)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
			_ = s.store.SetSiteConfig(domain, "basic_auth_hash", h)
			continue
		}
		if err := s.store.SetSiteConfig(domain, k, v); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if err := s.reapplyWeb(r.Context(), domain); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "site.config.update", domain)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- Cloudflare token (instance-wide) ----

// GET /api/cloudflare — whether a token is configured (never returns it).
func (s *Server) getCloudflare(w http.ResponseWriter, r *http.Request) {
	_, ok := s.store.GetSetting(cfTokenKey)
	writeJSON(w, http.StatusOK, map[string]bool{"configured": ok})
}

// POST /api/cloudflare — store an API token (encrypted at rest).
func (s *Server) setCloudflare(w http.ResponseWriter, r *http.Request) {
	var in struct{ Token string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.Token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token required"})
		return
	}
	enc, err := s.secret.Encrypt(in.Token)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.store.SetSetting(cfTokenKey, enc); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.audit(r, "cloudflare.token.set", "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- per-site SSH/FTP users ----

// GET /api/sites/{domain}/ssh-users
func (s *Server) listSSHUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.SSHUsersForSite(r.PathValue("domain"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if users == nil {
		users = []store.SSHUser{}
	}
	writeJSON(w, http.StatusOK, users)
}

// POST /api/sites/{domain}/ssh-users  {username, type, password?}
func (s *Server) addSSHUser(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	st, err := s.store.SiteByDomain(domain)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	var in struct{ Username, Type, Password string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.Type != "ssh" {
		in.Type = "sftp"
	}
	if in.Password == "" {
		in.Password, _ = auth.RandomPassword()
	}
	if err := s.osu.CreateExtra(r.Context(), in.Username, st.SiteUser, in.Type == "ssh"); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	_ = s.osu.SetPassword(r.Context(), in.Username, in.Password)
	enc, _ := s.secret.Encrypt(in.Password)
	if err := s.store.AddSSHUser(store.SSHUser{Domain: domain, Username: in.Username, Type: in.Type}, enc); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.audit(r, "ssh_user.create", domain+"/"+in.Username)
	writeJSON(w, http.StatusCreated, map[string]string{"username": in.Username, "type": in.Type, "password": in.Password})
}

// DELETE /api/sites/{domain}/ssh-users/{username}
func (s *Server) deleteSSHUser(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	username := r.PathValue("username")
	if _, err := s.store.SSHUserByName(domain, username); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err := s.osu.DeleteExtra(r.Context(), username); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.store.DeleteSSHUser(domain, username); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.audit(r, "ssh_user.delete", domain+"/"+username)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

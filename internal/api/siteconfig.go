package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/auracp/auracp/internal/auth"
	"github.com/auracp/auracp/internal/store"
	"github.com/auracp/auracp/internal/webserver"
)

const cfTokenKey = "cloudflare_token_enc"

// reapplyWeb re-renders a site's nginx vhost from its stored flags and reloads.
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
		Type: st.Type, Domain: domain, User: st.SiteUser, Root: st.RootPath, Upstream: st.Upstream,
		PHPVer: st.PHPVersion,
		Cache: cfg["cache"] == "true", CacheTTL: cfg["cache_ttl"],
		BlockBots: cfg["block_bots"] == "true",
		Override:  cfg["vhost_override"],   // verbatim vhost from the in-panel editor
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
// v0.2.41: also include the validation status from the most recent save so
// the UI can show "✓ Valid" / "✗ Invalid" inline on refresh, without making
// the operator paste the token again.
func (s *Server) getCloudflare(w http.ResponseWriter, r *http.Request) {
	_, ok := s.store.GetSetting(cfTokenKey)
	status, _ := s.store.GetSetting("cloudflare_token_status") // "valid"|"invalid"|""
	writeJSON(w, http.StatusOK, map[string]any{
		"configured": ok,
		"status":     status,
	})
}

// POST /api/cloudflare — validate then store a Cloudflare API token.
//
// v0.2.41: token is now verified against Cloudflare's /user/tokens/verify
// endpoint BEFORE persistence. A token that doesn't authenticate (typo,
// revoked, wrong scopes) is rejected with the verbatim CF error so the
// operator knows exactly what to fix — no more saving a dud token and
// finding out only when DNS-01 issuance fails 30s later.
func (s *Server) setCloudflare(w http.ResponseWriter, r *http.Request) {
	var in struct{ Token string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	in.Token = strings.TrimSpace(in.Token)
	if in.Token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token required"})
		return
	}
	if vErr := verifyCloudflareToken(r.Context(), in.Token); vErr != nil {
		// Don't persist — let the operator fix the token first.
		_ = s.store.SetSetting("cloudflare_token_status", "invalid")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": vErr.Error()})
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
	_ = s.store.SetSetting("cloudflare_token_status", "valid")
	s.audit(r, "cloudflare.token.set", "validated")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "valid"})
}

// verifyCloudflareToken calls Cloudflare's user/tokens/verify endpoint with
// the operator's token. Returns nil if CF reports the token as active;
// otherwise a human-readable error citing the specific failure reason CF
// gave us. 5s timeout — verify is meant to be lightning fast.
func verifyCloudflareToken(ctx context.Context, token string) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.cloudflare.com/client/v4/user/tokens/verify", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	cli := &http.Client{Timeout: 5 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach Cloudflare: %w", err)
	}
	defer resp.Body.Close()
	var body struct {
		Success bool `json:"success"`
		Errors  []struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
		Result struct {
			Status string `json:"status"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("Cloudflare returned an unparseable response (HTTP %d)", resp.StatusCode)
	}
	if !body.Success {
		if len(body.Errors) > 0 {
			return fmt.Errorf("Cloudflare rejected the token: %s (code %d)", body.Errors[0].Message, body.Errors[0].Code)
		}
		return fmt.Errorf("Cloudflare rejected the token (HTTP %d)", resp.StatusCode)
	}
	if body.Result.Status != "" && body.Result.Status != "active" {
		return fmt.Errorf("Cloudflare reports token status: %s", body.Result.Status)
	}
	return nil
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

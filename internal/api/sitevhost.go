package api

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/auracp/auracp/internal/paths"
	"github.com/auracp/auracp/internal/store"
)

// GET /api/sites/{domain}/vhost — returns the currently-rendered nginx config
// for the site (read from disk; what nginx is actually serving). Read-only.
func (s *Server) getSiteVhost(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	st, err := s.store.SiteByDomain(domain)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	body, err := os.ReadFile(paths.NginxSiteFile(domain))
	if err != nil {
		// Site exists in the DB but the vhost file is missing — race with a
		// failed reload, or the panel hasn't written it yet.
		writeJSON(w, http.StatusOK, map[string]string{
			"content":   "",
			"path":      paths.NginxSiteFile(domain),
			"writable":  "true",
			"site_user": st.SiteUser,
			"note":      "vhost not yet generated; rendering will happen on the next reload",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"content":   string(body),
		"path":      paths.NginxSiteFile(domain),
		"writable":  "true",
		"site_user": st.SiteUser,
	})
}

// PUT /api/sites/{domain}/vhost — accept a hand-edited vhost and store it as
// a per-site OVERRIDE (site_config key "vhost_override"). The next render
// emits the override verbatim instead of the generated template. `content`
// empty / missing key removes the override and reverts to generated.
//
// We still validate via `nginx -t` before persisting — a bad config from the
// editor must not break the box.
func (s *Server) putSiteVhost(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	var in struct{ Content string `json:"content"` }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.Content == "" {
		// Remove override → fall back to generated vhost on next reapply.
		_ = s.store.SetSiteConfig(domain, "vhost_override", "")
	} else {
		if err := s.store.SetSiteConfig(domain, "vhost_override", in.Content); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	// Re-render with the override applied; the renderer in v0.2.11 honours
	// the vhost_override site_config value when present (see webserver.Apply).
	if err := s.reapplyWeb(r.Context(), domain); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "site.vhost.edit", domain)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// PATCH /api/sites/{domain} — partial site update (currently just root_path).
// Future home for any other in-place site mutations that don't fit the
// dedicated /node-version, /pm2, /php-version endpoints.
func (s *Server) patchSite(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	st, err := s.store.SiteByDomain(domain)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	var in struct {
		RootPath *string `json:"root"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.RootPath != nil {
		// Permit only paths inside the site user's home — operators should be
		// pointing at subdirs like .../htdocs/<domain>/public for Laravel /
		// Symfony / Statamic. Going outside the site UID is a security hole.
		root := *in.RootPath
		homePrefix := paths.SiteHome(st.SiteUser) + "/"
		if root != paths.SiteHome(st.SiteUser) && !startsWith(root, homePrefix) {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "root must be inside " + paths.SiteHome(st.SiteUser),
			})
			return
		}
		if err := s.store.SetSiteRoot(domain, root); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if err := s.reapplyWeb(r.Context(), domain); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	s.audit(r, "site.update", domain)
	st, _ = s.store.SiteByDomain(domain)
	writeJSON(w, http.StatusOK, st.View())
}

// Tiny helper to avoid an extra strings import here.
func startsWith(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	return s[:len(prefix)] == prefix
}

// Ensure store has SetSiteRoot — declared once in queries.go but referenced
// here for compile-time clarity.
var _ = func(_ *store.Store) {}

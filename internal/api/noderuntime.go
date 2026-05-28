package api

import (
	"encoding/json"
	"net/http"

	"github.com/auracp/auracp/internal/store"
)

// GET /api/instance/node-versions
func (s *Server) listNodeRuntimes(w http.ResponseWriter, r *http.Request) {
	rts, err := s.store.NodeRuntimes()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if rts == nil {
		rts = []store.NodeRuntime{}
	}
	writeJSON(w, http.StatusOK, rts)
}

// POST /api/instance/node-versions {version, makeDefault?}
func (s *Server) installNodeRuntime(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version     string `json:"version"`
		MakeDefault bool   `json:"makeDefault"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.node.Install(r.Context(), in.Version, in.MakeDefault); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "node.install", in.Version)
	writeJSON(w, http.StatusCreated, map[string]string{"version": in.Version})
}

// POST /api/instance/node-versions/{version}/default
func (s *Server) setDefaultNodeRuntime(w http.ResponseWriter, r *http.Request) {
	v := r.PathValue("version")
	if err := s.node.SetDefault(r.Context(), v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "node.default", v)
	writeJSON(w, http.StatusOK, map[string]string{"version": v})
}

// DELETE /api/instance/node-versions/{version}
func (s *Server) deleteNodeRuntime(w http.ResponseWriter, r *http.Request) {
	v := r.PathValue("version")
	if err := s.node.Remove(r.Context(), v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "node.remove", v)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// PUT /api/sites/{domain}/pm2 {enabled}
func (s *Server) setSitePM2(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	st, err := s.store.SiteByDomain(domain)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if st.Type != "nodejs" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "site is not a Node.js site"})
		return
	}
	var in struct{ Enabled bool }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.SetSitePM2(domain, in.Enabled); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.sites.ReapplyRuntime(r.Context(), domain); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "site.pm2", domain)
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": in.Enabled})
}

// PUT /api/sites/{domain}/node-version {version}
func (s *Server) setSiteNodeVersion(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	st, err := s.store.SiteByDomain(domain)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if st.Type != "nodejs" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "site is not a Node.js site"})
		return
	}
	var in struct{ Version string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.Version == "" {
		in.Version = "default"
	}
	// Reject unknown versions (except the symbolic "default" which always resolves).
	if in.Version != "default" {
		if _, ok := s.store.NodeRuntime(in.Version); !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "node runtime " + in.Version + " is not installed"})
			return
		}
	}
	if err := s.store.SetSiteNodeVersion(domain, in.Version); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.sites.ReapplyRuntime(r.Context(), domain); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "site.node-version", domain+" -> "+in.Version)
	writeJSON(w, http.StatusOK, map[string]string{"version": in.Version})
}

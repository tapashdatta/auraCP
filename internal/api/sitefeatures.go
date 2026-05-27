package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/auracp/auracp/internal/files"
	"github.com/auracp/auracp/internal/logs"
	"github.com/auracp/auracp/internal/store"
)

// GET /api/sites/{domain}/logs?kind=access&n=200
func (s *Server) siteLogs(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.SiteByDomain(r.PathValue("domain"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	n := 200
	if v, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && v > 0 {
		n = v
	}
	lines, err := logs.Tail(st.SiteUser, r.URL.Query().Get("kind"), n)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
}

// GET /api/sites/{domain}/files?path=sub/dir
func (s *Server) siteFiles(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	st, err := s.store.SiteByDomain(domain)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	entries, err := files.List(st.SiteUser, domain, r.URL.Query().Get("path"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// GET /api/sites/{domain}/cron
func (s *Server) listCron(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.store.CronJobsForSite(r.PathValue("domain"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if jobs == nil {
		jobs = []store.CronJob{}
	}
	writeJSON(w, http.StatusOK, jobs)
}

// POST /api/sites/{domain}/cron
func (s *Server) addCron(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	st, err := s.store.SiteByDomain(domain)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	var in struct {
		Schedule string `json:"schedule"`
		Command  string `json:"command"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.Schedule == "" || in.Command == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "schedule and command are required"})
		return
	}
	if err := s.store.AddCronJob(store.CronJob{
		Domain: domain, User: st.SiteUser, Schedule: in.Schedule, Command: in.Command, Enabled: true,
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.cron.Apply(r.Context(), st.SiteUser, domain); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

// DELETE /api/sites/{domain}/cron/{id}
func (s *Server) deleteCron(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	st, err := s.store.SiteByDomain(domain)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.DeleteCronJob(domain, id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.cron.Apply(r.Context(), st.SiteUser, domain); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

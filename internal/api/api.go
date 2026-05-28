// Package api exposes auraCP's JSON HTTP API (consumed by the Svelte UI and the
// auracp CLI). Uses stdlib net/http routing (Go 1.22+ method+path patterns) —
// no web framework, in keeping with the lightweight goal.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/auracp/auracp/internal/auth"
	"github.com/auracp/auracp/internal/backup"
	"github.com/auracp/auracp/internal/cron"
	"github.com/auracp/auracp/internal/db"
	"github.com/auracp/auracp/internal/osuser"
	"github.com/auracp/auracp/internal/secret"
	"github.com/auracp/auracp/internal/site"
	"github.com/auracp/auracp/internal/store"
	"github.com/auracp/auracp/internal/system"
	"github.com/auracp/auracp/internal/webserver"
)

type Server struct {
	store   *store.Store
	sites   *site.Manager
	dbs     *db.Manager
	cron    *cron.Manager
	backups *backup.Manager
	web     *webserver.Manager
	osu     *osuser.Manager
	secret  *secret.Box
	runner  *system.Runner
}

// Deps bundles the managers the API needs.
type Deps struct {
	Sites   *site.Manager
	DBs     *db.Manager
	Cron    *cron.Manager
	Backups *backup.Manager
	Web     *webserver.Manager
	OS      *osuser.Manager
	Secret  *secret.Box
	Runner  *system.Runner
}

// Register wires the API routes onto mux.
func Register(mux *http.ServeMux, s *store.Store, d Deps) {
	srv := &Server{store: s, sites: d.Sites, dbs: d.DBs, cron: d.Cron, backups: d.Backups,
		web: d.Web, osu: d.OS, secret: d.Secret, runner: d.Runner}

	// public
	mux.HandleFunc("GET /api/health", srv.health)
	mux.HandleFunc("GET /api/auth/setup", srv.setupStatus)
	mux.HandleFunc("POST /api/auth/setup", srv.setupAdmin)
	mux.HandleFunc("POST /api/auth/login", srv.login)
	mux.HandleFunc("POST /api/auth/mfa/verify", srv.mfaVerify)
	mux.HandleFunc("POST /api/auth/logout", srv.logout)
	mux.HandleFunc("GET /api/auth/me", srv.me)

	// authenticated: account / MFA management
	mux.Handle("POST /api/auth/mfa/setup", srv.protect(srv.mfaSetup))
	mux.Handle("POST /api/auth/mfa/enable", srv.protect(srv.mfaEnable))
	mux.Handle("POST /api/auth/mfa/disable", srv.protect(srv.mfaDisable))

	// authenticated: sites (granular CRUD permissions)
	mux.Handle("GET /api/sites", srv.requirePerm("sites", "read", srv.listSites))
	mux.Handle("POST /api/sites", srv.requirePerm("sites", "create", srv.createSite))
	mux.Handle("GET /api/sites/{domain}", srv.requirePerm("sites", "read", srv.getSite))
	mux.Handle("DELETE /api/sites/{domain}", srv.requirePerm("sites", "delete", srv.deleteSite))
	mux.Handle("GET /api/sites/{domain}/config", srv.requirePerm("sites", "read", srv.getSiteConfig))
	mux.Handle("PATCH /api/sites/{domain}/config", srv.requirePerm("sites", "update", srv.patchSiteConfig))

	// databases
	mux.Handle("GET /api/database-servers", srv.protect(srv.listDatabaseServers))
	mux.Handle("GET /api/sites/{domain}/databases", srv.requirePerm("databases", "read", srv.listDatabases))
	mux.Handle("POST /api/sites/{domain}/databases", srv.requirePerm("databases", "create", srv.createDatabase))

	// per-site features
	mux.Handle("GET /api/sites/{domain}/logs", srv.protect(srv.siteLogs))
	mux.Handle("GET /api/sites/{domain}/files", srv.requirePerm("files", "read", srv.siteFiles))
	mux.Handle("GET /api/sites/{domain}/cron", srv.requirePerm("cron", "read", srv.listCron))
	mux.Handle("POST /api/sites/{domain}/cron", srv.requirePerm("cron", "create", srv.addCron))
	mux.Handle("DELETE /api/sites/{domain}/cron/{id}", srv.requirePerm("cron", "delete", srv.deleteCron))
	mux.Handle("GET /api/sites/{domain}/backups", srv.requirePerm("backups", "read", srv.listBackups))
	mux.Handle("POST /api/sites/{domain}/backups", srv.requirePerm("backups", "create", srv.createBackup))
	mux.Handle("GET /api/sites/{domain}/ssh-users", srv.requirePerm("ssh_users", "read", srv.listSSHUsers))
	mux.Handle("POST /api/sites/{domain}/ssh-users", srv.requirePerm("ssh_users", "create", srv.addSSHUser))
	mux.Handle("DELETE /api/sites/{domain}/ssh-users/{username}", srv.requirePerm("ssh_users", "delete", srv.deleteSSHUser))
	mux.Handle("GET /api/sites/{domain}/ssl", srv.requirePerm("sites", "read", srv.siteSSL))

	// instance + admin + cloudflare
	mux.Handle("GET /api/instance", srv.protect(srv.instanceInfo))
	mux.Handle("GET /api/instance/services", srv.requireAdmin(srv.instanceServices))
	mux.Handle("GET /api/admin/users", srv.requirePerm("users", "read", srv.listAdminUsers))
	mux.Handle("POST /api/admin/users", srv.requirePerm("users", "create", srv.createAdminUser))
	mux.Handle("PUT /api/admin/users/{email}", srv.requirePerm("users", "update", srv.updateAdminUser))
	mux.Handle("DELETE /api/admin/users/{email}", srv.requirePerm("users", "delete", srv.deleteAdminUser))
	mux.Handle("GET /api/settings", srv.protect(srv.getSettings))
	mux.Handle("PUT /api/settings", srv.requirePerm("settings", "update", srv.putSettings))
	mux.Handle("GET /api/cloudflare", srv.requirePerm("settings", "read", srv.getCloudflare))
	mux.Handle("POST /api/cloudflare", srv.requirePerm("settings", "update", srv.setCloudflare))
	mux.Handle("GET /api/backups/remote", srv.requirePerm("settings", "read", srv.getRemoteBackup))
	mux.Handle("POST /api/backups/remote", srv.requirePerm("settings", "update", srv.setRemoteBackup))
	mux.Handle("GET /api/audit", srv.requireAdmin(srv.auditLog))
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "auracpd"})
}

func (s *Server) listSites(w http.ResponseWriter, r *http.Request) {
	sites, err := s.store.Sites()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	views := make([]store.SiteView, 0, len(sites))
	for _, st := range sites {
		views = append(views, st.View())
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) getSite(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.SiteByDomain(r.PathValue("domain"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, st.View())
}

func (s *Server) createSite(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Type       string `json:"type"`
		Domain     string `json:"domain"`
		SiteUser   string `json:"user"`
		Password   string `json:"password"`
		PHPVersion string `json:"phpVersion"`
		StartFile  string `json:"startFile"`
		Module     string `json:"module"`
		Upstream   string `json:"upstream"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	rec, err := s.sites.Create(r.Context(), site.Spec{
		Type: in.Type, Domain: in.Domain, User: in.SiteUser, Password: in.Password,
		PHPVer: in.PHPVersion, StartFile: in.StartFile, Module: in.Module,
		Upstream: in.Upstream, NodeReady: true,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "site.create", rec.Domain)
	writeJSON(w, http.StatusCreated, rec.View())
}

func (s *Server) deleteSite(w http.ResponseWriter, r *http.Request) {
	if err := s.sites.Delete(r.Context(), r.PathValue("domain")); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "site.delete", r.PathValue("domain"))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) listDatabases(w http.ResponseWriter, r *http.Request) {
	dbs, err := s.store.DatabasesForSite(r.PathValue("domain"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if dbs == nil {
		dbs = []store.Database{}
	}
	writeJSON(w, http.StatusOK, dbs)
}

func (s *Server) createDatabase(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	var in struct {
		Engine   string `json:"engine"` // mariadb|postgres
		Name     string `json:"name"`
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.Password == "" {
		pw, err := auth.RandomPassword()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		in.Password = pw
	}
	if err := s.dbs.Create(r.Context(), in.Engine, domain, in.Name, in.User, in.Password); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "database.create", in.Engine+":"+in.Name)
	// Return the generated password once (so the operator can copy it).
	writeJSON(w, http.StatusCreated, map[string]string{
		"engine": in.Engine, "name": in.Name, "user": in.User, "password": in.Password,
	})
}

func (s *Server) listDatabaseServers(w http.ResponseWriter, r *http.Request) {
	servers, err := s.store.DatabaseServers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, servers)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

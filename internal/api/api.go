// Package api exposes auraCP's JSON HTTP API (consumed by the Svelte UI and the
// auracp CLI). Uses stdlib net/http routing (Go 1.22+ method+path patterns) —
// no web framework, in keeping with the lightweight goal.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/auracp/auracp/internal/acme"
	"github.com/auracp/auracp/internal/auth"
	"github.com/auracp/auracp/internal/backup"
	"github.com/auracp/auracp/internal/cron"
	"github.com/auracp/auracp/internal/db"
	"github.com/auracp/auracp/internal/noderuntime"
	"github.com/auracp/auracp/internal/osuser"
	"github.com/auracp/auracp/internal/perm"
	"github.com/auracp/auracp/internal/phpruntime"
	"github.com/auracp/auracp/internal/secret"
	"github.com/auracp/auracp/internal/site"
	"github.com/auracp/auracp/internal/store"
	"github.com/auracp/auracp/internal/system"
	"github.com/auracp/auracp/internal/updater"
	"github.com/auracp/auracp/internal/webserver"
)

type Server struct {
	store        *store.Store
	sites        *site.Manager
	dbs          *db.Manager
	cron         *cron.Manager
	backups      *backup.Manager
	web          *webserver.Manager
	osu          *osuser.Manager
	node         *noderuntime.Manager
	php          *phpruntime.Manager
	acme         *acme.Manager
	updater      *updater.Manager
	secret       *secret.Box
	runner       *system.Runner
	panelBackend string
}

// Deps bundles the managers the API needs.
type Deps struct {
	Sites        *site.Manager
	DBs          *db.Manager
	Cron         *cron.Manager
	Backups      *backup.Manager
	Web          *webserver.Manager
	OS           *osuser.Manager
	Node         *noderuntime.Manager
	PHP          *phpruntime.Manager
	ACME         *acme.Manager
	Updater      *updater.Manager
	Secret       *secret.Box
	Runner       *system.Runner
	PanelBackend string
}

// Register wires the API routes onto mux.
func Register(mux *http.ServeMux, s *store.Store, d Deps) {
	srv := &Server{store: s, sites: d.Sites, dbs: d.DBs, cron: d.Cron, backups: d.Backups,
		web: d.Web, osu: d.OS, node: d.Node, php: d.PHP, acme: d.ACME, updater: d.Updater,
		secret: d.Secret, runner: d.Runner, panelBackend: d.PanelBackend}

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
	mux.Handle("PATCH /api/sites/{domain}", srv.requirePerm("sites", "update", srv.patchSite))
	mux.Handle("DELETE /api/sites/{domain}", srv.requirePerm("sites", "delete", srv.deleteSite))
	mux.Handle("GET /api/sites/{domain}/config", srv.requirePerm("sites", "read", srv.getSiteConfig))
	mux.Handle("PATCH /api/sites/{domain}/config", srv.requirePerm("sites", "update", srv.patchSiteConfig))
	mux.Handle("GET /api/sites/{domain}/vhost", srv.requirePerm("sites", "read", srv.getSiteVhost))
	mux.Handle("PUT /api/sites/{domain}/vhost", srv.requirePerm("sites", "update", srv.putSiteVhost))

	// databases
	mux.Handle("GET /api/database-servers", srv.protect(srv.listDatabaseServers))
	mux.Handle("GET /api/sites/{domain}/databases", srv.requirePerm("databases", "read", srv.listDatabases))
	mux.Handle("POST /api/sites/{domain}/databases", srv.requirePerm("databases", "create", srv.createDatabase))

	// per-site features
	mux.Handle("GET /api/sites/{domain}/logs", srv.protect(srv.siteLogs))
	mux.Handle("GET /api/sites/{domain}/files", srv.requirePerm("files", "read", srv.siteFiles))
	mux.Handle("POST /api/sites/{domain}/files", srv.requirePerm("files", "create", srv.uploadFiles))
	mux.Handle("DELETE /api/sites/{domain}/files", srv.requirePerm("files", "delete", srv.deleteFile))
	mux.Handle("GET /api/sites/{domain}/files/download", srv.requirePerm("files", "read", srv.downloadFile))
	mux.Handle("POST /api/sites/{domain}/files/rename", srv.requirePerm("files", "update", srv.renameFile))
	mux.Handle("POST /api/sites/{domain}/files/mkdir", srv.requirePerm("files", "create", srv.mkdirFile))
	mux.Handle("POST /api/sites/{domain}/files/touch", srv.requirePerm("files", "create", srv.touchFile))
	mux.Handle("GET /api/sites/{domain}/files/text", srv.requirePerm("files", "read", srv.readTextFile))
	mux.Handle("PUT /api/sites/{domain}/files/text", srv.requirePerm("files", "update", srv.writeTextFile))
	mux.Handle("POST /api/sites/{domain}/files/chmod", srv.requirePerm("files", "update", srv.chmodFile))
	mux.Handle("POST /api/sites/{domain}/files/delete-many", srv.requirePerm("files", "delete", srv.deleteManyFiles))
	mux.Handle("POST /api/sites/{domain}/files/zip", srv.requirePerm("files", "create", srv.zipFiles))
	mux.Handle("POST /api/sites/{domain}/files/unzip", srv.requirePerm("files", "create", srv.unzipFile))
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
	mux.Handle("POST /api/instance/services/{name}/restart", srv.requireAdmin(srv.instanceServiceRestart))
	mux.Handle("GET /api/admin/users", srv.requirePerm("users", "read", srv.listAdminUsers))
	mux.Handle("POST /api/admin/users", srv.requirePerm("users", "create", srv.createAdminUser))
	mux.Handle("PUT /api/admin/users/{email}", srv.requirePerm("users", "update", srv.updateAdminUser))
	mux.Handle("DELETE /api/admin/users/{email}", srv.requirePerm("users", "delete", srv.deleteAdminUser))
	mux.Handle("GET /api/admin/roles", srv.requirePerm("users", "read", srv.listRolePerms))
	mux.Handle("PUT /api/admin/roles/{role}", srv.requirePerm("users", "update", srv.updateRolePerms))
	mux.Handle("DELETE /api/admin/roles/{role}", srv.requirePerm("users", "update", srv.resetRolePerms))
	mux.Handle("GET /api/settings", srv.protect(srv.getSettings))
	mux.Handle("PUT /api/settings", srv.requirePerm("settings", "update", srv.putSettings))
	mux.Handle("GET /api/cloudflare", srv.requirePerm("settings", "read", srv.getCloudflare))
	mux.Handle("POST /api/cloudflare", srv.requirePerm("settings", "update", srv.setCloudflare))
	mux.Handle("GET /api/settings/panel-domain", srv.requirePerm("settings", "read", srv.getPanelDomain))
	mux.Handle("POST /api/settings/panel-domain", srv.requirePerm("settings", "update", srv.setPanelDomain))
	mux.Handle("GET /api/backups/remote", srv.requirePerm("settings", "read", srv.getRemoteBackup))
	mux.Handle("POST /api/backups/remote", srv.requirePerm("settings", "update", srv.setRemoteBackup))
	mux.Handle("GET /api/audit", srv.requireAdmin(srv.auditLog))

	// Node.js runtime management
	mux.Handle("GET /api/instance/node-versions", srv.requirePerm("settings", "read", srv.listNodeRuntimes))
	mux.Handle("POST /api/instance/node-versions", srv.requirePerm("settings", "update", srv.installNodeRuntime))
	mux.Handle("POST /api/instance/node-versions/{version}/default", srv.requirePerm("settings", "update", srv.setDefaultNodeRuntime))
	mux.Handle("DELETE /api/instance/node-versions/{version}", srv.requirePerm("settings", "update", srv.deleteNodeRuntime))
	mux.Handle("PUT /api/sites/{domain}/node-version", srv.requirePerm("sites", "update", srv.setSiteNodeVersion))
	mux.Handle("PUT /api/sites/{domain}/pm2", srv.requirePerm("sites", "update", srv.setSitePM2))

	// PHP runtime management (parallel to node-versions). v0.2.0+.
	mux.Handle("GET /api/instance/php-versions", srv.requirePerm("settings", "read", srv.listPHPRuntimes))
	mux.Handle("POST /api/instance/php-versions", srv.requirePerm("settings", "update", srv.installPHPRuntime))
	mux.Handle("POST /api/instance/php-versions/{version}/default", srv.requirePerm("settings", "update", srv.setDefaultPHPRuntime))
	mux.Handle("DELETE /api/instance/php-versions/{version}", srv.requirePerm("settings", "update", srv.deletePHPRuntime))
	mux.Handle("PUT /api/sites/{domain}/php-version", srv.requirePerm("sites", "update", srv.setSitePHPVersion))
	mux.Handle("GET /api/sites/{domain}/php-settings", srv.requirePerm("sites", "read", srv.getPHPSettings))
	mux.Handle("PUT /api/sites/{domain}/php-settings", srv.requirePerm("sites", "update", srv.setPHPSettings))

	// Certificates listing (read-only; issuance happens automatically).
	mux.Handle("GET /api/certificates", srv.requirePerm("settings", "read", srv.listCertificates))
	mux.Handle("POST /api/certificates/{domain}/renew", srv.requirePerm("settings", "update", srv.renewCertificate))

	// Self-update — GET is settings:read (so the topbar badge renders for any
	// signed-in user); POST is settings:update so a curious viewer can't kick
	// off an apt install via the JSON API.
	mux.Handle("GET /api/instance/update", srv.requirePerm("settings", "read", srv.instanceUpdateStatus))
	mux.Handle("POST /api/instance/update", srv.requirePerm("settings", "update", srv.instanceUpdateApply))
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
	// v0.2.15: filter to the user's allowed-sites scope (admins see everything).
	if u, ok := s.currentUser(r); ok && u.Role != "ROLE_ADMIN" {
		if set, all := perm.AllowedSites(u.SitesScope); !all {
			filtered := sites[:0]
			for _, st := range sites {
				if set[st.Domain] {
					filtered = append(filtered, st)
				}
			}
			sites = filtered
		}
	}
	views := make([]store.SiteView, 0, len(sites))
	for _, st := range sites {
		views = append(views, st.View())
	}
	writeJSON(w, http.StatusOK, views)
}

// scopedDomain returns 403 (and ok=false) if the current user can't act on the
// given domain, given their sites_scope. Admins always pass. Use this at the
// top of per-site mutating handlers; read handlers also use it so a scoped
// user can't sniff config from sites they shouldn't see.
func (s *Server) scopedDomain(w http.ResponseWriter, r *http.Request, domain string) bool {
	u, ok := s.currentUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return false
	}
	if u.Role == "ROLE_ADMIN" {
		return true
	}
	set, all := perm.AllowedSites(u.SitesScope)
	if all || set[domain] {
		return true
	}
	writeJSON(w, http.StatusForbidden, map[string]string{"error": "this site is not in your assigned scope"})
	return false
}

func (s *Server) getSite(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	if !s.scopedDomain(w, r, domain) {
		return
	}
	st, err := s.store.SiteByDomain(domain)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, st.View())
}

func (s *Server) createSite(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Type        string `json:"type"`
		Domain      string `json:"domain"`
		SiteUser    string `json:"user"`
		Password    string `json:"password"`
		PHPVersion  string `json:"phpVersion"`
		NodeVersion string `json:"nodeVersion"`
		PM2         bool   `json:"pm2"`
		StartFile   string `json:"startFile"`
		Module      string `json:"module"`
		Upstream    string `json:"upstream"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	rec, err := s.sites.Create(r.Context(), site.Spec{
		Type: in.Type, Domain: in.Domain, User: in.SiteUser, Password: in.Password,
		PHPVer: in.PHPVersion, NodeVer: in.NodeVersion, UsePM2: in.PM2,
		StartFile: in.StartFile, Module: in.Module,
		Upstream: in.Upstream,
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

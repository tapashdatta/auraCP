// Package api exposes auraCP's JSON HTTP API (consumed by the Svelte UI and the
// auracp CLI). Uses stdlib net/http routing (Go 1.22+ method+path patterns) —
// no web framework, in keeping with the lightweight goal.
package api

import (
	"encoding/json"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

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
	"github.com/auracp/auracp/internal/wpinstall"
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
	mux.Handle("DELETE /api/sites/{domain}/databases/{engine}/{name}", srv.requirePerm("databases", "delete", srv.deleteDatabase))
	mux.Handle("POST /api/sites/{domain}/databases/{engine}/{name}/manage", srv.requirePerm("databases", "read", srv.manageDatabase))

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
		// v0.2.34: WordPress one-click auto-install
		WPInstall    bool   `json:"wpInstall"`
		WPTitle      string `json:"wpTitle"`
		WPAdminUser  string `json:"wpAdminUser"`
		WPAdminPass  string `json:"wpAdminPass"`
		WPAdminEmail string `json:"wpAdminEmail"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	spec := site.Spec{
		Type: in.Type, Domain: in.Domain, User: in.SiteUser, Password: in.Password,
		PHPVer: in.PHPVersion, NodeVer: in.NodeVersion, UsePM2: in.PM2,
		StartFile: in.StartFile, Module: in.Module,
		Upstream: in.Upstream,
	}

	// v0.2.34: WordPress auto-install pre-flight. We provision the DB here
	// (so its creds land in the store regardless of whether wp-cli succeeds
	// later) and pass them into site.Create which runs the wp-cli steps.
	if in.Type == "wordpress" && in.WPInstall {
		if !wpinstall.Available() {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "wp-cli is not installed on this host. Re-run: sudo auracp-install --yes --php=yes",
			})
			return
		}
		// Auto-generate DB name/user. Same suffix is reused so the operator
		// can guess one from the other when troubleshooting.
		suffix, _ := auth.RandomToken()
		if len(suffix) > 8 {
			suffix = suffix[:8]
		}
		dbName := "wp_" + suffix
		dbUser := dbName + "_u"
		dbPass, perr := auth.RandomPassword()
		if perr != nil {
			writeErr(w, http.StatusInternalServerError, perr)
			return
		}
		if err := s.dbs.Create(r.Context(), "mariadb", in.Domain, dbName, dbUser, dbPass); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "create WP database: " + err.Error() + ". MariaDB must be installed on this host.",
			})
			return
		}
		// Admin password: auto-generate if the operator didn't supply one.
		// We return the password to the panel once on success so it can be
		// shown in a modal and copied; nothing of it stays in cleartext.
		if in.WPAdminPass == "" {
			in.WPAdminPass, _ = auth.RandomPassword()
		}
		if in.WPAdminUser == "" {
			in.WPAdminUser = "admin"
		}
		if in.WPAdminEmail == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "admin email is required for WordPress auto-install"})
			return
		}
		if in.WPTitle == "" {
			in.WPTitle = in.Domain
		}
		spec.WPInstall = true
		spec.WPDBName = dbName
		spec.WPDBUser = dbUser
		spec.WPDBPass = dbPass
		spec.WPTitle = in.WPTitle
		spec.WPAdminUser = in.WPAdminUser
		spec.WPAdminPass = in.WPAdminPass
		spec.WPAdminEmail = in.WPAdminEmail
	}

	rec, err := s.sites.Create(r.Context(), spec)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "site.create", rec.Domain)

	// v0.2.34: include the WP install summary on success so the panel can
	// surface the admin credentials in a one-shot modal. Nothing else
	// returns the password from the API; it's not stored in cleartext.
	resp := map[string]any{
		"domain": rec.Domain, "user": rec.SiteUser, "type": rec.Type,
		"app": rec.App, "root": rec.RootPath, "status": rec.Status,
		"statusText": rec.StatusText,
	}
	if spec.WPInstall {
		resp["wpInstall"] = map[string]any{
			"adminUser":  spec.WPAdminUser,
			"adminPass":  spec.WPAdminPass,
			"adminEmail": spec.WPAdminEmail,
			"loginUrl":   "https://" + spec.Domain + "/wp-admin/",
			"dbName":     spec.WPDBName,
			"dbUser":     spec.WPDBUser,
			"dbPass":     spec.WPDBPass,
		}
	}
	writeJSON(w, http.StatusCreated, resp)
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

// DELETE /api/sites/{domain}/databases/{engine}/{name}
// v0.2.23: drops a database + its user from the engine and the store. Engine
// is in the path (not the body) so the URL is RESTful and the operation is
// idempotent. We look up the dbUser from the store record before dropping.
func (s *Server) deleteDatabase(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	engine := r.PathValue("engine")
	name := r.PathValue("name")
	dbs, err := s.store.DatabasesForSite(domain)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	var rec *store.Database
	for i := range dbs {
		if dbs[i].Engine == engine && dbs[i].Name == name {
			rec = &dbs[i]
			break
		}
	}
	if rec == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "database not found"})
		return
	}
	if err := s.dbs.Drop(r.Context(), engine, name, rec.DBUser); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "database.delete", engine+":"+name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// POST /api/sites/{domain}/databases/{engine}/{name}/manage
//
// v0.2.25: mint a one-time SSO token, write it to /run/auracp/adminer-sso/
// with the decrypted credentials, and return a URL the browser can open
// to land in Adminer pre-authenticated. The PHP wrapper at /_adminer/ reads
// the token, deletes it (single-use), and seeds Adminer's session with the
// credentials. Token TTL is 60s — long enough to survive a slow browser
// open, short enough that a leaked URL is unusable a minute later.
func (s *Server) manageDatabase(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	engine := r.PathValue("engine")
	name := r.PathValue("name")
	// v0.2.26: pre-flight check — refuse if Adminer wasn't installed (rather
	// than mint a token that 502s when the browser opens the URL). PHP-FPM
	// being absent at install time is the usual cause; tell the operator how
	// to fix it.
	if _, err := os.Stat("/opt/auracp/adminer/index.php"); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "Adminer is not installed on this host. Re-run the installer with PHP enabled: sudo auracp-install --yes --php=yes",
		})
		return
	}
	dbs, err := s.store.DatabasesForSite(domain)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	var rec *store.Database
	for i := range dbs {
		if dbs[i].Engine == engine && dbs[i].Name == name {
			rec = &dbs[i]
			break
		}
	}
	if rec == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "database not found"})
		return
	}
	enc, err := s.store.DatabasePasswordEnc(engine, name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	password, err := s.secret.Decrypt(enc)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	tok, err := auth.RandomToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	dir := "/run/auracp/adminer-sso"
	if mkerr := os.MkdirAll(dir, 0o700); mkerr != nil {
		writeErr(w, http.StatusInternalServerError, mkerr)
		return
	}
	// Token file holds JSON { engine, name, user, password, expires }. The
	// PHP wrapper checks expires before honouring it; auracpd separately
	// sweeps stale tokens every minute (cleanup goroutine, future).
	payload := map[string]any{
		"engine":   engine,
		"name":     name,
		"user":     rec.DBUser,
		"password": password,
		"expires":  time.Now().Add(60 * time.Second).Unix(),
	}
	blob, _ := json.Marshal(payload)
	tokPath := filepath.Join(dir, tok)
	// Mode 0640: readable by www-data (Adminer's PHP-FPM pool group), not
	// world-readable. Owner stays root (auracpd) so the file can't be
	// rewritten from PHP.
	if werr := os.WriteFile(tokPath, blob, 0o640); werr != nil {
		writeErr(w, http.StatusInternalServerError, werr)
		return
	}
	// Chown the token file to root:www-data so PHP-FPM (www-data) can read it.
	if u, lookErr := user.Lookup("www-data"); lookErr == nil {
		gid, _ := strconv.Atoi(u.Gid)
		_ = os.Chown(tokPath, 0, gid)
	}
	s.audit(r, "database.manage", engine+":"+name)
	writeJSON(w, http.StatusOK, map[string]string{
		"url": "/_adminer/?sso=" + tok,
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

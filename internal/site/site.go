// Package site orchestrates the full lifecycle of a hosted site: the Linux
// user, the backend service (PHP-FPM pool, node systemd unit, gunicorn unit),
// the nginx vhost, the TLS cert, and the stored record. It is the single entry
// point the API/CLI call; the per-step work lives in osuser, runtime,
// phpruntime, webserver, and acme. Every step validates input before touching
// the system.
package site

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	"github.com/auracp/auracp/internal/acme"
	"github.com/auracp/auracp/internal/db"
	"github.com/auracp/auracp/internal/noderuntime"
	"github.com/auracp/auracp/internal/osuser"
	"github.com/auracp/auracp/internal/paths"
	"github.com/auracp/auracp/internal/phpruntime"
	"github.com/auracp/auracp/internal/runtime"
	"github.com/auracp/auracp/internal/store"
	"github.com/auracp/auracp/internal/system"
	"github.com/auracp/auracp/internal/validate"
	"github.com/auracp/auracp/internal/webserver"
	"github.com/auracp/auracp/internal/wpinstall"
)

type Manager struct {
	r     *system.Runner
	store *store.Store
	os    *osuser.Manager
	web   *webserver.Manager
	rt    *runtime.Manager
	node  *noderuntime.Manager
	php   *phpruntime.Manager
	acme  *acme.Manager
	// v0.2.51: needed for comprehensive teardown (drops DBs that the
	// site owns, alongside the existing vhost + FPM + user cleanup).
	dbs *db.Manager
}

// New wires the orchestrator. php + acme + dbs may be nil during early
// dev runs; non-nil in real production.
func New(r *system.Runner, st *store.Store, node *noderuntime.Manager, php *phpruntime.Manager, ac *acme.Manager, web *webserver.Manager, dbs *db.Manager) *Manager {
	return &Manager{
		r: r, store: st,
		os:   osuser.New(r),
		web:  web,
		rt:   runtime.New(r),
		node: node,
		php:  php,
		acme: ac,
		dbs:  dbs,
	}
}

// Spec is the create request (validated by Create).
type Spec struct {
	Type      string
	Domain    string
	User      string
	Password  string
	PHPVer    string // php/wordpress
	StartFile string // nodejs
	Module    string // python
	Upstream  string // reverseproxy (user-supplied URL)
	NodeVer   string // nodejs: pin to this Node runtime ("" or "default" = managed default)
	UsePM2    bool   // nodejs: run via pm2-runtime

	// v0.2.34: WordPress one-click auto-install. When Type=wordpress and
	// WPInstall=true, Create runs wp-cli end-to-end after the site backend
	// + vhost are up. The DB itself is provisioned by the API handler
	// before calling Create (so the handler can return useful errors per
	// step); we just consume the DB creds + the admin info here.
	WPInstall    bool
	WPDBName     string
	WPDBUser     string
	WPDBPass     string
	WPTitle      string
	WPAdminUser  string
	WPAdminPass  string
	WPAdminEmail string
}

// WPResult is the post-install info the API echoes back to the panel once a
// WordPress auto-install completes — DB creds the operator may want to
// record, plus the admin password (returned once; never stored cleartext).
type WPResult struct {
	DBName     string
	DBUser     string
	DBPassword string
	AdminUser  string
	AdminPass  string
	URL        string
}

func hasBackend(t string) bool {
	switch t {
	case "php", "wordpress", "nodejs", "python":
		return true
	}
	return false
}

// Create provisions a new site end-to-end and records it. On a failure mid-way
// it rolls back the resources it already created.
func (m *Manager) Create(ctx context.Context, s Spec) (store.Site, error) {
	if err := validate.SiteType(s.Type); err != nil {
		return store.Site{}, err
	}
	if err := validate.Domain(s.Domain); err != nil {
		return store.Site{}, err
	}
	if err := validate.Username(s.User); err != nil {
		return store.Site{}, err
	}

	// Resolve backend port / upstream. PHP sites use a unix socket — no port.
	var port int
	var upstream string
	switch {
	case s.Type == "reverseproxy":
		if s.Upstream == "" {
			return store.Site{}, fmt.Errorf("reverse proxy requires an upstream URL")
		}
		upstream = s.Upstream
	case s.Type == "php" || s.Type == "wordpress":
		if err := validate.PHPVersion(s.PHPVer); err != nil {
			return store.Site{}, err
		}
		// upstream stays empty; the webserver renderer wires fastcgi_pass to
		// paths.PHPSocket(domain) based on the site type, not the upstream field.
	case s.Type == "nodejs" || s.Type == "python":
		p, err := m.store.NextPort()
		if err != nil {
			return store.Site{}, err
		}
		port = p
		upstream = paths.Upstream(port)
	}

	// 1) Linux user + dirs + SFTP jail
	if err := m.os.Create(ctx, s.User, s.Domain); err != nil {
		return store.Site{}, err
	}
	rollback := func() { _ = m.os.Delete(ctx, s.User) }
	if s.Password != "" {
		_ = m.os.SetPassword(ctx, s.User, s.Password)
	}

	// 2) backend
	switch {
	case s.Type == "php" || s.Type == "wordpress":
		if m.php == nil {
			rollback()
			return store.Site{}, fmt.Errorf("PHP-FPM runtime manager not configured")
		}
		if err := m.php.WritePool(ctx, s.PHPVer, s.Domain, s.User); err != nil {
			rollback()
			return store.Site{}, err
		}
	case s.Type == "nodejs":
		if s.UsePM2 && m.node != nil {
			if err := m.node.EnsurePM2(ctx, s.NodeVer); err != nil {
				rollback()
				return store.Site{}, err
			}
		}
		if err := m.rt.Apply(ctx, runtime.Spec{
			Type: s.Type, Domain: s.Domain, User: s.User, Port: port,
			StartFile: s.StartFile, NodeVer: s.NodeVer, UsePM2: s.UsePM2,
		}); err != nil {
			rollback()
			return store.Site{}, err
		}
	case s.Type == "python":
		if err := m.rt.Apply(ctx, runtime.Spec{
			Type: s.Type, Domain: s.Domain, User: s.User, Port: port, Module: s.Module,
		}); err != nil {
			rollback()
			return store.Site{}, err
		}
	}

	// 3) nginx vhost + reload (HTTP-only initially; the ACME challenge location
	//    is rendered into every vhost so the first issuance can complete here).
	if err := m.web.Apply(ctx, webserver.Spec{
		Type: s.Type, Domain: s.Domain, User: s.User, Upstream: upstream, PHPVer: s.PHPVer,
	}); err != nil {
		m.cleanupBackend(ctx, s)
		rollback()
		return store.Site{}, err
	}

	// 4) record desired state
	rec := store.Site{
		Type: s.Type, Domain: s.Domain, SiteUser: s.User,
		RootPath: paths.DocRoot(s.User, s.Domain), App: appLabel(s),
		Port: port, Upstream: upstream, PHPVersion: s.PHPVer,
		PM2Enabled: s.UsePM2,
		Status:     "up", StatusText: "Online",
	}
	// NodeVersion is the version PINNED for this site's runtime. Only Node
	// sites have one; other types (PHP/WordPress/Python/Static/ReverseProxy)
	// run on their own stacks and should never display a Node tag in the UI.
	// (Earlier v0.2.x had a 'NodeReady' fallback that tagged everything with
	// Node 24 — left a 'node 24' badge on WordPress sites in the list.)
	if s.Type == "nodejs" {
		v := s.NodeVer
		if v == "" {
			v = "default"
		}
		rec.NodeVersion = sql.NullString{String: v, Valid: true}
	}
	if err := m.store.CreateSite(rec); err != nil {
		return store.Site{}, err
	}

	// v0.2.34: WordPress one-click auto-install. Runs AFTER the site record
	// is persisted so a wp-cli failure leaves a recoverable state — the
	// docroot may be half-populated but the site exists in the UI, so the
	// operator can either continue manually via SFTP or delete the site
	// outright. The DB creds were created by the API handler before us.
	if s.WPInstall && s.Type == "wordpress" {
		url := "https://" + s.Domain
		if err := wpinstall.Install(ctx, m.r, wpinstall.Spec{
			Domain:     s.Domain,
			SiteUser:   s.User,
			DBHost:     "localhost",
			DBName:     s.WPDBName,
			DBUser:     s.WPDBUser,
			DBPass:     s.WPDBPass,
			URL:        url,
			Title:      s.WPTitle,
			AdminUser:  s.WPAdminUser,
			AdminPass:  s.WPAdminPass,
			AdminEmail: s.WPAdminEmail,
		}); err != nil {
			log.Printf("site %s: wp install: %v", s.Domain, err)
			return rec, fmt.Errorf("wordpress auto-install failed: %w (site record kept; drop the site to retry)", err)
		}
	}

	// 5) issue cert in the background — non-fatal: site keeps working on :80
	//    until LE comes through, and the renewal loop will retry on failure.
	if m.acme != nil {
		go func() {
			bg := context.Background()
			if err := m.acme.EnsureCert(bg, s.Domain); err != nil {
				log.Printf("site %s: initial cert issuance failed: %v", s.Domain, err)
				return
			}
			// Re-render the vhost so the HTTPS server{} block points at the new
			// cert. v0.2.23: also read site_config so the user's vhost_override
			// (and any feature toggles like cache/basic_auth) survive the
			// cert-issuance re-render — previously this Spec only had Type+Domain
			// fields and would wipe edits made between Create and the first cert.
			cert, _ := m.store.Certificate(s.Domain)
			cfg, _ := m.store.SiteConfig(s.Domain)
			spec := webserver.Spec{
				Type: rec.Type, Domain: rec.Domain, User: rec.SiteUser,
				Root: rec.RootPath, Upstream: rec.Upstream, PHPVer: rec.PHPVersion,
				Cache: cfg["cache"] == "true", CacheTTL: cfg["cache_ttl"],
				BlockBots: cfg["block_bots"] == "true",
				Override:  cfg["vhost_override"],
			}
			if cfg["basic_auth"] == "true" {
				spec.BasicAuthUser = cfg["basic_auth_user"]
				spec.BasicAuthHash = cfg["basic_auth_hash"]
			}
			if cert.CertPath.Valid {
				spec.CertPath = cert.CertPath.String
				spec.KeyPath = cert.KeyPath.String
			}
			if err := m.web.Apply(bg, spec); err != nil {
				log.Printf("site %s: vhost re-render after cert: %v", s.Domain, err)
			}
		}()
	}
	return rec, nil
}

// cleanupBackend is the create-time rollback helper for the per-type backend
// step. Best-effort; logs nothing.
func (m *Manager) cleanupBackend(ctx context.Context, s Spec) {
	switch s.Type {
	case "php", "wordpress":
		if m.php != nil {
			_ = m.php.RemovePool(ctx, s.Domain)
		}
	case "nodejs", "python":
		_ = m.rt.Remove(ctx, s.Domain)
	}
}

// ReapplyRuntime re-renders & restarts a site's backend (e.g. after the
// operator changes its pinned Node/PHP version, or per-site PHP settings).
// No-op for site types without a backend (static / reverseproxy).
func (m *Manager) ReapplyRuntime(ctx context.Context, domain string) error {
	st, err := m.store.SiteByDomain(domain)
	if err != nil {
		return err
	}
	if !hasBackend(st.Type) {
		return nil
	}
	switch st.Type {
	case "php", "wordpress":
		if m.php == nil {
			return fmt.Errorf("PHP-FPM runtime manager not configured")
		}
		return m.php.WritePool(ctx, st.PHPVersion, st.Domain, st.SiteUser)
	case "nodejs":
		if st.PM2Enabled && m.node != nil {
			if err := m.node.EnsurePM2(ctx, st.NodeVersion.String); err != nil {
				return err
			}
		}
		return m.rt.Apply(ctx, runtime.Spec{
			Type: st.Type, Domain: domain, User: st.SiteUser, Port: st.Port,
			NodeVer: st.NodeVersion.String, UsePM2: st.PM2Enabled,
		})
	case "python":
		return m.rt.Apply(ctx, runtime.Spec{
			Type: st.Type, Domain: domain, User: st.SiteUser, Port: st.Port,
		})
	}
	return nil
}

// Delete tears a site down COMPLETELY: nginx vhost, backend service,
// every associated database, every cron job, every extra SSH/FTP user,
// every backup file, the primary Linux user (which drops the home
// dir + crontab + files), and every related store row.
//
// v0.2.51 expanded scope. Prior versions silently left orphan rows in
// site_config / cron_jobs / databases / ssh_users / backups whenever a
// site was deleted — those came back as "ghost cron" / "DB shown in
// UI" / "stale backup eating disk" bugs. The comprehensive teardown
// runs through internal/site/teardown.go::Teardown which is shared
// with the new-pipeline path (api/sites_creator.go) so both delete
// flows behave identically.
//
// Failure mode: best-effort on every step; the first error is returned
// at the end but the function continues through subsequent steps to
// minimize residual state. Each step logs its own outcome to journald.
func (m *Manager) Delete(ctx context.Context, domain string) error {
	st, err := m.store.SiteByDomain(domain)
	if err != nil {
		return err
	}

	// Step A: filesystem + system teardown (vhost, FPM pool / systemd
	// unit, primary Linux user). Same as before — the on-disk artifacts
	// that the legacy path always handled.
	if err := m.web.Remove(ctx, domain); err != nil {
		return err
	}
	switch st.Type {
	case "php", "wordpress":
		if m.php != nil {
			_ = m.php.RemovePool(ctx, domain)
		}
	case "nodejs", "python":
		if err := m.rt.Remove(ctx, domain); err != nil {
			return err
		}
	}
	if err := m.os.Delete(ctx, st.SiteUser); err != nil {
		return err
	}

	// Step B: comprehensive teardown of EVERYTHING else (databases,
	// extra users, backups, every store row). Shared with the new
	// pipeline so the two delete code paths have identical semantics.
	return Teardown(ctx, &TeardownDeps{
		R:     m.r,
		Store: m.store,
		DBs:   m.dbs,
		OS:    m.os,
	}, domain)
}

func appLabel(s Spec) string {
	switch s.Type {
	case "wordpress":
		return "WordPress"
	case "php":
		return "PHP " + s.PHPVer
	case "nodejs":
		return "Node.js"
	case "python":
		return "Python 3"
	case "static":
		return "Static"
	case "reverseproxy":
		return "Reverse Proxy"
	}
	return s.Type
}

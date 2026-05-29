// Command auracpd is the auraCP control-plane daemon: it serves the admin UI
// and JSON API, and provisions sites, databases, certs (in-process ACME).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net"
	"net/http"
	"runtime"
	"time"

	"github.com/auracp/auracp/internal/acme"
	"github.com/auracp/auracp/internal/api"
	dbadminintegration "github.com/auracp/auracp/internal/api/dbadmin"
	dbadminwebui "github.com/auracp/auracp/internal/dbadmin/webui"
	"github.com/auracp/auracp/internal/backup"
	"github.com/auracp/auracp/internal/cron"
	"github.com/auracp/auracp/internal/db"
	"github.com/auracp/auracp/internal/noderuntime"
	"github.com/auracp/auracp/internal/osuser"
	"github.com/auracp/auracp/internal/perm"
	"github.com/auracp/auracp/internal/phpruntime"
	"github.com/auracp/auracp/internal/secret"
	"github.com/auracp/auracp/internal/site/creator"
	"github.com/auracp/auracp/internal/store"
	"github.com/auracp/auracp/internal/system"
	"github.com/auracp/auracp/internal/updater"
	"github.com/auracp/auracp/internal/webserver"
	"github.com/auracp/auracp/internal/webui"
)

// version is injected at build time via -ldflags "-X main.version=…" from the
// Makefile. The updater package reports this back to the panel UI and uses it
// to decide whether a newer GitHub release is worth flagging.
var version = "dev"

func main() {
	addr := flag.String("addr", ":8443", "listen address")
	dbPath := flag.String("db", "auracp.db", "path to the SQLite state database")
	etcDir := flag.String("etc", "/etc/auracp", "config dir (holds the secret key + ACME state)")
	provision := flag.Bool("provision", runtime.GOOS == "linux",
		"actually provision the OS (users/services/nginx); off = record-only (dev)")
	useTLS := flag.Bool("tls", true, "serve HTTPS with a self-signed cert (panel.crt/key in -etc); off = plain HTTP")
	panelDomain := flag.String("panel-domain", "", "front the panel under this domain via nginx (real Let's Encrypt cert on :443)")
	acmeStaging := flag.Bool("acme-staging", false, "use Let's Encrypt staging endpoint (no rate limits; certs are not browser-trusted)")
	flag.Parse()

	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	// v0.2.21: hydrate per-role permission overrides from the settings table
	// into perm's in-memory map. Persistence is one row per role
	// (role_perm_<role>); empty value = no override. ROLE_ADMIN is ignored.
	if all, err := st.AllSettings(); err == nil {
		for _, role := range []string{"ROLE_SITE_MANAGER", "ROLE_USER"} {
			blob := all["role_perm_"+role]
			if blob == "" {
				continue
			}
			var s perm.Set
			if jerr := json.Unmarshal([]byte(blob), &s); jerr == nil && s != nil {
				perm.SetOverride(role, s)
			}
		}
	}

	sec, err := secret.Open(*etcDir)
	if err != nil {
		log.Fatalf("secret: %v", err)
	}

	runner := system.New()
	runner.DryRun = !*provision
	if !*provision {
		log.Printf("provisioning DISABLED (record-only mode); OS commands are logged, not run")
	}

	// Loopback URL nginx proxies to when the panel is fronted under a domain.
	scheme := "https"
	if !*useTLS {
		scheme = "http"
	}
	_, port, err := net.SplitHostPort(*addr)
	if err != nil || port == "" {
		port = "8443"
	}
	panelBackend := scheme + "://127.0.0.1:" + port

	web := webserver.New(runner)
	// v0.2.38: install the default-server vhost (drops unmatched HTTPS,
	// 444s unmatched HTTP). Idempotent — only writes + reloads nginx when
	// the conf is stale or missing. Without this, sites whose cert is
	// still being issued fall back to the panel server block when a
	// browser hits them over HTTPS.
	if err := web.ApplyCatchAll(context.Background()); err != nil {
		log.Printf("catch-all default vhost: %v (sites still being provisioned may show the panel until lego issues their cert)", err)
	}
	node := noderuntime.New(runner, st)
	node.Reconcile()
	php := phpruntime.New(runner, st)
	php.Reconcile()
	// v0.2.62: osuser manager is needed both by the startup heal below
	// AND by the API server. Construct it once here; pass to both.
	osu := osuser.New(runner)

	// v0.2.58: self-heal nginx state on boot. Two-step:
	//   1. PruneDeadVhosts removes vhost files whose site user no
	//      longer exists in /etc/passwd. Catches partial-cleanup state
	//      from pre-v0.2.55 (rollback wasn't yet on the create path),
	//      and from operator-reported "deleted site, vhost lingered"
	//      bugs that v0.2.51 already mostly closed but the on-disk
	//      heal here guarantees nginx -t can't be poisoned by a leak.
	//   2. EnsureLogDirsForEnabledVhosts mkdir's any /home/<u>/logs
	//      directory a SURVIVING vhost references that's somehow gone
	//      from disk (manual rm, snapshot-restore artifacts, etc.).
	// Both are cheap (filesystem walk + a few mkdir's at worst) and
	// idempotent — nothing to do on a clean panel. The payoff: every
	// future site-create's nginx -t passes regardless of how the
	// system arrived at its current state.
	creator.PruneDeadVhosts()
	creator.EnsureLogDirsForEnabledVhosts(context.Background(), runner)

	// v0.2.62: ensure nginx's www-data is a member of every site user's
	// group. Without it, ResetPermissions' chmod-750 on /home/<user>
	// blocks nginx workers from `stat()`'ing the docroot — operator-
	// reported failure mode:
	//   [crit] stat() "/home/<u>/htdocs/<d>/" failed (13: Permission denied)
	// surfaces to the visitor as HTTP 404. Idempotent gpasswd -a calls
	// + a single nginx reload at the end pick up the new group memberships
	// for newly-spawned worker processes. Existing workers stay until
	// the reload re-execs them, after which group resolution refreshes.
	if sites, err := st.Sites(); err == nil && len(sites) > 0 {
		users := make([]string, 0, len(sites))
		seen := map[string]bool{}
		for _, s := range sites {
			if !seen[s.SiteUser] {
				users = append(users, s.SiteUser)
				seen[s.SiteUser] = true
			}
		}
		n := osu.EnsureNginxAccess(context.Background(), users)
		if n > 0 {
			log.Printf("nginx access: www-data added to %d site user group(s); reloading nginx", n)
			_, _ = runner.Run(context.Background(), "systemctl", "reload", "nginx")
		}
	}

	// Self-update checker. The Manager owns a 1h cache; a goroutine refreshes
	// every 12h so the UI never blocks on api.github.com. Honours the version
	// injected by the Makefile's -ldflags -X main.version=…
	upd := updater.New(version)
	go func() {
		// Initial fire-and-forget probe a few seconds after startup so the
		// dashboard's update card has a value to show right away.
		time.Sleep(5 * time.Second)
		_ = upd.Refresh(context.Background())
		t := time.NewTicker(12 * time.Hour)
		defer t.Stop()
		for range t.C {
			_ = upd.Refresh(context.Background())
		}
	}()

	// ACME owns LE issuance + renewal; nginx reload happens after each issuance.
	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// v0.2.41: acme.Manager gets the secret box so it can decrypt the
	// instance Cloudflare API token + fall back to DNS-01 when HTTP-01
	// can't reach the domain (typically because it's CF-proxied).
	ac := acme.New(st, *etcDir, web.Reload, sec)
	ac.SetStaging(*acmeStaging)
	ac.StartRenewalLoop(rootCtx)

	// Reconcile the panel domain: persist the flag (if given), then (re)apply
	// the nginx front for whatever domain is configured + kick off cert issuance.
	if *panelDomain != "" {
		_ = st.SetSetting("panel_domain", *panelDomain)
	}
	if d, ok := st.GetSetting("panel_domain"); ok && d != "" {
		if err := web.ApplyPanelProxy(rootCtx, d, panelBackend); err != nil {
			log.Printf("panel domain %q: %v", d, err)
		} else {
			log.Printf("panel fronted at https://%s (auracpd will obtain its Let's Encrypt cert)", d)
		}
		// Background cert issuance for the panel itself.
		go func() {
			if err := ac.EnsureCert(rootCtx, d); err != nil {
				log.Printf("panel domain %q: cert: %v", d, err)
				return
			}
			if err := web.ApplyPanelProxy(rootCtx, d, panelBackend); err != nil {
				log.Printf("panel domain %q: re-render after cert: %v", d, err)
			}
		}()
	}

	mux := http.NewServeMux()
	dbs := db.New(runner, st, sec)
	api.Register(mux, st, api.Deps{
		// v0.2.52: site.Manager.New is gone — every site-lifecycle
		// operation runs through internal/site/creator. cmd/auracpd's
		// job is to construct the leaf managers (db, php, node, acme,
		// web, runner) and pass them through Deps; the creator package
		// composes them per-request.
		DBs:          dbs,
		Cron:         cron.New(runner, st),
		Backups:      backup.New(runner, st),
		Web:          web,
		OS:           osu,
		Node:         node,
		PHP:          php,
		ACME:         ac,
		Updater:      upd,
		Secret:       sec,
		Runner:       runner,
		PanelBackend: panelBackend,
	}) // /api/*

	// PR #10: Aura DB — modern DB admin UI mounted alongside legacy
	// Adminer. Adminer continues to be served by nginx at /_adminer/
	// (untouched). The dbadmin engine takes its identity from the panel
	// session cookie via ResolveIdentity (FIX-7 / INT-11: surfaces only
	// UserID/Email/Role/MFA — no PasswordHash, no TOTPSecret) and uses
	// the panel's secret.Box for credential encryption at rest.
	dbaCfg := dbadminintegration.LoadFromStore(st)
	dbaEngine, dbaCloser, err := dbadminintegration.Mount(mux, st, sec,
		func(r *http.Request) (api.IdentitySummary, bool) { return api.ResolveIdentity(st, r) },
		dbaCfg)
	if err != nil {
		log.Fatalf("dbadmin mount: %v", err)
	}
	defer func() { _ = dbaCloser.Close() }()
	// Engine shutdown joins the daemon's graceful shutdown context.
	go func() {
		<-rootCtx.Done()
		_ = dbaEngine.Shutdown(context.Background())
	}()

	// PR #11: Aura DB Svelte SPA shell. Sibling Vite build embedded under
	// /dbadmin/. Mounted BEFORE the panel "/" catch-all so requests with
	// the /dbadmin/ prefix route to the DB workstation rather than the
	// panel. Cohabits with /api/dbadmin/ (JSON API) on the same eTLD so
	// the auracp_csrf + auracp_session cookies cross-mount automatically.
	mux.Handle("/dbadmin/", http.StripPrefix("/dbadmin", dbadminwebui.Handler()))
	mux.Handle("/dbadmin", http.RedirectHandler("/dbadmin/", http.StatusMovedPermanently))

	mux.Handle("/", webui.Handler()) // embedded SPA (catch-all)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           api.Secure(mux), // security headers + CSRF + rate-limit
		ReadHeaderTimeout: 10 * time.Second,
	}

	if *useTLS {
		certPath, keyPath, err := webui.EnsureSelfSignedCert(*etcDir)
		if err != nil {
			log.Fatalf("tls cert: %v", err)
		}
		ln, err := net.Listen("tcp", *addr)
		if err != nil {
			log.Fatalf("listen: %v", err)
		}
		// Plain HTTP requests on the TLS port get 301'd to https://, so users
		// who type http://… don't see a TLS handshake error.
		tlsLn, httpLn := splitTLSAndHTTP(ln)
		go func() {
			redir := &http.Server{Handler: http.HandlerFunc(httpsRedirect), ReadHeaderTimeout: 5 * time.Second}
			_ = redir.Serve(httpLn)
		}()
		log.Printf("auracpd listening on https://%s (db: %s)", *addr, *dbPath)
		if err := srv.ServeTLS(tlsLn, certPath, keyPath); err != nil {
			log.Fatalf("server: %v", err)
		}
		return
	}
	log.Printf("auracpd listening on http://%s (db: %s)", *addr, *dbPath)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server: %v", err)
	}
}

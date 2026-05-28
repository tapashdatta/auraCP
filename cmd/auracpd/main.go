// Command auracpd is the auraCP control-plane daemon: it serves the admin UI
// and JSON API, and provisions sites, databases, certs (in-process ACME).
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"runtime"
	"time"

	"github.com/auracp/auracp/internal/acme"
	"github.com/auracp/auracp/internal/api"
	"github.com/auracp/auracp/internal/backup"
	"github.com/auracp/auracp/internal/cron"
	"github.com/auracp/auracp/internal/db"
	"github.com/auracp/auracp/internal/noderuntime"
	"github.com/auracp/auracp/internal/osuser"
	"github.com/auracp/auracp/internal/phpruntime"
	"github.com/auracp/auracp/internal/secret"
	"github.com/auracp/auracp/internal/site"
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
	node := noderuntime.New(runner, st)
	node.Reconcile()
	php := phpruntime.New(runner, st)
	php.Reconcile()

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
	ac := acme.New(st, *etcDir, web.Reload)
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
	api.Register(mux, st, api.Deps{
		Sites:        site.New(runner, st, node, php, ac, web),
		DBs:          db.New(runner, st, sec),
		Cron:         cron.New(runner, st),
		Backups:      backup.New(runner, st),
		Web:          web,
		OS:           osuser.New(runner),
		Node:         node,
		PHP:          php,
		ACME:         ac,
		Updater:      upd,
		Secret:       sec,
		Runner:       runner,
		PanelBackend: panelBackend,
	}) // /api/*
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

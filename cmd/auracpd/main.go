// Command auracpd is the auraCP control-plane daemon: it serves the admin UI
// and JSON API, and provisions sites, databases, and certs.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"runtime"
	"time"

	"github.com/auracp/auracp/internal/api"
	"github.com/auracp/auracp/internal/backup"
	"github.com/auracp/auracp/internal/cron"
	"github.com/auracp/auracp/internal/db"
	"github.com/auracp/auracp/internal/noderuntime"
	"github.com/auracp/auracp/internal/osuser"
	"github.com/auracp/auracp/internal/secret"
	"github.com/auracp/auracp/internal/site"
	"github.com/auracp/auracp/internal/store"
	"github.com/auracp/auracp/internal/system"
	"github.com/auracp/auracp/internal/webserver"
	"github.com/auracp/auracp/internal/webui"
)

func main() {
	addr := flag.String("addr", ":8443", "listen address")
	dbPath := flag.String("db", "auracp.db", "path to the SQLite state database")
	etcDir := flag.String("etc", "/etc/auracp", "config dir (holds the secret key + panel cert)")
	provision := flag.Bool("provision", runtime.GOOS == "linux",
		"actually provision the OS (users/services/caddy); off = record-only (dev)")
	useTLS := flag.Bool("tls", true, "serve HTTPS with a self-signed cert (panel.crt/key in -etc); off = plain HTTP")
	panelDomain := flag.String("panel-domain", "", "front the panel under this domain via Caddy (real Let's Encrypt cert on :443)")
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

	// Loopback URL Caddy proxies to when the panel is fronted under a domain.
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
	node.ReconcileDefaultSymlink()

	// Reconcile the panel domain: persist the flag (if given), then (re)apply the
	// Caddy front for whatever domain is configured — this triggers Caddy's
	// automatic Let's Encrypt issuance once DNS points here.
	if *panelDomain != "" {
		_ = st.SetSetting("panel_domain", *panelDomain)
	}
	if d, ok := st.GetSetting("panel_domain"); ok && d != "" {
		if err := web.ApplyPanelProxy(context.Background(), d, panelBackend); err != nil {
			log.Printf("panel domain %q: %v", d, err)
		} else {
			log.Printf("panel fronted at https://%s (Caddy will obtain its certificate)", d)
		}
	}

	mux := http.NewServeMux()
	api.Register(mux, st, api.Deps{
		Sites:        site.New(runner, st, node),
		DBs:          db.New(runner, st, sec),
		Cron:         cron.New(runner, st),
		Backups:      backup.New(runner, st),
		Web:          web,
		OS:           osuser.New(runner),
		Node:         node,
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

// v0.2.48: feature-flagged dispatch into the new creator pipeline.
//
// This file is the only call site of internal/site/creator. It will
// absorb the site.Manager.Create logic incrementally — by v0.2.49 it
// becomes the only path and api.go's `createSiteViaNewPipeline` call
// drops the env-var gate.
//
// What this function does that's different from the legacy
// site.Manager.Create:
//
//   - Filesystem work is delegated to creator.RunCreate, which runs
//     Preflight upfront (zero writes if validation fails) and a smoke
//     probe at the end (refuses to mark status=active on empty body).
//   - Structured slog at every step ("step ok"/"step failed" with the
//     site + duration) — journalctl -u auracpd | grep <domain> gives
//     the post-mortem timeline.
//   - ACME issuance + vhost re-render after cert lands happens here
//     (extracted from site.Manager.Create so the new path doesn't
//     depend on the legacy code).
//
// What's the SAME:
//
//   - The Spec/Site request body format (zero UI changes)
//   - The store.Site persistence shape
//   - The WP-install hand-off (creds are pre-provisioned by the
//     caller in createSite())
//   - The 201 Created response shape
package api

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"log/slog"

	"github.com/auracp/auracp/internal/paths"
	"github.com/auracp/auracp/internal/runtime"
	"github.com/auracp/auracp/internal/site"
	"github.com/auracp/auracp/internal/site/creator"
	"github.com/auracp/auracp/internal/store"
	"github.com/auracp/auracp/internal/wpinstall"
)

// createSiteViaNewPipeline builds a creator.Spec from the API input
// and runs the full create pipeline. Returns the persisted store.Site
// or an error.
//
// v0.2.52: legacy site.Spec is gone; the WP DB creds the handler
// generated (createSiteInput.WPDBName/User/Pass — `json:"-"` fields)
// flow straight into creator.Spec.
func (s *Server) createSiteViaNewPipeline(ctx context.Context, in createSiteInput) (store.Site, error) {
	// 1. Build creator.Spec from the API input.
	cspec := &creator.Spec{
		Type:         in.Type,
		Domain:       in.Domain,
		User:         in.SiteUser,
		Password:     in.Password,
		PHPVersion:   in.PHPVersion,
		NodeVersion:  in.NodeVersion,
		StartFile:    in.StartFile,
		UsePM2:       in.PM2,
		Module:       in.Module,
		Upstream:     in.Upstream,
		WPInstall:    in.WPInstall,
		WPDBName:     in.WPDBName,
		WPDBUser:     in.WPDBUser,
		WPDBPass:     in.WPDBPass,
		WPTitle:      in.WPTitle,
		WPAdminUser:  in.WPAdminUser,
		WPAdminPass:  in.WPAdminPass,
		WPAdminEmail: in.WPAdminEmail,
	}

	// 2. Run the pipeline. Preflight + ordered steps + smoke probe.
	// Every per-type manager is passed in (php for PHP, rt+node for
	// Node/Python, store for port allocation, runner for the rest).
	// Per-type Run() picks the ones it needs and errors if a required
	// manager is nil.
	// runtime.Manager is stateless (only holds the runner reference),
	// so constructing it on demand here is free and avoids adding
	// another field to Server just for the creator path. When v0.2.49
	// retires the legacy site.Manager.Create, this construction moves
	// up to Server.New so it's amortised across requests.
	deps := &creator.Deps{
		R:     s.runner,
		Php:   s.php,
		Rt:    runtime.New(s.runner),
		Node:  s.node,
		Store: s.store,
	}
	if err := creator.RunCreate(ctx, cspec, deps); err != nil {
		return store.Site{}, err
	}

	// 3. Persist the store record. Shape matches what site.Manager.Create
	// produced so the rest of the API (list, detail, settings) sees the
	// same fields it always has.
	rec := store.Site{
		Type:       in.Type,
		Domain:     in.Domain,
		SiteUser:   in.SiteUser,
		RootPath:   paths.DocRoot(in.SiteUser, in.Domain),
		App:        creatorAppLabel(in.Type, in.PHPVersion),
		PHPVersion: in.PHPVersion,
		Port:       cspec.AppPort, // populated by AllocatePort for node/python; 0 otherwise
		Upstream:   in.Upstream,   // reverseproxy only — empty for others
		PM2Enabled: in.PM2,
		Status:     "up",
		StatusText: "Online",
	}
	if in.Type == "nodejs" {
		v := in.NodeVersion
		if v == "" {
			v = "default"
		}
		rec.NodeVersion = sql.NullString{String: v, Valid: true}
	}
	// For Node/Python sites, also record the loopback upstream so the
	// vhost re-render path (cert post-issuance, settings save) sees it.
	if in.Type == "nodejs" || in.Type == "python" {
		rec.Upstream = paths.Upstream(cspec.AppPort)
	}
	if err := s.store.CreateSite(rec); err != nil {
		return store.Site{}, fmt.Errorf("persist site record: %w", err)
	}

	// 4. WP install (synchronous — same as legacy path). DB creds were
	// pre-provisioned by the caller (createSite handler in api.go).
	if cspec.WPInstall && in.Type == "wordpress" {
		err := wpinstall.Install(ctx, s.runner, wpinstall.Spec{
			Domain:     cspec.Domain,
			SiteUser:   cspec.User,
			DBHost:     "localhost",
			DBName:     cspec.WPDBName,
			DBUser:     cspec.WPDBUser,
			DBPass:     cspec.WPDBPass,
			URL:        "https://" + cspec.Domain,
			Title:      cspec.WPTitle,
			AdminUser:  cspec.WPAdminUser,
			AdminPass:  cspec.WPAdminPass,
			AdminEmail: cspec.WPAdminEmail,
		})
		if err != nil {
			slog.Default().With("site", cspec.Domain).
				Error("wp-cli install failed; site record kept",
					"err", err.Error())
			return rec, fmt.Errorf("wordpress auto-install failed: %w (site record kept; delete the site to retry)", err)
		}
	}

	// 5. ACME issuance — background, non-fatal. The site is reachable
	// HTTP-only until the cert lands; the renewal loop will retry on
	// failure (12h tick with jitter — see internal/acme).
	if s.acme != nil {
		go s.issueCertAndReapply(rec)
	}

	return rec, nil
}

// issueCertAndReapply runs in a goroutine after createSiteViaNewPipeline.
// Once lego writes the cert files at /etc/auracp/ssl/<domain>.{crt,key},
// we re-render the vhost so the `ssl_certificate` directives are filled
// in (the initial render had them empty — HTTP-only fallback).
//
// v0.2.52: now uses creator.RunReapply (which reads store state +
// detects cert files on disk) instead of the legacy webserver.Apply.
// No more dual-renderer surface; the new pipeline owns every vhost
// write across the entire site lifecycle.
func (s *Server) issueCertAndReapply(rec store.Site) {
	bg := context.Background()
	if err := s.acme.EnsureCert(bg, rec.Domain); err != nil {
		log.Printf("site %s: initial cert issuance failed: %v", rec.Domain, err)
		return
	}
	deps := &creator.Deps{
		R:     s.runner,
		Php:   s.php,
		Rt:    runtime.New(s.runner),
		Node:  s.node,
		Store: s.store,
	}
	if err := creator.RunReapply(bg, rec.Domain, deps); err != nil {
		log.Printf("site %s: vhost re-render after cert: %v", rec.Domain, err)
	}
}

// deleteSiteViaNewPipeline runs creator.RunDelete + tears down the
// database-side records (certificates, site_config, php_settings, the
// site row itself). The on-disk teardown is creator's job; everything
// involving s.store is OUR job — separation of concerns mirrors what
// site.Manager.Delete did, but with the cross-PHP-version pool sweep
// now front-loaded into creator.RunDelete.
func (s *Server) deleteSiteViaNewPipeline(ctx context.Context, domain string) error {
	st, err := s.store.SiteByDomain(domain)
	if err != nil {
		return err
	}
	deps := &creator.Deps{
		R:     s.runner,
		Php:   s.php,
		Rt:    runtime.New(s.runner),
		Node:  s.node,
		Store: s.store,
	}
	// Filesystem teardown first — if it fails, we keep the DB record so
	// the operator can retry. Reversed order would orphan files.
	if err := creator.RunDelete(ctx, &creator.DeleteSpec{
		Domain: st.Domain,
		User:   st.SiteUser,
	}, deps); err != nil {
		return err
	}
	// Backend service teardown for Node/Python — handled by runtime.Manager
	// (separate concern from RunDelete's nginx+pool sweep). RunDelete
	// doesn't touch systemd units today; v0.2.49 absorbs this branch.
	if st.Type == "nodejs" || st.Type == "python" {
		_ = deps.Rt.Remove(ctx, domain)
	}

	// v0.2.51: comprehensive teardown — drops every database the site
	// owned, removes extra SFTP/SSH users, sweeps backup files, and
	// purges every store row associated with the domain. Identical
	// semantics to the legacy path (site.Manager.Delete also calls
	// site.Teardown). Without this, deleting a site through the new
	// pipeline left orphan rows in site_config / cron_jobs / databases
	// / ssh_users / backups, which surfaced later as ghost cron jobs
	// and "DB shown in UI but file gone" bugs.
	return site.Teardown(ctx, &site.TeardownDeps{
		R:     s.runner,
		Store: s.store,
		DBs:   s.dbs,
		OS:    s.osu,
	}, domain)
}

// reapplyRuntime is the API-layer helper that builds the creator.Deps
// once and dispatches to creator.RunReapplyRuntime. Avoids repeating
// the Deps boilerplate at every call site (4 of them across
// noderuntime.go + phpruntime.go).
//
// v0.2.52: replaces every s.sites.ReapplyRuntime call.
func (s *Server) reapplyRuntime(ctx context.Context, domain string) error {
	return creator.RunReapplyRuntime(ctx, domain, &creator.Deps{
		R:     s.runner,
		Php:   s.php,
		Rt:    runtime.New(s.runner),
		Node:  s.node,
		Store: s.store,
	})
}

// creatorAppLabel returns the UI label for a freshly-created site.
// Mirrors site.appLabel — duplicated here so this file doesn't import
// the legacy site package's private helper.
func creatorAppLabel(siteType, phpVer string) string {
	switch siteType {
	case "wordpress":
		return "WordPress"
	case "php":
		return "PHP " + phpVer
	case "nodejs":
		return "Node.js"
	case "python":
		return "Python 3"
	case "static":
		return "Static"
	case "reverseproxy":
		return "Reverse Proxy"
	}
	return siteType
}

// createSiteInput is the request body shape declared inside the
// createSite handler. Lifted here as a named type so this file can
// reference it cleanly.
//
// v0.2.52: the WP DB credentials computed by the createSite handler
// (DBName/DBUser/DBPass) live on this struct as panel-internal fields
// (`json:"-"`) so the handler can hand the whole input straight to
// createSiteViaNewPipeline without the now-deleted site.Spec carrier.
type createSiteInput struct {
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
	// Panel-internal — populated by createSite from auto-generated DB
	// creds before the pipeline runs. Never deserialized from the API
	// request body (`json:"-"`).
	WPDBName string `json:"-"`
	WPDBUser string `json:"-"`
	WPDBPass string `json:"-"`
}

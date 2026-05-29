// Creator is the abstract base — shared steps every site type needs.
// Per-type subtypes (PhpCreator, NodejsCreator, …) embed *Creator and
// add their own type-specific steps, then expose Run() which calls every
// step in the right order.
//
// Step methods take no arguments — they read everything from c.Spec.
// That's the structural property the whole refactor is built on: a
// step cannot accidentally use a "stale" copy of the user/domain
// because there's only one Spec in the Creator, and every step reads
// from it directly.
package creator

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/auracp/auracp/internal/osuser"
	"github.com/auracp/auracp/internal/paths"
	"github.com/auracp/auracp/internal/system"
	"github.com/auracp/auracp/internal/webserver/template"
)

// Creator carries the input Spec + the shared system-level managers.
// Subtypes wrap it and add type-specific managers (php, runtime, etc.).
type Creator struct {
	Spec *Spec
	R    *system.Runner
	Os   *osuser.Manager
	Log  *slog.Logger // structured log per step — see logStep
}

// New builds the base Creator. Subtypes (PhpCreator, …) call this and
// then add their own per-type fields.
func New(spec *Spec, r *system.Runner) *Creator {
	return &Creator{
		Spec: spec,
		R:    r,
		Os:   osuser.New(r),
		Log:  slog.Default().With("site", spec.Domain, "type", spec.Type, "user", spec.User),
	}
}

// logStep is the structured-logging hook every Step calls. Net effect:
// `journalctl -u auracpd | grep <domain>` shows the ordered timeline of
// what auracpd did for a site, with timing. Post-mortem becomes trivial
// for the next time a class-of-bug strikes.
func (c *Creator) logStep(step string, started time.Time, err error) {
	if err != nil {
		c.Log.Error("step failed", "step", step, "took_ms", time.Since(started).Milliseconds(), "err", err.Error())
		return
	}
	c.Log.Info("step ok", "step", step, "took_ms", time.Since(started).Milliseconds())
}

// ─── Step 1: Linux user + skeleton dirs ───
//
// Reuses internal/osuser, which already handles useradd, the SFTP-jail
// group, the .ssh skeleton, and password setting. The skeleton dir
// inside /home/<user> (htdocs/, logs/, tmp/, backups/) is created by
// osuser via Linux's `useradd -k`.
func (c *Creator) CreateUser(ctx context.Context) error {
	t := time.Now()
	err := c.Os.Create(ctx, c.Spec.User, c.Spec.Domain)
	if err == nil && c.Spec.Password != "" {
		// SetPassword is intentionally best-effort post-create — a
		// failure here doesn't roll back the user (operator can set
		// the password manually in the SSH/FTP tab).
		_ = c.Os.SetPassword(ctx, c.Spec.User, c.Spec.Password)
	}
	c.logStep("CreateUser", t, err)
	return err
}

// ─── Step 2: docroot directory ───
//
// /home/<user>/htdocs/<domain> — owned by <user>:<user>, mode 0750. The
// dir already exists thanks to useradd's skel/, but we explicitly
// mkdir+chown so the path is guaranteed even if the skel layout drifts.
func (c *Creator) CreateRootDirectory(ctx context.Context) error {
	t := time.Now()
	root := paths.DocRoot(c.Spec.User, c.Spec.Domain)
	err := os.MkdirAll(root, 0o750)
	if err == nil {
		// chown to <user>:<user> via system runner so dry-run mode honours
		// it (direct syscall.Chown bypasses our dry-run gate).
		_, err = c.R.Run(ctx, "chown", c.Spec.User+":"+c.Spec.User, root)
	}
	c.logStep("CreateRootDirectory", t, err)
	return err
}

// ─── Step 3: logrotate ───
//
// One file per site user (not per site) so a single rotate batch handles
// every site they host. /etc/logrotate.d/<user>.
func (c *Creator) CreateLogrotateFile() error {
	t := time.Now()
	user := c.Spec.User
	body := strings.ReplaceAll(strings.ReplaceAll(logrotateTemplate, "{{user}}", user), "{{group}}", user)
	path := filepath.Join("/etc/logrotate.d", user)
	err := os.WriteFile(path, []byte(body), 0o644)
	c.logStep("CreateLogrotateFile", t, err)
	return err
}

// ─── Step 4: write SSL cert + key files (initial self-signed) ───
//
// We write a self-signed cert immediately so the vhost's `listen 443
// ssl;` directive has files to point at. The renewal goroutine in
// internal/acme upgrades to a real Let's Encrypt cert as soon as
// HTTP-01 (or Cloudflare DNS-01) completes. Between the two events
// (sub-second on a healthy host) browsers see a self-signed warning;
// that's the same window CloudPanel has and is acceptable.
//
// When this is called from re-render paths (Refactor #2's RunReapply),
// it doesn't overwrite an existing real cert — see the os.Stat guard.
func (c *Creator) CreateSslCertFiles(ctx context.Context) error {
	t := time.Now()
	if err := os.MkdirAll(paths.SSLDir, 0o755); err != nil {
		c.logStep("CreateSslCertFiles", t, err)
		return err
	}
	crt := paths.CertPath(c.Spec.Domain)
	if _, err := os.Stat(crt); err == nil {
		// Real cert already issued — leave it. The acme goroutine owns
		// these files now.
		c.logStep("CreateSslCertFiles", t, nil)
		return nil
	}
	// Generate a self-signed cert via openssl. Could be done with
	// crypto/x509 but shelling out keeps the audit-log entry visible.
	key := paths.KeyPath(c.Spec.Domain)
	args := []string{"req", "-x509", "-nodes", "-days", "30",
		"-newkey", "rsa:2048", "-keyout", key, "-out", crt,
		"-subj", "/CN=" + c.Spec.Domain}
	_, err := c.R.Run(ctx, "openssl", args...)
	c.logStep("CreateSslCertFiles", t, err)
	return err
}

// ─── Step 5: nginx vhost (atomic stage→test→swap) ───
//
// Uses the new template/ + processor/ packages. Note that the cert
// paths come from the filesystem state (was a cert issued already?) so
// re-runs of this method after lego completes pick up the real cert
// without us having to pass it down explicitly.
func (c *Creator) CreateNginxVhost(ctx context.Context) error {
	t := time.Now()
	body, err := template.Load(c.Spec.Type)
	if err != nil {
		c.logStep("CreateNginxVhost", t, err)
		return err
	}
	tmpl, err := template.For(c.Spec.Type)
	if err != nil {
		c.logStep("CreateNginxVhost", t, err)
		return err
	}
	// Probe for issued cert files; if present, the renderer will emit
	// the ssl_certificate directives. If not, the vhost is HTTP-only
	// (and the acme goroutine will re-render us once issuance lands).
	var crt, key string
	if _, err := os.Stat(paths.CertPath(c.Spec.Domain)); err == nil {
		crt = paths.CertPath(c.Spec.Domain)
		key = paths.KeyPath(c.Spec.Domain)
	}
	rendered := tmpl.Render(body, c.Spec.RenderContext(crt, key))

	// Atomic stage→test→swap. Same pattern as the existing webserver.go
	// uses — copy here so the new pipeline doesn't depend on the old.
	if err := os.MkdirAll(paths.NginxSitesAvailable, 0o755); err != nil {
		c.logStep("CreateNginxVhost", t, err)
		return err
	}
	if err := os.MkdirAll(paths.NginxSitesEnabled, 0o755); err != nil {
		c.logStep("CreateNginxVhost", t, err)
		return err
	}
	// v0.2.57: defensive — ensure /home/<user>/logs/ exists before
	// nginx -t. Pre-this, an operator-reported Node site create failed
	// with `nginx: [emerg] open() "/home/<user>/logs/access.log"
	// failed (2: No such file or directory)`. The vhost has
	// `access_log /home/<user>/logs/access.log;` and nginx -t tries to
	// open the parent dir. osuser.Create does create the dir at user-
	// creation time, but if anything (orphan user from a previous
	// failed create, manual filesystem tinkering) leaves it missing,
	// CreateNginxVhost now self-heals before validation runs.
	if err := os.MkdirAll(paths.LogDir(c.Spec.User), 0o750); err != nil {
		c.logStep("CreateNginxVhost", t, err)
		return err
	}
	// chown to the site user so PHP-FPM / Node systemd unit (running
	// as that user) can write to the dir. nginx itself opens these
	// files as the master process (root) for write, so this chown is
	// for the BACKEND log lines, not nginx access/error.
	_, _ = c.R.Run(ctx, "chown", c.Spec.User+":"+c.Spec.User, paths.LogDir(c.Spec.User))
	dst := paths.NginxSiteFile(c.Spec.Domain)
	link := paths.NginxSiteLink(c.Spec.Domain)
	tmp := dst + ".new"
	if err := os.WriteFile(tmp, []byte(rendered), 0o644); err != nil {
		c.logStep("CreateNginxVhost", t, err)
		return err
	}
	// Point the live symlink at tmp so `nginx -t` validates the new
	// content. If validation fails, rollback to the prior target.
	var prev string
	if li, _ := os.Lstat(link); li != nil && li.Mode()&os.ModeSymlink != 0 {
		if rl, e := os.Readlink(link); e == nil {
			prev = rl
		}
	}
	_ = os.Remove(link)
	if err := os.Symlink(tmp, link); err != nil {
		_ = os.Remove(tmp)
		c.logStep("CreateNginxVhost", t, err)
		return err
	}
	if _, err := c.R.Run(ctx, "nginx", "-t"); err != nil {
		// Rollback.
		_ = os.Remove(link)
		if prev != "" {
			_ = os.Symlink(prev, link)
		}
		_ = os.Remove(tmp)
		err = fmt.Errorf("nginx config invalid: %w", err)
		c.logStep("CreateNginxVhost", t, err)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		c.logStep("CreateNginxVhost", t, err)
		return err
	}
	_ = os.Remove(link)
	err = os.Symlink(dst, link)
	c.logStep("CreateNginxVhost", t, err)
	return err
}

// ─── Step 6: nginx reload ───
//
// One reload per pipeline run. Backend changes (FPM pool, systemd unit)
// don't need nginx to reload — they reload their own service. nginx
// reloads ONLY when the vhost on disk changed.
func (c *Creator) ReloadNginx(ctx context.Context) error {
	t := time.Now()
	if _, err := c.R.Run(ctx, "nginx", "-t"); err != nil {
		err = fmt.Errorf("nginx config invalid: %w", err)
		c.logStep("ReloadNginx", t, err)
		return err
	}
	_, err := c.R.Run(ctx, "systemctl", "reload", "nginx")
	if err != nil {
		// Fallback (defensive — stock nginx unit ships ExecReload).
		_, err = c.R.Run(ctx, "nginx", "-s", "reload")
	}
	c.logStep("ReloadNginx", t, err)
	return err
}

// ─── Step 7: ownership + permissions ───
//
// chown -R <user>:<user> /home/<user>     (recursive)
// chmod 750 /home/<user>                  (so nginx-as-www-data can cd in via group)
// chmod 700 /home/<user>/.ssh
// chmod 600 /home/<user>/.ssh/*
//
// Best-effort — failures here don't roll back the site, since a write to
// e.g. .ssh might fail if the operator hasn't added one yet. Logged and
// surfaced; nothing crashes.
func (c *Creator) ResetPermissions(ctx context.Context) error {
	t := time.Now()
	home := paths.SiteHome(c.Spec.User)
	_, err := c.R.Run(ctx, "chown", "-R", c.Spec.User+":"+c.Spec.User, home)
	if err == nil {
		_, _ = c.R.Run(ctx, "chmod", "750", home)
	}
	c.logStep("ResetPermissions", t, err)
	return err
}

// logrotateTemplate — one logrotate entry covering nginx + php-fpm logs
// for one site user. Daily, 14-day retention, compress old, kill empty,
// kill missing. Inline as a const to avoid a third resource file.
const logrotateTemplate = `/home/{{user}}/logs/*.log /home/{{user}}/logs/*/*.log {
    daily
    rotate 14
    compress
    delaycompress
    missingok
    notifempty
    create 0640 {{user}} {{group}}
    sharedscripts
    postrotate
        systemctl reload nginx >/dev/null 2>&1 || true
    endscript
}
`

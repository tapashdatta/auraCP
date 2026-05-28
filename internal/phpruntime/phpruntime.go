// Package phpruntime manages the per-OS PHP-FPM versions auraCP-hosted sites
// run against. Multiple versions live side-by-side (8.3 / 8.4 / 8.5 from
// deb.sury.org); each PHP-FPM service ships its own daemon. Per-site pool
// configs land under /etc/php/<ver>/fpm/pool.d/<domain>.conf.
package phpruntime

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/auracp/auracp/internal/paths"
	"github.com/auracp/auracp/internal/store"
	"github.com/auracp/auracp/internal/system"
	"github.com/auracp/auracp/internal/validate"
)

// Whitelist matches validate.PHPVersion — keep them in sync.
var supportedVersions = []string{"8.3", "8.4", "8.5"}

// Default pool defaults. Per-site values come from store.PHPSettings(domain)
// (UI form) and override these. Tuned for "minimum host RAM" — `ondemand`
// keeps idle sites at zero workers.
const (
	DefaultMemoryLimit       = "256M"
	DefaultUploadMaxFilesize = "64M"
	DefaultPostMaxSize       = "64M"
	DefaultMaxExecutionTime  = "120"
	DefaultMaxInputVars      = "5000"
	DefaultMaxChildren       = "10"
)

type Manager struct {
	R     *system.Runner
	Store *store.Store
}

func New(r *system.Runner, st *store.Store) *Manager { return &Manager{R: r, Store: st} }

// Available returns the version strings auraCP supports (whitelisted; matches
// validate.PHPVersion). The installer probes deb.sury.org for actual
// availability; this list is the universe of choices the panel ever offers.
func (m *Manager) Available() []string {
	out := make([]string, len(supportedVersions))
	copy(out, supportedVersions)
	return out
}

// Installed returns the versions whose php<ver>-fpm pool.d directory exists
// on disk (i.e. the package is present). Cheap; no apt calls.
func (m *Manager) Installed() []string {
	var out []string
	for _, v := range supportedVersions {
		if _, err := os.Stat(filepath.Join("/etc/php", v, "fpm", "pool.d")); err == nil {
			out = append(out, v)
		}
	}
	return out
}

// Reconcile aligns the php_runtimes table with what's actually installed on
// disk. Called on auracpd startup so the UI never lies about availability.
func (m *Manager) Reconcile() {
	if m.R.DryRun {
		return
	}
	for _, v := range m.Installed() {
		if _, ok := m.Store.PHPRuntime(v); !ok {
			_ = m.Store.AddPHPRuntime(v)
		}
	}
	if _, ok := m.Store.DefaultPHPRuntime(); !ok {
		if inst := m.Installed(); len(inst) > 0 {
			_ = m.Store.SetDefaultPHPRuntime(inst[len(inst)-1]) // newest installed
		}
	}
}

// Install adds the deb.sury.org packages for the chosen version. Idempotent.
//
// Extension list is conservative: every PHP site needs the core extensions
// (mbstring/xml/curl/gd/zip/bcmath/intl) and the embedded opcache (no
// separate php<ver>-opcache package on deb.sury.org since PHP 7.0). The DB /
// cache client libraries are added ONLY when the corresponding service is
// installed on the host — no point pulling php<ver>-mysql onto a Postgres-
// only box, php<ver>-redis where Redis was never selected, etc.
func (m *Manager) Install(ctx context.Context, version string, makeDefault bool) error {
	if err := validate.PHPVersion(version); err != nil {
		return err
	}
	pkgs := []string{
		"php" + version + "-fpm",
		"php" + version + "-cli",
		"php" + version + "-mbstring",
		"php" + version + "-xml",
		"php" + version + "-curl",
		"php" + version + "-gd",
		"php" + version + "-zip",
		"php" + version + "-bcmath",
		"php" + version + "-intl",
	}
	// Detect by binary because that's what the installer (or future apt
	// install of these engines) reliably drops; status of the systemd unit
	// is a less stable signal than the binary's presence on PATH.
	if _, err := exec.LookPath("mariadbd"); err == nil {
		pkgs = append(pkgs, "php"+version+"-mysql")
	} else if _, err := exec.LookPath("mysqld"); err == nil {
		pkgs = append(pkgs, "php"+version+"-mysql")
	}
	if _, err := exec.LookPath("psql"); err == nil {
		pkgs = append(pkgs, "php"+version+"-pgsql")
	}
	if _, err := exec.LookPath("redis-server"); err == nil {
		pkgs = append(pkgs, "php"+version+"-redis")
	}
	args := append([]string{"install", "-y", "--no-install-recommends"}, pkgs...)
	if _, err := m.R.Run(ctx, "apt-get", args...); err != nil {
		return fmt.Errorf("apt install php %s: %w", version, err)
	}
	// Disable the auto-added default `www` pool so the panel owns all pools.
	wwwConf := filepath.Join("/etc/php", version, "fpm/pool.d/www.conf")
	if _, err := os.Stat(wwwConf); err == nil {
		_ = os.Rename(wwwConf, wwwConf+".disabled")
	}
	if _, err := m.R.Run(ctx, "systemctl", "enable", "--now", "php"+version+"-fpm"); err != nil {
		return err
	}
	if err := m.Store.AddPHPRuntime(version); err != nil {
		return err
	}
	if makeDefault {
		return m.Store.SetDefaultPHPRuntime(version)
	}
	if _, ok := m.Store.DefaultPHPRuntime(); !ok {
		return m.Store.SetDefaultPHPRuntime(version)
	}
	return nil
}

// Remove uninstalls a PHP version's apt packages. Refuses if any site uses it.
func (m *Manager) Remove(ctx context.Context, version string) error {
	if m.Store.SiteUsesPHPVersion(version) {
		return fmt.Errorf("PHP %s is in use by one or more sites", version)
	}
	pkg := "php" + version + "-fpm"
	_, _ = m.R.Run(ctx, "systemctl", "disable", "--now", pkg)
	if _, err := m.R.Run(ctx, "apt-get", "purge", "-y", "php"+version+"-*"); err != nil {
		return fmt.Errorf("apt purge php %s: %w", version, err)
	}
	return m.Store.DeletePHPRuntime(version)
}

// SetDefault marks `version` as the default in the store. There's no symlink
// here (unlike Node) — pool configs name the version explicitly, so a "default"
// is purely a UI hint for which version new sites should default to.
func (m *Manager) SetDefault(version string) error {
	if _, ok := m.Store.PHPRuntime(version); !ok {
		return fmt.Errorf("PHP %s is not installed", version)
	}
	return m.Store.SetDefaultPHPRuntime(version)
}

// poolData binds into the pool.d/<domain>.conf template.
type poolData struct {
	Domain, User, Socket                    string
	MemoryLimit, UploadMax, PostMax         string
	MaxExec, MaxInputVars, MaxChildren      string
}

const poolTemplate = `; auraCP-managed pool — do not edit by hand. Rewritten on every panel change.
[{{.Domain}}]
user = {{.User}}
group = {{.User}}

listen = {{.Socket}}
listen.owner = {{.User}}
listen.group = www-data
listen.mode = 0660

pm = ondemand
pm.max_children = {{.MaxChildren}}
pm.process_idle_timeout = 30s
pm.max_requests = 500

catch_workers_output = yes
decorate_workers_output = no

php_admin_value[memory_limit] = {{.MemoryLimit}}
php_admin_value[upload_max_filesize] = {{.UploadMax}}
php_admin_value[post_max_size] = {{.PostMax}}
php_admin_value[max_execution_time] = {{.MaxExec}}
php_admin_value[max_input_vars] = {{.MaxInputVars}}
php_admin_value[opcache.enable] = 1
php_admin_value[expose_php] = Off
`

// WritePool generates the per-site pool config and reloads the right
// php<ver>-fpm service. Validates version + domain + user before touching disk.
//
// Refuses if the requested PHP version isn't actually installed on the host
// (e.g. the operator installed PHP 8.5 in the data-plane installer but the
// site form posted 8.4) — without this guard we'd write a pool file to
// /etc/php/8.4/fpm/pool.d/ that no service would ever read, then fail at
// `systemctl reload php8.4-fpm` with the cryptic "Unit not found".
func (m *Manager) WritePool(ctx context.Context, version, domain, user string) error {
	if err := validate.PHPVersion(version); err != nil {
		return err
	}
	if err := validate.Domain(domain); err != nil {
		return err
	}
	if err := validate.Username(user); err != nil {
		return err
	}
	installed := m.Installed()
	found := false
	for _, v := range installed {
		if v == version {
			found = true
			break
		}
	}
	if !found {
		if len(installed) == 0 {
			return fmt.Errorf("no PHP-FPM versions installed on this host — install one from Settings → PHP Versions, then retry")
		}
		return fmt.Errorf("PHP %s is not installed (available: %s) — install it from Settings → PHP Versions, or pick an installed version",
			version, strings.Join(installed, ", "))
	}
	// Per-site overrides from the store, fall back to package defaults.
	get := func(k, def string) string {
		if v, ok := m.Store.PHPSetting(domain, k); ok && strings.TrimSpace(v) != "" {
			return v
		}
		return def
	}
	d := poolData{
		Domain:       domain,
		User:         user,
		Socket:       paths.PHPSocket(domain),
		MemoryLimit:  get("memory_limit", DefaultMemoryLimit),
		UploadMax:    get("upload_max_filesize", DefaultUploadMaxFilesize),
		PostMax:      get("post_max_size", DefaultPostMaxSize),
		MaxExec:      get("max_execution_time", DefaultMaxExecutionTime),
		MaxInputVars: get("max_input_vars", DefaultMaxInputVars),
		MaxChildren:  get("pm.max_children", DefaultMaxChildren),
	}
	t, err := template.New("pool").Parse(poolTemplate)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, d); err != nil {
		return err
	}
	if !m.R.DryRun {
		if err := os.MkdirAll(filepath.Dir(paths.PHPPoolFile(version, domain)), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(paths.PHPPoolFile(version, domain), buf.Bytes(), 0o644); err != nil {
			return err
		}
		if err := os.MkdirAll(paths.PHPSocketsDir, 0o755); err != nil {
			return err
		}
	}
	_, err = m.R.Run(ctx, "systemctl", "reload", "php"+version+"-fpm")
	return err
}

// RemovePool deletes the per-site pool config and reloads the corresponding
// php<ver>-fpm. Best-effort across all installed versions in case the site's
// pinned version changed since creation.
func (m *Manager) RemovePool(ctx context.Context, domain string) error {
	for _, v := range m.Installed() {
		f := paths.PHPPoolFile(v, domain)
		if _, err := os.Stat(f); err == nil {
			if !m.R.DryRun {
				_ = os.Remove(f)
			}
			_, _ = m.R.Run(ctx, "systemctl", "reload", "php"+v+"-fpm")
		}
	}
	return nil
}

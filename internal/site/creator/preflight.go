// Preflight runs every validation BEFORE any filesystem mutation. If it
// returns an error, the caller can show it to the operator and try again
// with no cleanup work — zero artifacts were written. This is the
// single biggest win against the class of bug we've been hitting:
// "the validation failed three steps in, but the user + dir + pool
// already exist on disk."
//
// What gets checked:
//   - Site type is recognized
//   - Domain is a valid DNS-style label
//   - Site user is a valid Linux username
//   - PHP version (for PHP/WP) is installed
//   - Domain conflicts: no existing vhost OR pool file (any PHP version)
//     for this domain — catches the "deleted but not cleaned up"
//     leftover state
//   - User conflicts: no existing /home/<user> — catches name collision
//     across recreated sites
//   - Reverse proxy URL parseable, non-loopback (for safety against
//     accidentally proxying to localhost services)
//
// What's NOT checked here (because they require real I/O the Creator
// has to do anyway):
//   - DNS resolves to this server (operators have legitimate reasons
//     to set up nginx before pointing DNS)
//   - Cloudflare API token works (lego's job, not ours)
package creator

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/auracp/auracp/internal/paths"
	"github.com/auracp/auracp/internal/validate"
)

// Preflight returns nil if the Spec is safe to hand to the Creator.
// Any error is operator-readable and surfaces directly to the UI.
func Preflight(spec *Spec, deps *Deps) error {
	// ─── universal validation ───
	if err := validate.SiteType(spec.Type); err != nil {
		return fmt.Errorf("type: %w", err)
	}
	if err := validate.Domain(spec.Domain); err != nil {
		return fmt.Errorf("domain: %w", err)
	}
	if err := validate.Username(spec.User); err != nil {
		return fmt.Errorf("user: %w", err)
	}

	// ─── domain conflict: existing nginx vhost? ───
	if _, err := os.Stat(paths.NginxSiteFile(spec.Domain)); err == nil {
		return fmt.Errorf("domain conflict: nginx vhost for %s already exists at %s (delete the existing site first, or rerun with conflict=force)", spec.Domain, paths.NginxSiteFile(spec.Domain))
	}

	// ─── domain conflict: existing FPM pool file (any PHP version)? ───
	// This is the check that would have caught the a-4zwq/a-ukfs bug at
	// create time. Walks every installed PHP version's pool.d/ and
	// errors if it finds <domain>.conf — that's stale state from a
	// previous create that didn't clean up.
	if deps != nil && deps.Php != nil {
		for _, ver := range deps.Php.Installed() {
			pool := paths.PHPPoolFile(ver, spec.Domain)
			if _, err := os.Stat(pool); err == nil {
				return fmt.Errorf("domain conflict: stale FPM pool exists at %s (orphan from a previous create that didn't clean up — remove the file manually or delete-then-recreate the site)", pool)
			}
		}
	}

	// ─── user conflict: /home/<user> already populated? ───
	// Skip if the directory exists empty (could be skel from useradd).
	// We refuse only if it has content — a half-created previous site.
	home := paths.SiteHome(spec.User)
	if entries, err := os.ReadDir(home); err == nil && len(entries) > 0 {
		// Tolerate the standard skel subdirs (htdocs/logs/tmp/.ssh/backups)
		// being present but EMPTY. Refuse if any of them have content.
		for _, e := range entries {
			if !e.IsDir() {
				return fmt.Errorf("user conflict: /home/%s already has files (was the user previously used for another site?)", spec.User)
			}
			sub := filepath.Join(home, e.Name())
			if subEntries, _ := os.ReadDir(sub); len(subEntries) > 0 && e.Name() != ".ssh" {
				return fmt.Errorf("user conflict: /home/%s/%s is not empty (was the user previously used for another site?)", spec.User, e.Name())
			}
		}
	}

	// ─── per-type checks ───
	switch spec.Type {
	case "php", "wordpress":
		if err := validate.PHPVersion(spec.PHPVersion); err != nil {
			return fmt.Errorf("php_version: %w", err)
		}
		if deps != nil && deps.Php != nil {
			installed := deps.Php.Installed()
			found := false
			for _, v := range installed {
				if v == spec.PHPVersion {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("php_version %q is not installed on this host (installed: %v) — install via Settings → PHP Versions", spec.PHPVersion, installed)
			}
		}
	case "reverseproxy":
		if spec.Upstream == "" {
			return fmt.Errorf("upstream: required for reverse proxy")
		}
		u, err := url.Parse(spec.Upstream)
		if err != nil {
			return fmt.Errorf("upstream: not a valid URL: %w", err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("upstream: scheme must be http or https, got %q", u.Scheme)
		}
		// Block accidental loopback proxies — a reverseproxy to
		// 127.0.0.1:<panel-port> would bypass auth.
		//
		// v0.2.58: refined message. The "use a node/python site type"
		// suggestion confused operators who legitimately want to front
		// a separate local backend (Grafana, a Docker
		// container on 127.0.0.1:3000, etc.). The block is still right
		// for the auth-bypass class — the OS firewall + the panel's
		// own bind-to-loopback design make a loopback reverse-proxy
		// nearly always a misconfiguration — but the message now
		// names the actual fix: bind the backend to the host's LAN/
		// public address and proxy there, or run it as a managed
		// node/python site so auracp owns its port allocator.
		host := u.Hostname()
		if host == "127.0.0.1" || host == "localhost" || strings.HasPrefix(host, "127.") {
			return fmt.Errorf("upstream: loopback addresses (%s) are blocked. A reverse proxy to 127.0.0.1:* would bypass the panel's auth boundary. Options: (a) bind your backend to a non-loopback interface and proxy there; (b) create a node/python site so auracp manages the port allocator; (c) for a private backend on this host, edit the vhost directly under Vhost → freeform once the site exists", host)
		}
	case "nodejs":
		if spec.StartFile == "" {
			return fmt.Errorf("start_file: required for Node.js sites")
		}
	case "python":
		if spec.Module == "" {
			return fmt.Errorf("module: required for Python sites (e.g. \"myapp.wsgi:application\")")
		}
	}

	return nil
}

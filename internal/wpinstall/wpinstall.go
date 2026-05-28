// Package wpinstall wraps wp-cli to provision a fresh WordPress install into
// a site's docroot. Three steps, all run as the site user via the system
// runner's privilege drop:
//
//   1. wp core download   — fetches the latest stable WordPress tarball
//   2. wp config create   — writes wp-config.php pointing at the DB
//   3. wp core install    — runs the famous 5-minute setup non-interactively
//
// Errors are returned with enough context that the API handler can surface
// which step failed; partial state (a downloaded core with no wp-config) is
// left alone so the operator can finish manually if wanted, or delete the
// site to clean up.
package wpinstall

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/auracp/auracp/internal/paths"
	"github.com/auracp/auracp/internal/system"
)

// Spec is everything wpinstall needs to call wp-cli end to end. All fields
// are required; the API layer validates + defaults before constructing this.
type Spec struct {
	Domain     string
	SiteUser   string

	DBHost string // typically 'localhost' for MariaDB on the same box
	DBName string
	DBUser string
	DBPass string

	URL        string // e.g. https://example.com
	Title      string
	AdminUser  string
	AdminPass  string
	AdminEmail string

	Locale string // optional; defaults to en_US
}

// Available reports whether the wp-cli binary is on PATH. The API checks
// this before exposing the auto-install option so we don't promise something
// the host can't deliver.
func Available() bool {
	_, err := exec.LookPath("wp")
	return err == nil
}

// Install performs all three steps of a one-shot WordPress install.
// Idempotent only for `wp core download` — `wp config create` refuses to
// overwrite an existing wp-config, and `wp core install` is a no-op if the
// site is already installed (and exits non-zero, which we treat as success
// after a sniff). Caller is expected to delete a half-installed site
// rather than re-run Install.
func Install(ctx context.Context, r *system.Runner, s Spec) error {
	if !Available() {
		return fmt.Errorf("wp-cli not installed (expected at /usr/local/bin/wp). Re-run sudo auracp-install with --php=yes")
	}
	if s.SiteUser == "" || s.Domain == "" {
		return fmt.Errorf("wpinstall: site user + domain are required")
	}
	docroot := paths.DocRoot(s.SiteUser, s.Domain)
	locale := s.Locale
	if locale == "" {
		locale = "en_US"
	}

	// 1. download core. --skip-content drops the bundled twentytwentyfour
	// themes + akismet/hello plugins; operator can add them via the UI later
	// and a fresh site loads a couple seconds faster.
	if _, err := r.RunAs(ctx, s.SiteUser, "wp",
		"core", "download",
		"--path="+docroot,
		"--locale="+locale,
		"--skip-content",
	); err != nil {
		return fmt.Errorf("wp core download failed: %w", err)
	}

	// 2. write wp-config.php pointing at the DB. wp-cli generates the salts
	// itself by calling api.wordpress.org/secret-key/1.1/salt/; falls back
	// to a local rand if the network's down.
	if _, err := r.RunAs(ctx, s.SiteUser, "wp",
		"config", "create",
		"--dbname="+s.DBName,
		"--dbuser="+s.DBUser,
		"--dbpass="+s.DBPass,
		"--dbhost="+s.DBHost,
		"--dbprefix=wp_",
		"--path="+docroot,
		"--force",
	); err != nil {
		return fmt.Errorf("wp config create failed: %w", err)
	}

	// 3. run the install. --skip-email avoids the "your site is ready" mail
	// that wp-cli tries to send via PHP's mail() — most boxes don't have a
	// MTA configured and the install would error out otherwise.
	out, err := r.RunAs(ctx, s.SiteUser, "wp",
		"core", "install",
		"--url="+s.URL,
		"--title="+s.Title,
		"--admin_user="+s.AdminUser,
		"--admin_password="+s.AdminPass,
		"--admin_email="+s.AdminEmail,
		"--path="+docroot,
		"--skip-email",
	)
	if err != nil {
		// "Site already installed" is a soft-success — happens if the
		// operator clicks Install twice; treat it as ok rather than block.
		if strings.Contains(out, "already installed") || strings.Contains(err.Error(), "already installed") {
			return nil
		}
		return fmt.Errorf("wp core install failed: %w", err)
	}
	return nil
}

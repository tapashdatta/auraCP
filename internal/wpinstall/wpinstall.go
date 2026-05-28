// Package wpinstall provisions a fresh WordPress install into a site's
// docroot. Three logical steps:
//
//   1. core download — fetch + extract the latest stable tarball
//   2. config create — write wp-config.php with DB creds + salts
//   3. core install  — run the famous 5-minute setup non-interactively
//
// v0.2.50: steps 1 and 2 are native Go (see wpcore.go + wpconfig.go).
// wp-cli's 2.10+ config-command package has a phar template loader
// regression that breaks `wp config create` on some PHP builds; the
// failure cascade leaves a partial install with no recovery path
// short of hand-writing wp-config.php. Doing the work in Go eliminates
// the dependency on a brittle upstream tool for the critical-path
// file write.
//
// Step 3 still calls wp-cli because reimplementing WordPress's setup
// wizard (user creation, table seeding, options, etc.) in Go is out
// of scope. `wp core install` is a different wp-cli code path that
// hasn't shown the phar regression.
//
// Errors are returned with enough context that the API handler can
// surface which step failed; partial state (a downloaded core with no
// wp-config) is left alone so the operator can finish manually if
// wanted, or delete the site to clean up.
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
//
// v0.2.50 flow:
//   1. downloadAndExtract — native Go HTTPS GET of latest.tar.gz +
//      streamed tar/gz extraction into docroot, chown to site user.
//      Replaces `wp core download`.
//   2. writeWPConfig — fetches salts from api.wordpress.org, renders
//      wp-config.php from a Go template, atomic-writes into docroot,
//      chowns to site user. Replaces `wp config create` (the step
//      that was broken by wp-cli's phar template loader regression).
//   3. wp core install — still wp-cli. Different code path; not
//      affected by the phar bug. Requires wp-cli on PATH.
//
// Idempotent: step 1 is a no-op if wp-includes/version.php already
// exists; step 2 is a no-op if wp-config.php already exists; step 3
// reports "already installed" if the WordPress install row exists.
// Half-installed sites can be resumed by re-running Install (modulo
// the DB row, which is owned by the API handler before us).
func Install(ctx context.Context, r *system.Runner, s Spec) error {
	if s.SiteUser == "" || s.Domain == "" {
		return fmt.Errorf("wpinstall: site user + domain are required")
	}
	docroot := paths.DocRoot(s.SiteUser, s.Domain)
	locale := s.Locale
	if locale == "" {
		locale = "en_US"
	}

	// Step 1: native Go core download + tarball extract.
	if _, err := downloadAndExtract(ctx, r, docroot, s.SiteUser, locale); err != nil {
		return fmt.Errorf("core download failed: %w", err)
	}

	// Step 2: native Go wp-config.php writer. The phar bug-free path.
	if err := writeWPConfig(ctx, r, docroot, s.SiteUser, s); err != nil {
		return fmt.Errorf("write wp-config: %w", err)
	}

	// Step 3: wp-cli — only the install wizard. Still needs wp-cli on
	// the box; surface a clear error if not.
	if !Available() {
		return fmt.Errorf("wp-cli not installed (expected at /usr/local/bin/wp). Re-run sudo auracp-install with --php=yes — note that v0.2.50 only needs wp-cli for `wp core install`; core download + wp-config writing run natively in auracpd")
	}
	// --skip-email avoids the "your site is ready" mail that wp-cli
	// tries to send via PHP's mail() — most boxes don't have a MTA
	// configured and the install would error out otherwise.
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

// v0.2.50: replace `wp core download` shell-out with a native Go
// downloader + tarball extractor.
//
// Why: wp-cli's config-command package has a phar-template loader bug
// in some 2.10+ builds that produces the malformed path:
//   phar://wp-cli.phar/vendor/...templates/phar://usr/local/bin/wp/...
// breaking `wp config create`. Reducing our wp-cli dependence narrows
// the surface where these upstream regressions can break a site
// create.
//
// What this file owns:
//   - Download https://wordpress.org/latest.tar.gz (or the localized
//     variant) over plain HTTP to a temp file.
//   - Stream-extract the tarball into docroot, stripping the leading
//     `wordpress/` directory (matches `tar --strip-components=1`).
//   - chown -R the extracted tree to the site user.
//
// What still uses wp-cli (because reimplementing it is out of scope):
//   - The final `wp core install` step that runs WordPress's setup
//     wizard non-interactively. That code path doesn't hit the phar
//     template bug.
//
// Motto check:
//   - New Go deps: 0 (archive/tar, compress/gzip, net/http — stdlib)
//   - Binary delta on auracpd: ~30 KB for the tar/gzip codepaths
//   - Daemons / sockets added: 0
//   - Per-create overhead: one HTTPS download of ~25 MB (same as
//     before, just streamed through Go instead of wp-cli's phar)
package wpinstall

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/auracp/auracp/internal/system"
)

// coreDownloadTimeout caps the HTTPS GET. The latest WordPress tarball
// is ~25 MB; even a slow 1 Mbps link finishes in ~4 min. Set to 5 min
// for headroom; partial downloads abort cleanly via context.
const coreDownloadTimeout = 5 * time.Minute

// downloadAndExtract fetches the WordPress tarball for the given locale
// (empty = en_US/latest) and extracts it into docroot, stripping the
// leading `wordpress/` directory. Returns the number of files extracted
// (useful for caller's log; aiding debugging if the install completes
// but the tarball was truncated).
//
// Idempotent against a partial previous extract: existing files get
// overwritten, missing dirs created. If the user's docroot already has
// `wp-includes/version.php` we skip the whole thing — they already
// have WordPress.
func downloadAndExtract(ctx context.Context, r *system.Runner, docroot, siteUser, locale string) (int, error) {
	// Skip if WordPress is already extracted (e.g. retry after a
	// partial install).
	if _, err := os.Stat(filepath.Join(docroot, "wp-includes", "version.php")); err == nil {
		return 0, nil
	}

	url := tarballURL(locale)
	dlCtx, cancel := context.WithTimeout(ctx, coreDownloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(dlCtx, "GET", url, nil)
	if err != nil {
		return 0, fmt.Errorf("build core-download request: %w", err)
	}
	req.Header.Set("User-Agent", "auracp/0.2.50 (wpinstall)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("download WordPress core (%s): %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("download WordPress core (%s): HTTP %d", url, resp.StatusCode)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("gzip-decode WordPress tarball: %w", err)
	}
	defer gz.Close()

	if err := os.MkdirAll(docroot, 0o755); err != nil {
		return 0, fmt.Errorf("mkdir docroot %q: %w", docroot, err)
	}

	count, err := extractTar(tar.NewReader(gz), docroot)
	if err != nil {
		return count, err
	}

	// v0.2.60: belt-and-suspenders. The tar walk reports `count` files
	// written, but the operator-facing failure mode "site shows 404
	// after a successful create" can also come from a partial extract
	// (gzip ended early, file system out of inodes mid-stream, etc.).
	// Hard-assert the two files we absolutely need before declaring
	// success: index.php (front controller) and wp-includes/version.php
	// (loaded on every WP request). If either is missing, surface a
	// clear error so the API layer rolls the create back rather than
	// leaving the site half-done and serving 404s.
	for _, must := range []string{"index.php", "wp-includes/version.php", "wp-load.php"} {
		p := filepath.Join(docroot, must)
		if _, err := os.Stat(p); err != nil {
			return count, fmt.Errorf("post-extract verification: %s missing at %s (only %d files extracted — tarball truncated or extraction interrupted)", must, p, count)
		}
	}

	// chown -R via the runner so the audit log captures it.
	if _, err := r.Run(ctx, "chown", "-R", siteUser+":"+siteUser, docroot); err != nil {
		return count, fmt.Errorf("chown docroot: %w", err)
	}
	return count, nil
}

// tarballURL returns the WordPress download URL for a locale. en_US
// uses wordpress.org/latest.tar.gz; other locales use their own
// hostname (de_DE.wordpress.org/latest-de_DE.tar.gz pattern). Falls
// back to the global latest if locale is empty or "en_US".
func tarballURL(locale string) string {
	locale = strings.TrimSpace(locale)
	if locale == "" || locale == "en_US" {
		return "https://wordpress.org/latest.tar.gz"
	}
	// Locale code → BCP47-ish lowercased host: de_DE → de_de
	host := strings.ToLower(locale)
	return fmt.Sprintf("https://%s.wordpress.org/latest-%s.tar.gz", host, locale)
}

// extractTar walks the tar stream and writes entries under dst,
// stripping the leading path component (`wordpress/`). Returns the
// number of files written.
//
// Security:
//   - Rejects entries whose name escapes dst via `..` or absolute
//     paths after the strip — defends against the classic zip-slip /
//     tar-slip attack class even though our source is wordpress.org.
//   - Rejects symlinks pointing outside dst.
func extractTar(tr *tar.Reader, dst string) (int, error) {
	dstAbs, err := filepath.Abs(dst)
	if err != nil {
		return 0, err
	}
	count := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return count, fmt.Errorf("tar.Next: %w", err)
		}

		// Reject hostile names BEFORE filepath.Clean has a chance to
		// swallow them. `wordpress/../escape.txt` cleans to `escape.txt`
		// which would silently land in dst — but we want a hard refusal
		// so unusual tarballs surface loudly in the log.
		if strings.Contains(hdr.Name, "..") {
			return count, fmt.Errorf("tar entry %q escapes destination (contains \"..\")", hdr.Name)
		}
		if strings.HasPrefix(hdr.Name, "/") {
			return count, fmt.Errorf("tar entry %q escapes destination (absolute path)", hdr.Name)
		}

		// Strip the leading `wordpress/` (or whatever single component
		// the archive uses).
		parts := strings.SplitN(filepath.Clean(hdr.Name), string(os.PathSeparator), 2)
		if len(parts) < 2 || parts[1] == "" {
			// Top-level dir entry itself (e.g. "wordpress/") — skip.
			continue
		}
		rel := parts[1]

		// zip-slip second-line defense: resolve the target and verify
		// it's inside dst. The .. check above should make this
		// unreachable, but the cheap belt-and-suspenders confirms.
		target := filepath.Join(dstAbs, rel)
		if !strings.HasPrefix(target+string(os.PathSeparator), dstAbs+string(os.PathSeparator)) && target != dstAbs {
			return count, fmt.Errorf("tar entry %q escapes destination", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return count, fmt.Errorf("mkdir %q: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return count, fmt.Errorf("mkdir parent of %q: %w", target, err)
			}
			f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
			if err != nil {
				return count, fmt.Errorf("open %q for write: %w", target, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				_ = f.Close()
				return count, fmt.Errorf("write %q: %w", target, err)
			}
			if err := f.Close(); err != nil {
				return count, fmt.Errorf("close %q: %w", target, err)
			}
			count++
		case tar.TypeSymlink:
			// WordPress's tarball doesn't contain symlinks, but defend
			// anyway — reject any symlink pointing outside dst.
			linkTarget := filepath.Join(filepath.Dir(target), hdr.Linkname)
			absLink, err := filepath.Abs(linkTarget)
			if err != nil || !strings.HasPrefix(absLink+string(os.PathSeparator), dstAbs+string(os.PathSeparator)) {
				return count, fmt.Errorf("tar symlink %q escapes destination", hdr.Name)
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return count, fmt.Errorf("symlink %q: %w", target, err)
			}
		default:
			// Skip block/char devices, fifos, hardlinks — WordPress
			// tarballs don't contain them.
		}
	}
	return count, nil
}

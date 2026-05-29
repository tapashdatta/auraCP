// vhost_heal.go — self-heal orphan log-dir references that poison nginx -t.
//
// Failure shape this fixes (operator-reported in v0.2.57, even with the
// defensive os.MkdirAll in CreateNginxVhost):
//
//   nginx: [emerg] open() "/home/c-wo1m/logs/access.log" failed
//                  (2: No such file or directory)
//
// Across EVERY site-type create. The new vhost wasn't the problem —
// `nginx -t` validates every file under sites-enabled/, so a single
// vhost left behind by a prior failed create (whose log dir got rm'd
// when the site was torn down without removing the vhost) poisons all
// subsequent nginx -t invocations. Result: the operator can't create
// any new site of any type until they manually find and remove the
// stale vhost.
//
// Two healing strategies, both shipped:
//
//   1. EnsureLogDirsForEnabledVhosts — walks /etc/nginx/sites-enabled/,
//      extracts every `/home/<user>/logs` directory referenced by an
//      access_log / error_log directive, and mkdir+chown each missing
//      one. Empty dirs are safe — nginx workers just create the log
//      files inside them on first hit. No site state is reconstructed
//      that the operator didn't intend; we only restore the directory
//      shape required for nginx -t to pass.
//
//   2. PruneDeadVhosts — for each vhost file whose referenced site user
//      no longer exists in /etc/passwd, removes the vhost from
//      sites-enabled (and sites-available). True orphans — there's no
//      one for the site to belong to. This catches the case where the
//      site's user was deleted (manually or via the v0.2.57 uninstall
//      orphan sweep) but the vhost was left behind.
//
// Called from CreateNginxVhost BEFORE the new vhost's nginx -t.
package creator

import (
	"bufio"
	"context"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/auracp/auracp/internal/paths"
	"github.com/auracp/auracp/internal/system"
)

// access_log and error_log directives in our template always have the
// shape `access_log /home/<user>/logs/access.log;` (no commas, no
// buffers, no formats). One regex catches both.
var logPathRE = regexp.MustCompile(`^\s*(?:access_log|error_log)\s+(\S+)\s*;`)

// EnsureLogDirsForEnabledVhosts walks sites-enabled and creates any
// missing log directory referenced by an existing vhost. Best-effort:
// missing files / unreadable dirs are ignored — the goal is to clear
// the obvious "nginx -t fails because dir is missing" class, not to
// audit every byte.
//
// Chown to the directory's expected owner — for our template, the
// owner is the second-to-last path segment (i.e. /home/<user>/logs →
// <user>). If that user doesn't exist, we skip chown but still create
// the dir as root; nginx workers run as root anyway for log writes,
// so the dir works either way.
func EnsureLogDirsForEnabledVhosts(ctx context.Context, R *system.Runner) {
	seen := map[string]bool{}
	matches, _ := filepath.Glob(filepath.Join(paths.NginxSitesEnabled, "*.conf"))
	for _, p := range matches {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		// Allow log dirs nested fairly deep; bump max line size for
		// long fastcgi_param lines that scan finds in the file too.
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			m := logPathRE.FindStringSubmatch(sc.Text())
			if m == nil {
				continue
			}
			logPath := m[1]
			// Only self-heal paths under /home/<x>/logs — never touch
			// /var/log or other system locations a freeform Vhost-tab
			// edit might point at.
			if !strings.HasPrefix(logPath, paths.HomeBase+"/") || !strings.Contains(logPath, "/logs/") {
				continue
			}
			dir := filepath.Dir(logPath)
			if seen[dir] {
				continue
			}
			seen[dir] = true
			if _, err := os.Stat(dir); err == nil {
				continue
			}
			_ = os.MkdirAll(dir, 0o750)
			// Best-effort chown to the owning user.
			owner := filepath.Base(filepath.Dir(dir))
			if _, err := user.Lookup(owner); err == nil {
				_, _ = R.Run(ctx, "chown", owner+":"+owner, dir)
			}
		}
		_ = f.Close()
	}
}

// PruneDeadVhosts removes any vhost in sites-available / sites-enabled
// whose referenced site user no longer exists in /etc/passwd. We probe
// the vhost body for an access_log line under /home/<user>/logs and
// pass the extracted user to user.Lookup; absent = orphan, prune.
//
// Idempotent and safe: a vhost belonging to a live site never has its
// user missing from /etc/passwd, so this only ever removes true
// dangling files. The panel's own vhost (00-panel.conf) lives outside
// /home and is never touched by this scan.
func PruneDeadVhosts() {
	matches, _ := filepath.Glob(filepath.Join(paths.NginxSitesEnabled, "*.conf"))
	for _, p := range matches {
		base := filepath.Base(p)
		// Skip the panel + catch-all — those don't have a site user.
		if base == "00-panel.conf" || base == "00-default.conf" {
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		var owner string
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			m := logPathRE.FindStringSubmatch(sc.Text())
			if m == nil {
				continue
			}
			lp := m[1]
			if !strings.HasPrefix(lp, paths.HomeBase+"/") {
				continue
			}
			// /home/<user>/logs/access.log → <user>
			rest := strings.TrimPrefix(lp, paths.HomeBase+"/")
			if i := strings.IndexByte(rest, '/'); i > 0 {
				owner = rest[:i]
				break
			}
		}
		_ = f.Close()
		if owner == "" {
			continue
		}
		if _, err := user.Lookup(owner); err == nil {
			continue // user exists — live site, keep
		}
		// Orphan: remove the sites-enabled symlink AND the
		// sites-available file (no other artifact references it).
		_ = os.Remove(p)
		_ = os.Remove(filepath.Join(paths.NginxSitesAvailable, base))
	}
}

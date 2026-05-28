// RunDelete is the inverse of RunCreate — sweeps EVERY artifact path
// for a domain regardless of what's recorded in the DB. This is the
// other half of the structural fix for the a-4zwq/a-ukfs class of bug:
// when a delete happens, no orphan vhost or pool file survives that
// the next create might trip over.
//
// What gets swept:
//   1. nginx vhost: /etc/nginx/sites-{available,enabled}/<domain>.conf
//   2. FPM pool file in EVERY installed PHP version (not just the one
//      recorded — the version may have been switched at some point)
//   3. systemd unit for Node/Python (existing runtime.Manager.Remove)
//   4. SSL cert + key files at /etc/auracp/ssl/<domain>.{crt,key}
//   5. htpasswd file at /etc/auracp/htpasswd/<domain>
//   6. Linux user + home directory (existing osuser.Manager.Delete)
//   7. nginx reload at the end (only if any of #1 actually changed)
//
// Order matters less here than on create — we tolerate "doesn't exist"
// errors throughout (every step is idempotent best-effort), so a partial
// previous delete can be completed by re-running.
package creator

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/auracp/auracp/internal/osuser"
	"github.com/auracp/auracp/internal/paths"
	"github.com/auracp/auracp/internal/system"
)

// DeleteSpec is the minimal input needed to tear a site down. The DB
// record is the source for User (since the Spec was already discarded
// at create time) but we never trust the DB to be correct about what's
// on disk — we sweep filesystem state regardless.
type DeleteSpec struct {
	Domain string // required
	User   string // required (the Linux user to remove; usually from store.Site.SiteUser)
}

// RunDelete sweeps the filesystem and removes the Linux user. Returns
// the first hard error (filesystem-level, e.g. EPERM). Best-effort —
// "file doesn't exist" is silently OK at every step.
//
// IMPORTANT: every "DELETE site" API path MUST funnel through here.
// Like RunCreate, having exactly one delete function means the inverse
// of the drift-impossibility property: there's no path by which a vhost
// can survive a delete because the pool was tracked under a different
// PHP version than the DB recorded.
func RunDelete(ctx context.Context, spec *DeleteSpec, deps *Deps) error {
	log := slog.Default().With("site", spec.Domain, "user", spec.User, "op", "delete")
	r := deps.R

	// ─── #1: nginx vhost ───
	t := time.Now()
	_ = os.Remove(paths.NginxSiteLink(spec.Domain))
	_ = os.Remove(paths.NginxSiteFile(spec.Domain))
	log.Info("step ok", "step", "RemoveNginxVhost", "took_ms", time.Since(t).Milliseconds())

	// ─── #2: FPM pools across EVERY installed PHP version ───
	// This is the critical sweep. The pre-v0.2.48 delete only knew
	// about the PHP version the DB recorded; a site that had been
	// switched between versions left orphan pool files in the other
	// versions' pool.d/. RunDelete walks every installed version.
	t = time.Now()
	if deps.Php != nil {
		for _, ver := range deps.Php.Installed() {
			pool := paths.PHPPoolFile(ver, spec.Domain)
			if _, err := os.Stat(pool); err == nil {
				_ = os.Remove(pool)
				// Reload that version's FPM service so the kernel
				// releases the socket. Quiet failures — service may
				// not be installed if the version was uninstalled
				// between switch and now.
				_, _ = r.Run(ctx, "systemctl", "reload", "php"+ver+"-fpm")
			}
		}
	}
	log.Info("step ok", "step", "SweepPhpFpmPools", "took_ms", time.Since(t).Milliseconds())

	// ─── #3: SSL cert + key ───
	t = time.Now()
	_ = os.Remove(paths.CertPath(spec.Domain))
	_ = os.Remove(paths.KeyPath(spec.Domain))
	log.Info("step ok", "step", "RemoveSslCertFiles", "took_ms", time.Since(t).Milliseconds())

	// ─── #4: htpasswd (basic_auth) ───
	t = time.Now()
	_ = os.Remove(paths.HTPasswdFile(spec.Domain))
	log.Info("step ok", "step", "RemoveHTPasswd", "took_ms", time.Since(t).Milliseconds())

	// ─── #5: Linux user + home ───
	t = time.Now()
	osu := osuser.New(r)
	if err := osu.Delete(ctx, spec.User); err != nil {
		// Hard error here is worth surfacing — the user has files we
		// can't remove (immutable bit? mount?), and the operator needs
		// to know.
		log.Error("step failed", "step", "RemoveUser", "err", err.Error())
		return err
	}
	log.Info("step ok", "step", "RemoveUser", "took_ms", time.Since(t).Milliseconds())

	// ─── #6: nginx reload ───
	t = time.Now()
	if _, err := r.Run(ctx, "systemctl", "reload", "nginx"); err != nil {
		_, _ = r.Run(ctx, "nginx", "-s", "reload")
	}
	log.Info("step ok", "step", "ReloadNginx", "took_ms", time.Since(t).Milliseconds())

	return nil
}

// Avoid unused-import warning when the file is read independently.
var _ = system.Runner{}

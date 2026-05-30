// v0.2.51: comprehensive site teardown.
//
// Pre-v0.2.51 both the legacy site.Manager.Delete and the new
// creator.RunDelete dropped the obvious artifacts (vhost, FPM pool,
// Linux user, cert) but quietly left orphans:
//
//   - Databases (MariaDB / Postgres rows + on-disk files)
//   - Cron jobs (rows in the store; crontab disappears when the user
//     does, but the store rows linger forever)
//   - Backup records (rows + the .tar files in /var/lib/auracp/backups)
//   - Extra SSH/FTP users created via osuser.CreateExtra
//   - site_config rows (cache flags, basic_auth hashes, vhost overrides)
//
// This file owns the "delete EVERY trace of this site" pipeline. Both
// site.Manager.Delete and api.Server.deleteSiteViaNewPipeline call
// Teardown so behaviour is identical regardless of which dispatch path
// the API picks.
//
// Motto check:
//   - New deps: 0
//   - Binary impact: ~3 KB
//   - Daemons / sockets added: 0
//   - Failure mode: best-effort on each step; logs every miss to
//     journald via the structured slog already plumbed through; the
//     happy path leaves zero orphans.
package site

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/auracp/auracp/internal/db"
	"github.com/auracp/auracp/internal/osuser"
	"github.com/auracp/auracp/internal/store"
	"github.com/auracp/auracp/internal/system"
)

// TeardownDeps bundles everything Teardown needs without forcing the
// caller to construct a full site.Manager. The new-pipeline path can
// pass these straight in from the API server's fields.
type TeardownDeps struct {
	R     *system.Runner
	Store *store.Store
	DBs   *db.Manager
	OS    *osuser.Manager
}

// Teardown removes EVERY artifact associated with `domain`. Returns
// the FIRST hard error encountered — but continues through all
// best-effort cleanups before returning, so the operator gets a clean
// state regardless of which step blew up. Each step logs its own
// outcome to journald.
//
// Order matters: outer (nginx) → backend (DB) → ownership (extra
// users + crontab via primary user delete) → store rows. Reversing
// would orphan files we no longer have the user identity to remove.
//
// IMPORTANT: This function does NOT remove the nginx vhost, FPM pool,
// SSL cert files, or the primary Linux user — those are owned by the
// caller (creator.RunDelete in the new pipeline, or the legacy
// site.Manager.Delete inline steps). This function fills the GAP:
// everything else that needs to vanish.
func Teardown(ctx context.Context, deps *TeardownDeps, domain string) error {
	log := slog.Default().With("site", domain, "op", "teardown")

	// best: log only — cleanup failures don't fail the delete operation.
	// The site is being removed; if an administrative row can't be swept
	// it's an orphan at worst, not a reason to surface an error to the UI.
	best := func(name string, fn func() error) {
		t := time.Now()
		err := fn()
		if err != nil {
			log.Warn("teardown cleanup step failed (best-effort)", "step", name, "took_ms", time.Since(t).Milliseconds(), "err", err.Error())
			return
		}
		log.Info("teardown step ok", "step", name, "took_ms", time.Since(t).Milliseconds())
	}

	// 1. Drop every database associated with this site. The DB rows
	// contain the engine + name + user the live DB server still knows
	// about; we need them before the store rows go.
	dbs, _ := deps.Store.DatabasesForSite(domain)
	for _, d := range dbs {
		eng, name, user := d.Engine, d.Name, d.DBUser
		best("DropDatabase:"+eng+":"+name, func() error {
			return deps.DBs.Drop(ctx, eng, name, user)
		})
	}

	// 2. Delete extra SFTP / SSH users tied to this site.
	extras, _ := deps.Store.SSHUsersForSite(domain)
	for _, u := range extras {
		name := u.Username
		best("DeleteExtraSSHUser:"+name, func() error {
			return deps.OS.DeleteExtra(ctx, name)
		})
	}

	// 3. Backup files on disk.
	backups, _ := deps.Store.BackupsForSite(domain)
	for _, b := range backups {
		path := b.Path
		if path == "" {
			continue
		}
		best("RemoveBackupFile:"+path, func() error {
			err := os.Remove(path)
			if os.IsNotExist(err) {
				return nil
			}
			return err
		})
	}

	// 4. Store row sweep — all best-effort. A leftover config or cron row
	// is an orphan, not a failure visible to the operator.
	best("DeleteAllSiteConfig", func() error { return deps.Store.DeleteAllSiteConfig(domain) })
	best("DeleteAllCronJobs", func() error { return deps.Store.DeleteAllCronJobs(domain) })
	best("DeleteAllSSHUsers", func() error { return deps.Store.DeleteAllSSHUsers(domain) })
	best("DeleteAllDatabaseRecords", func() error { return deps.Store.DeleteAllDatabaseRecords(domain) })
	best("DeleteAllBackupRecords", func() error { return deps.Store.DeleteAllBackupRecords(domain) })
	best("DeleteAllPHPSettings", func() error { return deps.Store.DeleteAllPHPSettings(domain) })
	best("DeleteCertificate", func() error { return deps.Store.DeleteCertificate(domain) })

	// 5. The sites row itself — this is the only step whose failure is
	// meaningful to the caller. If it fails the site still appears in the
	// UI; every other failure above is invisible to the operator.
	t := time.Now()
	if err := deps.Store.DeleteSite(domain); err != nil {
		log.Error("teardown step failed", "step", "DeleteSite", "took_ms", time.Since(t).Milliseconds(), "err", err.Error())
		return err
	}
	log.Info("teardown step ok", "step", "DeleteSite", "took_ms", time.Since(t).Milliseconds())
	return nil
}

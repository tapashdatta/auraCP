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
	var firstErr error
	step := func(name string, fn func() error) {
		t := time.Now()
		err := fn()
		if err != nil {
			log.Error("teardown step failed", "step", name, "took_ms", time.Since(t).Milliseconds(), "err", err.Error())
			if firstErr == nil {
				firstErr = err
			}
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
		step("DropDatabase:"+eng+":"+name, func() error {
			// db.Manager.Drop drops both the DB and the user. Best-effort —
			// a missing DB (operator manually dropped it earlier) shouldn't
			// fail the whole teardown.
			return deps.DBs.Drop(ctx, eng, name, user)
		})
	}

	// 2. Delete extra SFTP / SSH users tied to this site. These are
	// Linux accounts that share the site user's home but have their
	// own /etc/passwd entries.
	extras, _ := deps.Store.SSHUsersForSite(domain)
	for _, u := range extras {
		name := u.Username
		step("DeleteExtraSSHUser:"+name, func() error {
			return deps.OS.DeleteExtra(ctx, name)
		})
	}

	// 3. Backup files. The rows themselves are deleted in step 5 (with
	// the rest of the store cleanup); here we rm the .tar files from
	// /var/lib/auracp/backups/ so they don't waste disk on a
	// long-running host with high site churn.
	backups, _ := deps.Store.BackupsForSite(domain)
	for _, b := range backups {
		path := b.Path
		if path == "" {
			continue
		}
		step("RemoveBackupFile:"+path, func() error {
			err := os.Remove(path)
			if os.IsNotExist(err) {
				return nil
			}
			return err
		})
	}

	// 4. Store row sweep. ORDER: dependent tables first (so if any
	// query fails, the FKs / indexes are still consistent at the
	// moment of crash), then `sites` itself. Each is best-effort.
	step("DeleteAllSiteConfig", func() error { return deps.Store.DeleteAllSiteConfig(domain) })
	step("DeleteAllCronJobs", func() error { return deps.Store.DeleteAllCronJobs(domain) })
	step("DeleteAllSSHUsers", func() error { return deps.Store.DeleteAllSSHUsers(domain) })
	step("DeleteAllDatabaseRecords", func() error { return deps.Store.DeleteAllDatabaseRecords(domain) })
	step("DeleteAllBackupRecords", func() error { return deps.Store.DeleteAllBackupRecords(domain) })
	step("DeleteAllPHPSettings", func() error { return deps.Store.DeleteAllPHPSettings(domain) })
	step("DeleteCertificate", func() error { return deps.Store.DeleteCertificate(domain) })
	// `sites` itself comes last — it's the parent row in the conceptual
	// model. Removing it earlier would leave dangling FKs in tables
	// without ON DELETE CASCADE (which is all of them).
	step("DeleteSite", func() error { return deps.Store.DeleteSite(domain) })

	return firstErr
}

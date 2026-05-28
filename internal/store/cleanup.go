// v0.2.50: bulk-delete helpers used by the site-teardown path. Schema
// doesn't cascade on `DELETE FROM sites` (only panel_users.sessions
// has ON DELETE CASCADE). Without these, deleting a site leaves
// orphan rows in site_config, cron_jobs, backups, ssh_users, and
// databases — surfacing later as "ghost cron job", "DB shown in UI
// but file gone", etc.
//
// Every method is best-effort: returns nil if the rows didn't exist.
// The caller (api/sites_creator.go) treats failures as advisory; the
// goal is no orphan rows when the happy path completes, with clear
// errors for the unhappy path.
package store

// DeleteAllSiteConfig removes every key/value row for a domain from
// site_config. Used at site teardown to clear cache flags, basic_auth
// creds, block_bots toggle, vhost_override text, etc.
func (s *Store) DeleteAllSiteConfig(domain string) error {
	_, err := s.DB.Exec(`DELETE FROM site_config WHERE domain = ?`, domain)
	return err
}

// DeleteAllCronJobs removes every cron entry for a domain. Note: this
// only removes the STORE records; the crontab on disk for the site
// user gets rewritten separately via cron.Manager.Apply (or simply
// disappears when the user is deleted by osuser).
func (s *Store) DeleteAllCronJobs(domain string) error {
	_, err := s.DB.Exec(`DELETE FROM cron_jobs WHERE domain = ?`, domain)
	return err
}

// DeleteAllSSHUsers removes every extra SSH/FTP user row for a domain.
// The actual Linux users are deleted via osuser.DeleteExtra by the
// caller; this just clears the panel-side records.
func (s *Store) DeleteAllSSHUsers(domain string) error {
	_, err := s.DB.Exec(`DELETE FROM ssh_users WHERE domain = ?`, domain)
	return err
}

// DeleteAllDatabaseRecords removes every db row for a domain. The
// actual DBs are dropped via db.Manager.Drop by the caller; this just
// clears the panel-side records.
func (s *Store) DeleteAllDatabaseRecords(domain string) error {
	_, err := s.DB.Exec(`DELETE FROM databases WHERE domain = ?`, domain)
	return err
}

// DeleteAllBackupRecords removes every backup row for a domain. The
// .tar files on disk are removed by the caller (via the paths in each
// row, fetched first). Returns the paths the caller should rm before
// deleting the rows.
func (s *Store) DeleteAllBackupRecords(domain string) error {
	_, err := s.DB.Exec(`DELETE FROM backups WHERE domain = ?`, domain)
	return err
}

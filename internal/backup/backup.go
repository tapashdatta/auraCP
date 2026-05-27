// Package backup creates per-site archives (document root + database dumps) and
// applies a retention policy. Remote upload is delegated to rclone when a
// provider is configured.
package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/auracp/auracp/internal/paths"
	"github.com/auracp/auracp/internal/store"
	"github.com/auracp/auracp/internal/system"
	"github.com/auracp/auracp/internal/validate"
)

const (
	BaseDir   = "/var/lib/auracp/backups"
	keepLocal = 5 // retain the newest N local backups per site
)

type Manager struct {
	R     *system.Runner
	Store *store.Store
}

func New(r *system.Runner, st *store.Store) *Manager { return &Manager{R: r, Store: st} }

// CreateSite archives a site's document root (and dumps its databases) into a
// timestamped tarball, records it, and prunes old local backups.
func (m *Manager) CreateSite(ctx context.Context, domain string) (store.Backup, error) {
	st, err := m.Store.SiteByDomain(domain)
	if err != nil {
		return store.Backup{}, err
	}
	if err := validate.Domain(domain); err != nil {
		return store.Backup{}, err
	}
	ts := time.Now().UTC().Format("2006-01-02_15-04-05")
	dir := filepath.Join(BaseDir, domain)
	archive := filepath.Join(dir, ts+".tar.gz")

	if !m.R.DryRun {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return store.Backup{}, err
		}
	}
	// Dump databases first, into the docroot so they land inside the archive.
	dbs, _ := m.Store.DatabasesForSite(domain)
	for _, d := range dbs {
		dumpInto := filepath.Join(paths.DocRoot(st.SiteUser, domain), fmt.Sprintf(".auracp-%s-%s.sql", d.Engine, d.Name))
		switch d.Engine {
		case "mariadb":
			_, _ = m.R.Run(ctx, "sh", "-c", fmt.Sprintf("mariadb-dump --single-transaction %q > %q", d.Name, dumpInto))
		case "postgres":
			_, _ = m.R.RunAs(ctx, "postgres", "sh", "-c", fmt.Sprintf("pg_dump %q > %q", d.Name, dumpInto))
		}
	}
	// tar the document root.
	if _, err := m.R.Run(ctx, "tar", "-czf", archive,
		"-C", paths.SiteHome(st.SiteUser), filepath.Join("htdocs", domain)); err != nil {
		return store.Backup{}, fmt.Errorf("archive: %w", err)
	}

	var size int64
	if fi, err := os.Stat(archive); err == nil {
		size = fi.Size()
	}
	rec := store.Backup{SiteDomain: domain, Kind: "site", Path: archive, SizeBytes: size}
	if err := m.Store.AddBackup(rec); err != nil {
		return store.Backup{}, err
	}
	m.prune(domain)
	return rec, nil
}

func (m *Manager) prune(domain string) {
	old, err := m.Store.OldBackupPaths(domain, keepLocal)
	if err != nil {
		return
	}
	for _, p := range old {
		if !m.R.DryRun {
			_ = os.Remove(p)
		}
		_ = m.Store.DeleteBackupByPath(p)
	}
}

// PushRemote uploads a backup to a configured rclone remote (e.g. "s3:bucket/path").
func (m *Manager) PushRemote(ctx context.Context, localPath, remote string) error {
	if remote == "" {
		return fmt.Errorf("no remote configured")
	}
	_, err := m.R.Run(ctx, "rclone", "copy", localPath, remote)
	return err
}

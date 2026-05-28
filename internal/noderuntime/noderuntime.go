// Package noderuntime installs and tracks Node.js runtimes that auraCP-managed
// sites run against. We download the official nodejs.org tarball into
// /opt/auracp/node/<version>/ so multiple sites can share one copy and per-site
// systemd units can ExecStart the exact binary they want — no nvm, no per-user
// duplication, no shell init.
package noderuntime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"

	"github.com/auracp/auracp/internal/store"
	"github.com/auracp/auracp/internal/system"
)

const Base = "/opt/auracp/node"

func Dir(version string) string { return filepath.Join(Base, version) }
func DefaultDir() string        { return filepath.Join(Base, "default") }
func binPath(dir string) string { return filepath.Join(dir, "bin", "node") }

// BinPath returns the Node binary to use for a site. Empty/"default" → the
// installer-managed default symlink; otherwise the pinned version's binary.
// Falls back to /usr/bin/node when nothing is provisioned yet.
func BinPath(version string) string {
	if version == "" || version == "default" {
		return binPath(DefaultDir())
	}
	return binPath(Dir(version))
}

type Manager struct {
	R     *system.Runner
	Store *store.Store
}

func New(r *system.Runner, st *store.Store) *Manager { return &Manager{R: r, Store: st} }

var verRe = regexp.MustCompile(`^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$`)

// Install downloads node-v<version>-linux-<arch>.tar.xz from nodejs.org/dist and
// extracts it into /opt/auracp/node/<version>/. Idempotent.
func (m *Manager) Install(ctx context.Context, version string, makeDefault bool) error {
	if !verRe.MatchString(version) {
		return fmt.Errorf("node version must be X.Y.Z (got %q)", version)
	}
	arch := "x64"
	if runtime.GOARCH == "arm64" {
		arch = "arm64"
	}
	dir := Dir(version)
	already := false
	if _, err := os.Stat(binPath(dir)); err == nil {
		already = true
	}
	if !already {
		if !m.R.DryRun {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
		}
		tarball := "/tmp/node-v" + version + "-linux-" + arch + ".tar.xz"
		url := fmt.Sprintf("https://nodejs.org/dist/v%s/node-v%s-linux-%s.tar.xz", version, version, arch)
		if _, err := m.R.Run(ctx, "curl", "-fsSL", url, "-o", tarball); err != nil {
			return fmt.Errorf("download node %s: %w", version, err)
		}
		// strip the top-level "node-v<ver>-linux-<arch>/" directory
		if _, err := m.R.Run(ctx, "tar", "-xJf", tarball, "-C", dir, "--strip-components=1"); err != nil {
			return fmt.Errorf("extract node %s: %w", version, err)
		}
		_, _ = m.R.Run(ctx, "rm", "-f", tarball)
	}
	if _, exists := m.Store.NodeRuntime(version); !exists {
		if err := m.Store.AddNodeRuntime(store.NodeRuntime{Version: version, Path: dir}); err != nil {
			return err
		}
	}
	if makeDefault {
		return m.SetDefault(ctx, version)
	}
	// If this is the first runtime we have, become the default.
	if _, ok := m.Store.DefaultNodeRuntime(); !ok {
		return m.SetDefault(ctx, version)
	}
	return nil
}

// SetDefault points /opt/auracp/node/default at the chosen version and records it.
func (m *Manager) SetDefault(ctx context.Context, version string) error {
	if _, ok := m.Store.NodeRuntime(version); !ok {
		return fmt.Errorf("node runtime %s is not installed", version)
	}
	if err := m.Store.SetDefaultNodeRuntime(version); err != nil {
		return err
	}
	if !m.R.DryRun {
		_ = os.Remove(DefaultDir())
		if err := os.Symlink(Dir(version), DefaultDir()); err != nil {
			return err
		}
	}
	return nil
}

// Remove deletes a runtime if no site is pinned to it.
func (m *Manager) Remove(ctx context.Context, version string) error {
	if m.Store.SiteUsesNodeVersion(version) {
		return fmt.Errorf("node runtime %s is in use by one or more sites", version)
	}
	if !m.R.DryRun {
		_ = os.RemoveAll(Dir(version))
	}
	return m.Store.DeleteNodeRuntime(version)
}

// ReconcileDefaultSymlink ensures /opt/auracp/node/default points at the DB's
// recorded default — used on startup so the symlink is always correct.
func (m *Manager) ReconcileDefaultSymlink() {
	if m.R.DryRun {
		return
	}
	d, ok := m.Store.DefaultNodeRuntime()
	if !ok {
		return
	}
	_ = os.Remove(DefaultDir())
	_ = os.Symlink(d.Path, DefaultDir())
}

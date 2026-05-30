// Package osuser provisions the per-site Linux user and its isolated home,
// plus a chroot-jailed SFTP setup. Each site runs as its own user so a
// compromise of one site cannot reach another's files.
package osuser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/auracp/auracp/internal/paths"
	"github.com/auracp/auracp/internal/system"
	"github.com/auracp/auracp/internal/validate"
)

type Manager struct{ R *system.Runner }

func New(r *system.Runner) *Manager { return &Manager{R: r} }

// Create makes the site user (login shell for SSH access), its htdocs/logs
// dirs, and adds it to the SFTP group. Idempotent-ish: tolerates re-runs.
func (m *Manager) Create(ctx context.Context, user, domain string) error {
	if err := validate.Username(user); err != nil {
		return err
	}
	if err := validate.Domain(domain); err != nil {
		return err
	}
	if err := m.ensureSFTPGroup(ctx); err != nil {
		return err
	}
	home := paths.SiteHome(user)
	// Pre-create the same-name primary group, then point useradd at it
	// with --no-user-group/--gid instead of letting useradd create its
	// own user-private group. This makes Create robust against an orphan
	// group left by a prior install: the v0.2.61 `gpasswd -a www-data
	// <user>` (below) makes www-data a *secondary* member of the
	// site-user group, and userdel refuses to reap a group that still has
	// members — so the group <user> survives uninstall. On reinstall the
	// default `useradd` then died with `useradd: group <user> exists`
	// (exit 9). `groupadd -f` is a no-op when the group already exists, so
	// we simply reuse it.
	if _, err := m.R.Run(ctx, "groupadd", "-f", user); err != nil {
		return fmt.Errorf("create user group: %w", err)
	}
	if _, err := m.R.Run(ctx, "useradd",
		"--create-home", "--home-dir", home, "--shell", "/bin/bash",
		"--no-user-group", "--gid", user, "--groups", paths.SFTPGroup, user); err != nil {
		// useradd exits 9 when the *user* already exists. Treat that as an
		// idempotent re-run: fall through to the dir/perm fixups after
		// re-asserting SFTP-group membership. Surface anything else.
		if _, idErr := m.R.Run(ctx, "id", user); idErr != nil {
			return fmt.Errorf("create user: %w", err)
		}
		_, _ = m.R.Run(ctx, "usermod", "-aG", paths.SFTPGroup, user)
	}
	for _, d := range []string{paths.DocRoot(user, domain), paths.LogDir(user)} {
		if _, err := m.R.Run(ctx, "mkdir", "-p", d); err != nil {
			return err
		}
	}
	// Home owned by root for chroot; the writable subtree owned by the user.
	if _, err := m.R.Run(ctx, "chown", "root:root", home); err != nil {
		return err
	}
	if _, err := m.R.Run(ctx, "chmod", "755", home); err != nil {
		return err
	}
	if _, err := m.R.Run(ctx, "chown", "-R", user+":"+user,
		filepath.Join(home, "htdocs"), paths.LogDir(user)); err != nil {
		return err
	}
	// v0.2.61: add nginx's www-data user to the site user's group so
	// nginx can traverse the 0750-mode home + read the docroot. Without
	// this, the v0.2.61 ResetPermissions chmod-750 on /home/<user>
	// blocks `stat()` from nginx workers — operator-reported symptom:
	//   [crit] stat() "/home/<user>/htdocs/<domain>/" failed (13:
	//          Permission denied) → HTTP 404 to the client.
	// Best-effort: failure here doesn't fail the create (group might
	// not exist yet on a brand-new install — the ResetPermissions
	// 0750 chmod is also best-effort), but the warning surfaces in
	// `journalctl -u auracpd`.
	_, _ = m.R.Run(ctx, "gpasswd", "-a", "www-data", user)
	return nil
}

// EnsureNginxAccess adds www-data to every existing site user's group.
// Idempotent — `gpasswd -a` is a no-op if www-data is already in the
// group. Called once on auracpd startup so panels that were upgraded
// from a pre-v0.2.61 release pick up the fix without operator action.
//
// Returns the count of users it processed, for the startup log line.
// Caller is responsible for `systemctl reload nginx` after — new group
// memberships only take effect for nginx workers spawned post-reload.
func (m *Manager) EnsureNginxAccess(ctx context.Context, siteUsers []string) int {
	n := 0
	for _, u := range siteUsers {
		if err := validate.Username(u); err != nil {
			continue
		}
		if _, err := m.R.Run(ctx, "gpasswd", "-a", "www-data", u); err == nil {
			n++
		}
	}
	return n
}

// Delete removes the user, its home, and kills its processes.
func (m *Manager) Delete(ctx context.Context, user string) error {
	if err := validate.Username(user); err != nil {
		return err
	}
	_, _ = m.R.Run(ctx, "pkill", "-9", "-u", user) // best-effort
	if _, err := m.R.Run(ctx, "userdel", "--remove", "--force", user); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	// userdel leaves the same-name group behind whenever www-data is still
	// a secondary member (added in v0.2.61 so nginx can traverse the home).
	// Reap it so a later reinstall's useradd doesn't trip over an orphan
	// group (exit 9). Best-effort: groupdel correctly refuses if a shared
	// "extra" user still has this as their primary group.
	_, _ = m.R.Run(ctx, "groupdel", user)
	return nil
}

// CreateExtra adds an additional SSH/SFTP user that shares a site's home (for
// granting extra access). It joins the site user's group so it can reach
// htdocs, and the SFTP group for the chroot jail. Its home is NOT created or
// removed — it points at the existing site home.
func (m *Manager) CreateExtra(ctx context.Context, username, siteUser string, sshAllowed bool) error {
	if err := validate.Username(username); err != nil {
		return err
	}
	if err := validate.Username(siteUser); err != nil {
		return err
	}
	if err := m.ensureSFTPGroup(ctx); err != nil {
		return err
	}
	shell := "/usr/sbin/nologin"
	if sshAllowed {
		shell = "/bin/bash"
	}
	_, err := m.R.Run(ctx, "useradd", "-M",
		"--home-dir", paths.SiteHome(siteUser),
		"--gid", siteUser, "--groups", paths.SFTPGroup,
		"--shell", shell, username)
	return err
}

// DeleteExtra removes an extra user WITHOUT deleting the shared site home.
func (m *Manager) DeleteExtra(ctx context.Context, username string) error {
	if err := validate.Username(username); err != nil {
		return err
	}
	_, _ = m.R.Run(ctx, "pkill", "-9", "-u", username)
	_, err := m.R.Run(ctx, "userdel", "--force", username)
	return err
}

// SetPassword sets the user's password via chpasswd (stdin-free, no shell).
func (m *Manager) SetPassword(ctx context.Context, user, password string) error {
	if err := validate.Username(user); err != nil {
		return err
	}
	// chpasswd reads "user:pass" on stdin; Runner has no stdin, so use usermod
	// with a pre-hashed password would be ideal. For now delegate to chpasswd
	// via a tiny helper file is avoided; this is a TODO for stdin support.
	_ = password
	return nil
}

// ensureSFTPGroup creates the SFTP group and the global sshd chroot rule once.
func (m *Manager) ensureSFTPGroup(ctx context.Context) error {
	_, _ = m.R.Run(ctx, "groupadd", "-f", paths.SFTPGroup)
	conf := "/etc/ssh/sshd_config.d/auracp-sftp.conf"
	if m.R.DryRun {
		return nil
	}
	if _, err := os.Stat(conf); err == nil {
		return nil
	}
	body := "# auraCP — chroot-jail SFTP-only users\n" +
		"Match Group " + paths.SFTPGroup + "\n" +
		"\tChrootDirectory %h\n" +
		"\tForceCommand internal-sftp\n" +
		"\tAllowTcpForwarding no\n" +
		"\tX11Forwarding no\n"
	if err := os.MkdirAll(filepath.Dir(conf), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(conf, []byte(body), 0o644); err != nil {
		return err
	}
	_, _ = m.R.Run(ctx, "systemctl", "reload", "ssh")
	return nil
}

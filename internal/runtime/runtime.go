// Package runtime writes and manages the per-site systemd unit that runs an
// application backend for Node.js and Python sites (gunicorn / node /
// pm2-runtime). Each unit runs as the site's own user behind nginx.
//
// v0.2.0: PHP sites no longer get a per-site systemd unit — they're served
// by a per-site pool inside the shared php<ver>-fpm service, owned by the
// internal/phpruntime package. Apply() returns nil (no-op) for php/wordpress.
package runtime

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/auracp/auracp/internal/noderuntime"
	"github.com/auracp/auracp/internal/paths"
	"github.com/auracp/auracp/internal/system"
	"github.com/auracp/auracp/internal/validate"
)

type Manager struct{ R *system.Runner }

func New(r *system.Runner) *Manager { return &Manager{R: r} }

// Spec describes a backend service for one site. Carried over from the
// FrankenPHP era; PHPVer is no longer used here (handled in phpruntime).
type Spec struct {
	Type      string // nodejs|python (php/wordpress short-circuit; static/reverseproxy never reach here)
	Domain    string
	User      string
	Port      int
	StartFile string // nodejs: app.js
	Module    string // python: main:app
	PHPVer    string // retained for source compat; ignored
	NodeVer   string // nodejs: "" or "default" → /opt/auracp/node/default; else /opt/auracp/node/<ver>
	UsePM2    bool   // nodejs: run app via pm2-runtime (foreground), not plain node
}

// Apply writes the unit file and (re)starts it. PHP/WordPress sites are
// handled by phpruntime.WritePool — we no-op here so existing callers stay
// unchanged.
func (m *Manager) Apply(ctx context.Context, s Spec) error {
	switch s.Type {
	case "php", "wordpress":
		return nil // owned by internal/phpruntime
	case "nodejs", "python":
		// continue below
	default:
		return fmt.Errorf("type %q has no backend", s.Type)
	}

	if err := validate.Username(s.User); err != nil {
		return err
	}
	if err := validate.Domain(s.Domain); err != nil {
		return err
	}
	if err := validate.Port(s.Port); err != nil {
		return err
	}
	execLine, err := execStart(s)
	if err != nil {
		return err
	}
	unit := fmt.Sprintf(`[Unit]
Description=auraCP site %s (%s)
After=network-online.target

[Service]
Type=simple
User=%s
Group=%s
WorkingDirectory=%s
ExecStart=%s
Restart=always
RestartSec=3
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=%s
PrivateTmp=true

[Install]
WantedBy=multi-user.target
`, s.Domain, s.Type, s.User, s.User,
		paths.DocRoot(s.User, s.Domain), execLine, paths.SiteHome(s.User))

	name := paths.UnitName(s.Domain)
	if !m.R.DryRun {
		if err := os.WriteFile(paths.UnitFile(s.Domain), []byte(unit), 0o644); err != nil {
			return err
		}
	}
	if _, err := m.R.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	_, err = m.R.Run(ctx, "systemctl", "enable", "--now", name)
	return err
}

// Remove tears down a site's per-site systemd unit. PHP/WordPress site removal
// is handled by phpruntime.RemovePool — this is a no-op for those types.
func (m *Manager) Remove(ctx context.Context, domain string) error {
	if err := validate.Domain(domain); err != nil {
		return err
	}
	name := paths.UnitName(domain)
	_, _ = m.R.Run(ctx, "systemctl", "disable", "--now", name)
	if !m.R.DryRun {
		_ = os.Remove(paths.UnitFile(domain))
	}
	_, err := m.R.Run(ctx, "systemctl", "daemon-reload")
	return err
}

func execStart(s Spec) (string, error) {
	root := paths.DocRoot(s.User, s.Domain)
	port := strconv.Itoa(s.Port)
	switch s.Type {
	case "nodejs":
		start := s.StartFile
		if start == "" {
			start = "app.js"
		}
		if s.UsePM2 {
			// pm2-runtime stays foreground (no separate pm2 daemon), so the systemd
			// unit owns the lifecycle. The PM2 process name is the site's domain.
			return fmt.Sprintf("%s --name %s %s/%s", noderuntime.PM2Path(s.NodeVer), s.Domain, root, start), nil
		}
		return fmt.Sprintf("%s %s/%s", noderuntime.BinPath(s.NodeVer), root, start), nil
	case "python":
		mod := s.Module
		if mod == "" {
			mod = "main:app"
		}
		return fmt.Sprintf("/usr/bin/gunicorn --chdir %s --bind 127.0.0.1:%s %s", root, port, mod), nil
	}
	return "", fmt.Errorf("type %q has no backend", s.Type)
}

// Package runtime writes and manages the per-site systemd unit that runs the
// application backend (FrankenPHP for PHP/WordPress, node for Node.js, gunicorn
// for Python). Each unit runs as the site's own user behind the front Caddy.
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

// Spec describes a backend service for one site.
type Spec struct {
	Type      string // php|wordpress|nodejs|python
	Domain    string
	User      string
	Port      int
	StartFile string // nodejs: app.js
	Module    string // python: main:app
	PHPVer    string // php: 8.3/8.4/8.5
	NodeVer   string // nodejs: "" or "default" → /opt/auracp/node/default; else /opt/auracp/node/<ver>
	UsePM2    bool   // nodejs: run app via pm2-runtime (foreground), not plain node
}

// Apply writes the unit file and (re)starts it. Static & reverse-proxy sites
// have no backend and never call this.
func (m *Manager) Apply(ctx context.Context, s Spec) error {
	if err := validate.Username(s.User); err != nil {
		return err
	}
	if err := validate.Domain(s.Domain); err != nil {
		return err
	}
	if err := validate.Port(s.Port); err != nil {
		return err
	}
	exec, err := execStart(s)
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
		paths.DocRoot(s.User, s.Domain), exec, paths.SiteHome(s.User))

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
	case "php", "wordpress":
		if err := validate.PHPVersion(s.PHPVer); err != nil {
			return "", err
		}
		// FrankenPHP serving the docroot, listening on the site's loopback port.
		return fmt.Sprintf("/usr/bin/frankenphp php-server --listen 127.0.0.1:%s --root %s", port, root), nil
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

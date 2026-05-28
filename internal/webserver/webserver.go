// Package webserver renders the auracp control-panel's own nginx
// touchpoints — the panel vhost (00-panel.conf), the catch-all default
// vhost (00-default.conf), and the nginx reload primitive used by
// every higher-level pipeline.
//
// v0.2.52: the site-vhost rendering surface (Apply, Remove, Spec,
// Render, vhostTemplate, vhostData) was deleted in favor of
// internal/webserver/template + internal/webserver/processor +
// internal/site/creator/RunCreate/RunReapply. This package now owns
// only the auracp-internal control-plane vhosts (panel + catch-all)
// and the shared Reload entry point.
package webserver

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"text/template"

	"github.com/auracp/auracp/internal/paths"
	"github.com/auracp/auracp/internal/system"
	"github.com/auracp/auracp/internal/validate"
)

// ErrNginxMissing is returned when site/panel operations need nginx but it
// isn't installed yet. The API surfaces this verbatim so the operator knows
// to run the data-plane installer.
var ErrNginxMissing = fmt.Errorf(
	"nginx is not installed on this host. Run installer/install.sh (or sudo auracp-install) to provision the data plane.")

// ErrCaddyMissing is a backwards-compat alias kept so existing callers and
// log-grepping in older docs keep working through the v0.2.0 transition.
// Deprecated: use ErrNginxMissing.
var ErrCaddyMissing = ErrNginxMissing

type Manager struct{ R *system.Runner }

func New(r *system.Runner) *Manager { return &Manager{R: r} }

// panelData is the template binding for the panel proxy vhost.
type panelData struct {
	Domain, Backend, ACMEDir string
	CertPath, KeyPath        string
}

// ApplyCatchAll installs the default-server vhost (00-default.conf) so
// requests for unmatched server_names get dropped instead of falling back
// to whichever server block nginx loaded first (typically the panel —
// which is how freshly-created sites showed the control panel UI before
// their cert landed). Idempotent + safe to call on every daemon startup.
// v0.2.38.
func (m *Manager) ApplyCatchAll(ctx context.Context) error {
	if m.R.DryRun {
		return nil
	}
	if err := os.MkdirAll(paths.NginxSitesAvailable, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(paths.NginxSitesEnabled, 0o755); err != nil {
		return err
	}
	// If the file already has the expected content, skip the write+reload —
	// avoids a needless nginx reload on every daemon startup.
	want := []byte(catchAllTemplate)
	if existing, err := os.ReadFile(paths.CatchAllNginxFile()); err == nil && bytes.Equal(existing, want) {
		// Still ensure the symlink — operator might have removed it.
		if _, err := os.Stat(paths.CatchAllNginxLink()); err == nil {
			return nil
		}
	}
	if err := os.WriteFile(paths.CatchAllNginxFile(), want, 0o644); err != nil {
		return err
	}
	_ = os.Remove(paths.CatchAllNginxLink())
	if err := os.Symlink(paths.CatchAllNginxFile(), paths.CatchAllNginxLink()); err != nil {
		return err
	}
	return m.Reload(ctx)
}

// ApplyPanelProxy fronts the control panel under a domain on :80/:443. nginx
// reverse-proxies HTTPS traffic into auracpd's :8443 self-signed TLS. The cert
// for <domain> is issued by lego (background job in cmd/auracpd) and lands in
// paths.SSLDir, which a subsequent ReloadPanel picks up. While the cert is
// pending, the panel is reachable plaintext on :80.
func (m *Manager) ApplyPanelProxy(ctx context.Context, domain, backend string) error {
	if err := validate.Domain(domain); err != nil {
		return err
	}
	d := panelData{Domain: domain, Backend: backend, ACMEDir: paths.ACMEChallengeDir}
	if _, err := os.Stat(paths.CertPath(domain)); err == nil {
		d.CertPath = paths.CertPath(domain)
		d.KeyPath = paths.KeyPath(domain)
	}
	t, err := template.New("panel").Parse(panelTemplate)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, d); err != nil {
		return err
	}
	if !m.R.DryRun {
		if err := os.MkdirAll(paths.NginxSitesAvailable, 0o755); err != nil {
			return err
		}
		if err := os.MkdirAll(paths.NginxSitesEnabled, 0o755); err != nil {
			return err
		}
		if err := os.MkdirAll(paths.ACMEChallengeDir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(paths.PanelNginxFile(), buf.Bytes(), 0o644); err != nil {
			return err
		}
		_ = os.Remove(paths.PanelNginxLink())
		if err := os.Symlink(paths.PanelNginxFile(), paths.PanelNginxLink()); err != nil {
			return err
		}
	}
	return m.Reload(ctx)
}

// RemovePanelProxy stops fronting the panel under a domain (back to IP:port).
func (m *Manager) RemovePanelProxy(ctx context.Context) error {
	if !m.R.DryRun {
		_ = os.Remove(paths.PanelNginxLink())
		_ = os.Remove(paths.PanelNginxFile())
	}
	return m.Reload(ctx)
}

// Reload validates the config and asks nginx to reload it gracefully.
//
// Two-step strategy mirrors v0.1.17's Caddy approach:
//  1. Prefer `systemctl reload nginx` — goes through journald + honours the
//     unit's restart policy.
//  2. Fall back to `nginx -s reload` direct if systemctl returns "reload not
//     applicable" (defensive: nginx ships ExecReload in its packaged unit, so
//     this fallback is mostly belt-and-suspenders).
func (m *Manager) Reload(ctx context.Context) error {
	if !m.R.DryRun {
		if _, err := exec.LookPath("nginx"); err != nil {
			return ErrNginxMissing
		}
	}
	if _, err := m.R.Run(ctx, "nginx", "-t"); err != nil {
		return fmt.Errorf("nginx config invalid: %w", err)
	}
	if _, err := m.R.Run(ctx, "systemctl", "reload", "nginx"); err == nil {
		return nil
	}
	_, err := m.R.Run(ctx, "nginx", "-s", "reload")
	return err
}

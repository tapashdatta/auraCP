// Package webserver renders per-site nginx config and reloads nginx. Each site
// gets one server{} block in /etc/nginx/sites-available, symlinked into
// sites-enabled. TLS certs come from internal/acme (in-process lego).
//
// v0.2.0 rewrite: this package replaced the previous Caddy + Souin
// implementation. Public method signatures (Apply, Remove, ApplyPanelProxy,
// RemovePanelProxy, Reload) are unchanged so the existing call sites in
// internal/site, internal/api, and cmd/auracpd compile untouched.
package webserver

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
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

// Spec is the input the renderer needs to emit a site's nginx config. Carried
// over from the Caddy-era structure so call sites in internal/site don't change.
type Spec struct {
	Type     string // static|php|wordpress|nodejs|python|reverseproxy
	Domain   string
	User     string
	Upstream string // app types: 127.0.0.1:<port>; reverseproxy: full URL

	PHPVer        string // php/wordpress only — picks which php-fpm socket to fastcgi_pass to
	Cache         bool   // emit fastcgi_cache / proxy_cache directives
	CacheTTL      string // e.g. "600s"
	BasicAuthUser string // currently unused in the template; reserved
	BasicAuthHash string // currently unused in the template; reserved
	CloudflareTok string // hint to the SSL layer (DNS-01); not rendered into nginx
	BlockBots     bool   // emit a User-Agent deny-list

	// Filled by Apply() from the certificates table. Empty until lego issues:
	// the rendered vhost stays HTTP-only with an ACME challenge location so
	// the first issuance can complete in band.
	CertPath string
	KeyPath  string
}

// vhostData is the template binding view of Spec — keeps the template tidy.
type vhostData struct {
	Type, Domain, User, SafeName        string
	DocRoot, LogDir, PHPSocket, Upstream string
	ACMEDir                              string
	CertPath, KeyPath                    string
	Bots                                 bool
	BasicAuthUser, BasicAuthFile         string
	Cache                                bool
	CacheTTL                             string
}

// Render produces the nginx server{} config for a site.
func (m *Manager) Render(s Spec) (string, error) {
	if err := validate.Domain(s.Domain); err != nil {
		return "", err
	}
	if err := validate.Username(s.User); err != nil {
		return "", err
	}

	d := vhostData{
		Type:    s.Type,
		Domain:  s.Domain,
		User:    s.User,
		SafeName: strings.NewReplacer(".", "_", "-", "_").Replace(s.Domain),
		DocRoot: paths.DocRoot(s.User, s.Domain),
		LogDir:  paths.LogDir(s.User),
		ACMEDir: paths.ACMEChallengeDir,
		CertPath: s.CertPath,
		KeyPath:  s.KeyPath,
		Bots:    s.BlockBots,
		Cache:   s.Cache,
		CacheTTL: s.CacheTTL,
	}
	if d.CacheTTL == "" {
		d.CacheTTL = "600s"
	}

	switch s.Type {
	case "static":
		// nothing extra
	case "php", "wordpress":
		if err := validate.PHPVersion(s.PHPVer); err != nil {
			return "", err
		}
		d.PHPSocket = paths.PHPSocket(s.Domain)
	case "nodejs", "python":
		if s.Upstream == "" {
			return "", fmt.Errorf("%s site requires an upstream", s.Type)
		}
		d.Upstream = s.Upstream
	case "reverseproxy":
		if s.Upstream == "" {
			return "", fmt.Errorf("reverse proxy site requires an upstream URL")
		}
		// preserve scheme — could be http:// or https://
		d.Upstream = s.Upstream
	default:
		return "", fmt.Errorf("unknown site type: %s", s.Type)
	}

	t, err := template.New("vhost").Parse(vhostTemplate)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, d); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// Apply renders and writes the vhost, ensures the sites-enabled symlink, and
// reloads nginx. Cert paths come from a callback so the renderer doesn't reach
// into the store directly; if the site has no cert yet (still pending ACME),
// CertPath/KeyPath stay empty and the vhost is HTTP-only.
func (m *Manager) Apply(ctx context.Context, s Spec) error {
	content, err := m.Render(s)
	if err != nil {
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
		if err := os.WriteFile(paths.NginxSiteFile(s.Domain), []byte(content), 0o644); err != nil {
			return err
		}
		// Ensure the symlink exists; nginx ignores files outside sites-enabled.
		_ = os.Remove(paths.NginxSiteLink(s.Domain))
		if err := os.Symlink(paths.NginxSiteFile(s.Domain), paths.NginxSiteLink(s.Domain)); err != nil {
			return err
		}
	}
	return m.Reload(ctx)
}

func (m *Manager) Remove(ctx context.Context, domain string) error {
	if err := validate.Domain(domain); err != nil {
		return err
	}
	if !m.R.DryRun {
		_ = os.Remove(paths.NginxSiteLink(domain))
		_ = os.Remove(paths.NginxSiteFile(domain))
	}
	return m.Reload(ctx)
}

// panelData is the template binding for the panel proxy vhost.
type panelData struct {
	Domain, Backend, ACMEDir string
	CertPath, KeyPath        string
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

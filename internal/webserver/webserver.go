// Package webserver renders per-site Caddy config and reloads Caddy. Caddy
// gives us automatic HTTPS (Let's Encrypt), HTTP/3, and Souin caching for free.
package webserver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/auracp/auracp/internal/paths"
	"github.com/auracp/auracp/internal/system"
	"github.com/auracp/auracp/internal/validate"
)

// ErrCaddyMissing is returned when site/panel operations need Caddy but it
// isn't installed yet. The API surfaces this verbatim so the operator knows
// to run the data-plane installer.
var ErrCaddyMissing = fmt.Errorf(
	"Caddy is not installed on this host. Run installer/install.sh to provision the data plane (or `sudo apt install caddy`).")

type Manager struct{ R *system.Runner }

func New(r *system.Runner) *Manager { return &Manager{R: r} }

// Spec is the shape needed to render a site's Caddyfile fragment, including the
// toggleable config (cache, basic auth, Cloudflare DNS, bot blocking).
type Spec struct {
	Type     string // static|php|wordpress|nodejs|python|reverseproxy
	Domain   string
	User     string
	Upstream string // for app/proxy types: host:port or full URL

	Cache         bool   // Souin full-page cache
	CacheTTL      string // e.g. "600s"
	BasicAuthUser string // if set with hash → basic_auth
	BasicAuthHash string // bcrypt hash
	CloudflareTok string // if set → tls { dns cloudflare <tok> }
	BlockBots     bool   // block common bad user-agents
}

// Render produces the Caddyfile fragment for a site.
func (m *Manager) Render(s Spec) (string, error) {
	if err := validate.Domain(s.Domain); err != nil {
		return "", err
	}
	if err := validate.Username(s.User); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s {\n", s.Domain)
	// zstd + gzip cover every modern client. We deliberately do NOT emit `br`:
	// the stock Caddy build (caddyserver.com/api/download) doesn't ship the
	// Brotli encoder, and adding the dunglas/caddy-cbrotli plugin requires cgo —
	// not worth the extra ~3% bytes-on-the-wire for the panel/site mix.
	b.WriteString("\tencode zstd gzip\n")

	if s.CloudflareTok != "" {
		// Wildcard / DNS-01 issuance via the Cloudflare DNS module.
		fmt.Fprintf(&b, "\ttls {\n\t\tdns cloudflare %s\n\t}\n", s.CloudflareTok)
	}
	if s.BlockBots {
		b.WriteString("\t@badbots header_regexp User-Agent (?i)(ahrefsbot|semrushbot|mj12bot|dotbot|petalbot)\n")
		b.WriteString("\trespond @badbots 403\n")
	}
	if s.BasicAuthUser != "" && s.BasicAuthHash != "" {
		fmt.Fprintf(&b, "\tbasic_auth {\n\t\t%s %s\n\t}\n", s.BasicAuthUser, s.BasicAuthHash)
	}
	if s.Cache {
		ttl := s.CacheTTL
		if ttl == "" {
			ttl = "600s"
		}
		fmt.Fprintf(&b, "\tcache {\n\t\tttl %s\n\t}\n", ttl)
	}

	switch s.Type {
	case "static":
		fmt.Fprintf(&b, "\troot * %s\n", paths.DocRoot(s.User, s.Domain))
		b.WriteString("\tfile_server\n")
	case "php", "wordpress", "nodejs", "python", "reverseproxy":
		up := s.Upstream
		if up == "" {
			return "", fmt.Errorf("%s site requires an upstream", s.Type)
		}
		fmt.Fprintf(&b, "\treverse_proxy %s\n", up)
	default:
		return "", fmt.Errorf("unknown site type: %s", s.Type)
	}
	fmt.Fprintf(&b, "\tlog {\n\t\toutput file %s/access.log\n\t}\n", paths.LogDir(s.User))
	b.WriteString("}\n")
	return b.String(), nil
}

// Write renders and writes the fragment, then reloads Caddy.
func (m *Manager) Apply(ctx context.Context, s Spec) error {
	content, err := m.Render(s)
	if err != nil {
		return err
	}
	if !m.R.DryRun {
		if err := os.MkdirAll(paths.CaddySitesDir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(paths.CaddyFile(s.Domain), []byte(content), 0o644); err != nil {
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
		_ = os.Remove(paths.CaddyFile(domain))
	}
	return m.Reload(ctx)
}

// ApplyPanelProxy fronts the control panel under a domain: Caddy obtains a real
// Let's Encrypt cert for <domain> on :443 and reverse-proxies to the local
// auracpd. Writing this (with Caddy running + DNS pointed here) triggers
// automatic certificate issuance.
func (m *Manager) ApplyPanelProxy(ctx context.Context, domain, backend string) error {
	if err := validate.Domain(domain); err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s {\n\tencode zstd gzip\n", domain)
	if strings.HasPrefix(backend, "https://") {
		// loopback to auracpd's self-signed TLS — skip-verify is safe on 127.0.0.1
		fmt.Fprintf(&b, "\treverse_proxy %s {\n\t\ttransport http {\n\t\t\ttls_insecure_skip_verify\n\t\t}\n\t}\n", backend)
	} else {
		fmt.Fprintf(&b, "\treverse_proxy %s\n", backend)
	}
	b.WriteString("}\n")
	if !m.R.DryRun {
		if err := os.MkdirAll(paths.CaddySitesDir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(paths.PanelCaddyFile(), []byte(b.String()), 0o644); err != nil {
			return err
		}
	}
	return m.Reload(ctx)
}

// RemovePanelProxy stops fronting the panel under a domain (back to IP:port).
func (m *Manager) RemovePanelProxy(ctx context.Context) error {
	if !m.R.DryRun {
		_ = os.Remove(paths.PanelCaddyFile())
	}
	return m.Reload(ctx)
}

// Reload validates the config, then asks Caddy to reload it gracefully.
//
// Two-step strategy:
//  1. Prefer `systemctl reload caddy` — goes through journald + honours the
//     unit's restart policy.
//  2. Fall back to `caddy reload --config … --force` (talks to Caddy's local
//     admin endpoint directly). Pre-v0.1.17 hosts shipped a Caddy unit
//     without ExecReload= and systemctl reload returns exit 3 ("Job type
//     reload is not applicable") — without this fallback those hosts would
//     load /etc/caddy/sites/00-panel.caddy on disk but Caddy would never
//     re-read it, leaving :80/:443 unbound and producing CF 521s.
func (m *Manager) Reload(ctx context.Context) error {
	if !m.R.DryRun {
		if _, err := exec.LookPath("caddy"); err != nil {
			return ErrCaddyMissing
		}
	}
	if _, err := m.R.Run(ctx, "caddy", "validate", "--config", "/etc/caddy/Caddyfile"); err != nil {
		return fmt.Errorf("caddy config invalid: %w", err)
	}
	if _, err := m.R.Run(ctx, "systemctl", "reload", "caddy"); err == nil {
		return nil
	}
	_, err := m.R.Run(ctx, "caddy", "reload", "--config", "/etc/caddy/Caddyfile", "--force")
	return err
}

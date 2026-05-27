// Package webserver renders per-site Caddy config and reloads Caddy. Caddy
// gives us automatic HTTPS (Let's Encrypt), HTTP/3, and Souin caching for free.
package webserver

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/auracp/auracp/internal/paths"
	"github.com/auracp/auracp/internal/system"
	"github.com/auracp/auracp/internal/validate"
)

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
	b.WriteString("\tencode zstd br gzip\n")

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

// Reload validates the config, then asks Caddy to reload it gracefully.
func (m *Manager) Reload(ctx context.Context) error {
	if _, err := m.R.Run(ctx, "caddy", "validate", "--config", "/etc/caddy/Caddyfile"); err != nil {
		return fmt.Errorf("caddy config invalid: %w", err)
	}
	_, err := m.R.Run(ctx, "systemctl", "reload", "caddy")
	return err
}

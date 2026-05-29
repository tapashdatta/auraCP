// Package processor renders the per-placeholder substitutions for a site
// vhost template. Mirrors CloudPanel's processor architecture: each
// placeholder ({{server_name}}, {{root}}, {{ssl_certificate}}, …) is a
// single-responsibility function that reads from one SiteContext and
// returns the template body with that one placeholder substituted.
//
// The contract is intentionally narrow — every processor signature is
// the same `func(in string, ctx *SiteContext) string`, so adding a new
// placeholder is one new file with one new function. Composition lives
// in internal/webserver/template/<type>.go, which picks the set of
// processors that match its template body.
//
// Why functions, not classes-with-Placeholder()-method (the CloudPanel
// shape): in Go the placeholder string is inseparable from the
// substitution logic — both live in the same file — so an interface
// with both methods would always have one trivial implementation. A
// plain func is more idiomatic and trivially mockable for tests.
package processor

import "strings"

// SiteContext is the single source of truth that every processor reads
// from. Populated once per render by internal/site/creator before the
// Template.Build() call. Every artifact that gets written for this site
// (vhost, FPM pool, cert files, chown target) must derive from the same
// SiteContext fields — that's the structural property that prevents the
// "vhost says user A, pool says user B" drift class.
type SiteContext struct {
	// ─── common to every site type ───
	Type    string // "php" | "wordpress" | "nodejs" | "python" | "static" | "reverseproxy"
	Domain  string // e.g. "a.garuda.sh"
	User    string // Linux user, e.g. "a-ukfs"
	DocRoot string // already resolved: /home/<user>/htdocs/<domain>
	LogDir  string // /home/<user>/logs

	// ─── TLS state ───
	// Empty until lego issues — vhost stays HTTP-only with an ACME
	// challenge location so the first issuance can complete in band.
	CertPath   string // /etc/auracp/ssl/<domain>.crt
	KeyPath    string // /etc/auracp/ssl/<domain>.key
	ForceHttps bool   // v0.2.57: when true AND CertPath != "", emit a
	// :80 → :443 301 redirect at {{force_https}}. Gated on CertPath so
	// pre-issuance sites don't trap visitors in a TLS-handshake loop.

	// ─── PHP / WordPress only ───
	PHPVersion  string            // "8.3"
	FPMSocket   string            // /run/php-fpm/<domain>.sock
	PHPSettings map[string]string // memory_limit, upload_max_filesize, post_max_size, max_execution_time, max_input_vars

	// ─── Node.js / Python only ───
	AppPort int // loopback port the systemd unit listens on

	// ─── Reverse Proxy only ───
	ReverseProxyURL string // user-supplied upstream (preserves scheme)

	// ─── operator escape hatch (Vhost-tab textarea) ───
	// Freeform nginx config injected at the {{settings}} placeholder.
	// Goes through `nginx -t` before being made live so syntactically
	// broken edits never reach the running server.
	Settings string

	// ─── per-site feature toggles ───
	Cache         bool   // emit fastcgi_cache / proxy_cache via {{settings}} merge
	CacheTTL      string // e.g. "600s" — applied to fastcgi_cache_valid
	BlockBots     bool   // emit a User-Agent deny-list via {{settings}} merge
	BasicAuthUser string // shown verbatim in the rendered htpasswd file (also drives auth_basic in the vhost)
	BasicAuthFile string // /etc/nginx/htpasswd/<domain>
}

// Func is the processor signature. One function, one placeholder, one
// substitution. Templates call Funcs in registration order; later
// processors operate on output produced by earlier ones, so they can in
// theory compose (e.g. {{settings}} runs late, after the placeholders
// it might contain are stripped).
type Func func(in string, ctx *SiteContext) string

// Replace is the helper every processor uses to emit a substitution.
// strings.ReplaceAll guarantees idempotence — running a processor twice
// against an already-rendered template body is a no-op (the placeholder
// is gone after the first pass).
func Replace(in, placeholder, value string) string {
	return strings.ReplaceAll(in, placeholder, value)
}

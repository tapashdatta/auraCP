// {{force_https}} → emits a dedicated HTTP-only server block that redirects
// all plain-HTTP traffic to HTTPS, EXCEPT /.well-known/acme-challenge/ which
// is served directly so LE HTTP-01 renewals work even with force_https on.
//
// Emitted ONLY when BOTH conditions hold:
//   1. ctx.CertPath != ""       — a cert is actually issued. Redirecting
//                                 to HTTPS before the cert lands would
//                                 trap visitors in a TLS handshake failure.
//   2. ctx.ForceHttps == true   — operator has the toggle on in Settings.
//
// Why a separate server block instead of `if ($scheme = "http")`:
// nginx's `if` runs in the REWRITE phase, which fires BEFORE location
// matching. An `if ($scheme = "http") { return 301; }` at server scope
// therefore intercepts ACME challenge requests too — the
// `location ^~ /.well-known/acme-challenge/` block never gets a chance
// to run. The curl symptom is: GET /.well-known/acme-challenge/<token>
// → 301 → https://domain/... → LE validation fails with 404.
//
// A standalone `server { listen 80; }` block with a dedicated ACME
// location is the canonical nginx pattern for this:
//   location ^~ /.well-known/acme-challenge/ { serve file }
//   location /                               { return 301 https://... }
// Location matching runs in the CONTENT phase so the more-specific
// ACME prefix wins cleanly.
//
// The main server block (listen 80 + 443) stays in place so that
// plain-HTTP requests still work during the window between site creation
// and first cert issuance.
package processor

import "fmt"

const phForceHttps = "{{force_https}}"

func ForceHttps(in string, ctx *SiteContext) string {
	if ctx.CertPath == "" || !ctx.ForceHttps {
		return Replace(in, phForceHttps, "")
	}
	block := fmt.Sprintf(`
# Force HTTPS — separate server block so the ACME challenge location
# is matched in the content phase (before the redirect fires).
server {
    listen 80;
    listen [::]:80;
    server_name %s;

    # ACME HTTP-01 — must be reachable on :80 even with force_https on.
    location ^~ /.well-known/acme-challenge/ {
        root /var/lib/auracp/acme/;
        default_type "text/plain";
        try_files $uri =404;
    }

    location / {
        return 301 https://$host$request_uri;
    }
}`, ctx.Domain)
	return Replace(in, phForceHttps, block)
}

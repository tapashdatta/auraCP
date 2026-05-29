// {{force_https}} → an `if ($scheme = "http")` 301 redirect block that
// makes nginx force every plain-HTTP request to its HTTPS counterpart.
//
// Emitted ONLY when BOTH conditions hold:
//   1. ctx.CertPath != ""       — a cert is actually issued. Redirecting
//                                 to HTTPS before the cert lands would
//                                 trap visitors in a TLS handshake
//                                 failure loop.
//   2. ctx.ForceHttps == true   — operator has the toggle on in
//                                 Settings → General.
//
// When either condition is false the placeholder is replaced with the
// empty string, leaving the site serving both schemes (same vhost,
// `listen 80;` + `listen 443 ssl;`).
//
// Why `if ($scheme)` and not a separate `server { listen 80; return
// 301 ...; }` block: keeping a single server block per site is the
// design invariant of the v0.2.52 template system — one file, one
// vhost, one source of truth. The if-block sits next to the ACME
// challenge location, which itself short-circuits the redirect on the
// LE renewal path (location ^~ /.well-known/acme-challenge/ runs
// before any server-scope `if`).
package processor

const phForceHttps = "{{force_https}}"

const forceHttpsBlock = `if ($scheme = "http") {
        return 301 https://$host$request_uri;
    }`

func ForceHttps(in string, ctx *SiteContext) string {
	if ctx.CertPath == "" || !ctx.ForceHttps {
		return Replace(in, phForceHttps, "")
	}
	return Replace(in, phForceHttps, forceHttpsBlock)
}

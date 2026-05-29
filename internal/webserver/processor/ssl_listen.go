// {{ssl_listen}} → the three lines that wire nginx to terminate TLS on
// :443, emitted ONLY when ctx.CertPath != "":
//
//   listen 443 ssl;
//   listen [::]:443 ssl;
//   http2 on;
//
// v0.2.56: pre-this, the templates hardcoded these three lines
// unconditionally. nginx -t accepts the block (only a warning, not an
// error) but at TLS handshake time nginx falls back to the catch-all
// 00-default.conf (`ssl_reject_handshake on`) for SNIs whose vhost
// has no cert to present → operator sees "tls: unrecognized name".
//
// Gating the block on cert presence means:
//   - Pre-issuance: vhost only listens on :80. The lego ACME goroutine
//     uses /.well-known/acme-challenge/ on :80 (HTTP-01), succeeds,
//     writes the cert files.
//   - Post-issuance: RunReapply detects the cert files on disk,
//     re-renders the vhost with this block emitted, nginx reload picks
//     up the 443 block. Site is now HTTPS-enabled.
//
// This is the same shape the legacy renderer used (`{{- if .CertPath }}`
// gate) — restored as a processor in the v0.2.52 template system.
package processor

const phSslListen = "{{ssl_listen}}"

const sslListenBlock = `listen 443 ssl;
    listen [::]:443 ssl;
    http2 on;`

func SslListen(in string, ctx *SiteContext) string {
	if ctx.CertPath == "" {
		return Replace(in, phSslListen, "")
	}
	return Replace(in, phSslListen, sslListenBlock)
}

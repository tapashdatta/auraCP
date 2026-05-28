// {{ssl_certificate}} / {{ssl_certificate_key}} → "ssl_certificate <path>;" / "ssl_certificate_key <path>;"
//
// Both come out empty when ctx.CertPath/KeyPath are empty (pre-issuance) —
// the resulting vhost is HTTP-only with an ACME challenge location, and
// the renewal goroutine re-renders the vhost once lego writes the cert
// files. The vhost template's `listen 443 ssl;` line is wrapped in an
// `{{ssl_listen}}` block via Settings, OR we emit both placeholders
// with the same conditional gating from the Creator.
//
// Current design: emit literal `ssl_certificate …;` lines only when
// CertPath is set. Otherwise emit nothing (empty replacement), and the
// surrounding template's `listen 443 ssl;` is dropped by the same Creator-
// level conditional (see template/php.go::Build).
package processor

import "fmt"

const phSslCertificate = "{{ssl_certificate}}"
const phSslCertificateKey = "{{ssl_certificate_key}}"

func SslCertificate(in string, ctx *SiteContext) string {
	if ctx.CertPath == "" {
		return Replace(in, phSslCertificate, "")
	}
	value := fmt.Sprintf("ssl_certificate %s;", ctx.CertPath)
	return Replace(in, phSslCertificate, value)
}

func SslCertificateKey(in string, ctx *SiteContext) string {
	if ctx.KeyPath == "" {
		return Replace(in, phSslCertificateKey, "")
	}
	value := fmt.Sprintf("ssl_certificate_key %s;", ctx.KeyPath)
	return Replace(in, phSslCertificateKey, value)
}

// PHP + WordPress templates. Both use the same processor chain — the
// only difference is the template body (wordpress.tmpl has WP-specific
// `wp-admin`, `xmlrpc.php`, and multisite rewrite blocks; php.tmpl is
// the bare Generic template).
//
// Processor order matters: Settings runs LAST so an operator who pastes
// a custom block referencing `{{server_name}}` or `{{root}}` still
// gets those substituted. Cert processors are in the chain even when
// no cert is issued yet — they return an empty string in that case
// (see processor/ssl_certificate.go) and the surrounding `listen 443
// ssl;` line is gated by the same `if .CertPath` template branch.
package template

import "github.com/auracp/auracp/internal/webserver/processor"

func Php() *Template {
	return &Template{
		Type: "php",
		Processors: []processor.Func{
			processor.BotMap, // before server{} block
			processor.ServerName,
			processor.Root,
			processor.SslListen, // v0.2.56: conditional `listen 443 ssl;` block
			processor.SslCertificate,
			processor.SslCertificateKey,
			processor.NginxAccessLog,
			processor.NginxErrorLog,
			processor.BotCheck,    // inside server{} block
			processor.BasicAuth,   // inside server{} block
			processor.CacheSkip,   // inside server{} block (drives bypass var)
			processor.PhpFpmSocket,
			processor.FastcgiCache, // inside location ~ \.php$
			processor.Settings,     // last — see file header
		},
	}
}

// WordPress shares the PHP processor chain (same placeholder set); the
// template body itself carries the WP-specific location blocks.
func WordPress() *Template {
	t := Php()
	t.Type = "wordpress"
	return t
}

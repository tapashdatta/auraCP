// {{http_listen}} → emits `listen 80; listen [::]:80;` in the main server
// block ONLY when force_https is off.
//
// When force_https is on, a separate dedicated server block (emitted by
// {{force_https}}) owns port 80 exclusively — the ACME challenge location
// and the redirect live there. The main server block then only needs to
// listen on 443 ({{ssl_listen}}), avoiding a duplicate-listen conflict.
//
// When force_https is off (or no cert yet), the main server block handles
// both HTTP and HTTPS from the same block, so listen 80 belongs here.
package processor

const phHttpListen = "{{http_listen}}"

func HttpListen(in string, ctx *SiteContext) string {
	if ctx.CertPath != "" && ctx.ForceHttps {
		return Replace(in, phHttpListen, "")
	}
	return Replace(in, phHttpListen, "listen 80;\n    listen [::]:80;")
}

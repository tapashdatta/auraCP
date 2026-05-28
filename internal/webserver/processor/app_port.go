// {{app_port}} → the loopback port the Node/Python backend listens on.
//
// Emitted bare (e.g. "9012", no surrounding nginx directive) — the
// surrounding template provides the proxy_pass / uwsgi_pass scaffold,
// like:
//     proxy_pass http://127.0.0.1:{{app_port}}/;
package processor

import "strconv"

const phAppPort = "{{app_port}}"

func AppPort(in string, ctx *SiteContext) string {
	if ctx.AppPort <= 0 {
		return Replace(in, phAppPort, "")
	}
	return Replace(in, phAppPort, strconv.Itoa(ctx.AppPort))
}

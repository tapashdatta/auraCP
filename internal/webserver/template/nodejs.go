// Node.js and Python sites — same shape: nginx proxies to a loopback
// port the per-site systemd unit listens on. Different processor chain
// from PHP (no FPM socket; uses {{app_port}} instead).
package template

import "github.com/auracp/auracp/internal/webserver/processor"

func Nodejs() *Template {
	return &Template{
		Type: "nodejs",
		Processors: []processor.Func{
			processor.BotMap,
			processor.ServerName,
			processor.Root,
			processor.HttpListen, // omit listen 80 from main block when force_https owns :80
			processor.SslListen,
			processor.ForceHttps, // dedicated redirect server block, ACME-safe
			processor.SslCertificate,
			processor.SslCertificateKey,
			processor.NginxAccessLog,
			processor.NginxErrorLog,
			processor.BotCheck,
			processor.BasicAuth,
			processor.CacheSkip,
			processor.AppPort,
			processor.ProxyCache, // inside location / { proxy_pass ... }
			processor.Settings,
		},
	}
}

func Python() *Template {
	t := Nodejs() // Python shares the Node processor chain — same place-holders
	t.Type = "python"
	return t
}

// Static HTML and Reverse Proxy templates.
//
// Static: nginx serves the docroot directly. No backend.
// ReverseProxy: nginx proxies every request to a user-supplied URL.
//   No docroot (well, an empty one for the ACME challenge path).
package template

import "github.com/auracp/auracp/internal/webserver/processor"

func Static() *Template {
	return &Template{
		Type: "static",
		Processors: []processor.Func{
			processor.BotMap,
			processor.ServerName,
			processor.Root,
			processor.SslCertificate,
			processor.SslCertificateKey,
			processor.NginxAccessLog,
			processor.NginxErrorLog,
			processor.BotCheck,
			processor.BasicAuth,
			// Static doesn't get cache_skip / proxy_cache — nginx serves
			// the files directly with `expires max` on assets; no PHP/FPM
			// upstream to cache.
			processor.Settings,
		},
	}
}

func ReverseProxy() *Template {
	return &Template{
		Type: "reverseproxy",
		Processors: []processor.Func{
			processor.BotMap,
			processor.ServerName,
			processor.SslCertificate,
			processor.SslCertificateKey,
			processor.NginxAccessLog,
			processor.NginxErrorLog,
			processor.BotCheck,
			processor.BasicAuth,
			processor.CacheSkip,
			processor.ReverseProxyURL,
			processor.ProxyCache, // inside location / { proxy_pass ... }
			processor.Settings,
		},
	}
}

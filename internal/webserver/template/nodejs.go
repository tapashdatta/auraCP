// Node.js and Python sites — same shape: nginx proxies to a loopback
// port the per-site systemd unit listens on. Different processor chain
// from PHP (no FPM socket; uses {{app_port}} instead).
package template

import "github.com/auracp/auracp/internal/webserver/processor"

func Nodejs() *Template {
	return &Template{
		Type: "nodejs",
		Processors: []processor.Func{
			processor.ServerName,
			processor.Root,
			processor.SslCertificate,
			processor.SslCertificateKey,
			processor.NginxAccessLog,
			processor.NginxErrorLog,
			processor.AppPort,
			processor.Settings,
		},
	}
}

func Python() *Template {
	t := Nodejs()
	t.Type = "python"
	return t
}

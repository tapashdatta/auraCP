// Package paths centralises the on-disk layout for hosted sites.
package paths

import (
	"path/filepath"
	"strconv"
)

const (
	HomeBase = "/home"
	// nginx vhost layout (Debian/Ubuntu convention). v0.2.0 dropped /etc/caddy/sites.
	NginxSitesAvailable = "/etc/nginx/sites-available"
	NginxSitesEnabled   = "/etc/nginx/sites-enabled"
	// PHP-FPM sockets land here (created by /etc/tmpfiles.d/auracp.conf).
	PHPSocketsDir = "/run/php-fpm"
	// Per-domain LE certs issued by auracpd's in-process lego.
	SSLDir = "/etc/auracp/ssl"
	// ACME HTTP-01 challenge tokens are written here and served by nginx.
	ACMEChallengeDir = "/var/lib/auracp/acme"
	// Per-site htpasswd files for basic auth — nginx auth_basic_user_file points here.
	HTPasswdDir = "/etc/auracp/htpasswd"
	SystemdDir  = "/etc/systemd/system"
	SFTPGroup   = "auracp-sftp"
)

// HTPasswdFile returns the per-site htpasswd path that the nginx template
// renders into the `auth_basic_user_file` directive.
func HTPasswdFile(domain string) string { return filepath.Join(HTPasswdDir, domain) }

// CatchAllNginxFile / CatchAllNginxLink — v0.2.38. Default-server vhost
// that catches requests for unmatched server_names. Lives at "00-default"
// so it sorts before everything else; nginx honours `default_server`
// regardless of order, but the early load makes the intent obvious in
// `nginx -T` output. See internal/webserver/template.go::catchAllTemplate.
func CatchAllNginxFile() string { return filepath.Join(NginxSitesAvailable, "00-default.conf") }
func CatchAllNginxLink() string { return filepath.Join(NginxSitesEnabled, "00-default.conf") }

// PanelNginxFile fronts the control panel itself behind nginx (LE cert on :443).
// The "00-" prefix keeps it ordering-stable among site vhosts.
func PanelNginxFile() string { return filepath.Join(NginxSitesAvailable, "00-panel.conf") }
func PanelNginxLink() string { return filepath.Join(NginxSitesEnabled, "00-panel.conf") }

func SiteHome(user string) string        { return filepath.Join(HomeBase, user) }
func DocRoot(user, domain string) string { return filepath.Join(HomeBase, user, "htdocs", domain) }
func LogDir(user string) string          { return filepath.Join(HomeBase, user, "logs") }

// nginx site config — write to sites-available, symlink into sites-enabled.
func NginxSiteFile(domain string) string { return filepath.Join(NginxSitesAvailable, domain+".conf") }
func NginxSiteLink(domain string) string { return filepath.Join(NginxSitesEnabled, domain+".conf") }

// PHP-FPM pool config for a site, under the chosen PHP version's pool.d directory.
func PHPPoolFile(phpVer, domain string) string {
	return filepath.Join("/etc/php", phpVer, "fpm/pool.d", domain+".conf")
}
func PHPSocket(domain string) string { return filepath.Join(PHPSocketsDir, domain+".sock") }

// Per-domain cert + key paths written by the ACME manager and consumed by nginx.
func CertPath(domain string) string { return filepath.Join(SSLDir, domain+".crt") }
func KeyPath(domain string) string  { return filepath.Join(SSLDir, domain+".key") }

func UnitName(domain string) string  { return "auracp-site-" + domain }
func UnitFile(domain string) string  { return filepath.Join(SystemdDir, UnitName(domain)+".service") }
func Upstream(port int) string       { return "127.0.0.1:" + strconv.Itoa(port) }

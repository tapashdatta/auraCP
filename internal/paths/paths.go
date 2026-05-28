// Package paths centralises the on-disk layout for hosted sites.
package paths

import (
	"path/filepath"
	"strconv"
)

const (
	HomeBase      = "/home"
	CaddySitesDir = "/etc/caddy/sites"
	SystemdDir    = "/etc/systemd/system"
	SFTPGroup     = "auracp-sftp"
)

// PanelCaddyFile fronts the control panel itself behind Caddy (real LE cert on
// :443). The "00-" prefix keeps it ordering-stable among site vhosts.
func PanelCaddyFile() string { return filepath.Join(CaddySitesDir, "00-panel.caddy") }

func SiteHome(user string) string        { return filepath.Join(HomeBase, user) }
func DocRoot(user, domain string) string { return filepath.Join(HomeBase, user, "htdocs", domain) }
func LogDir(user string) string          { return filepath.Join(HomeBase, user, "logs") }
func CaddyFile(domain string) string     { return filepath.Join(CaddySitesDir, domain+".caddy") }
func UnitName(domain string) string      { return "auracp-site-" + domain }
func UnitFile(domain string) string      { return filepath.Join(SystemdDir, UnitName(domain)+".service") }
func Upstream(port int) string           { return "127.0.0.1:" + strconv.Itoa(port) }

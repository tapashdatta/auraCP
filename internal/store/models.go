package store

import "database/sql"

// Site is the stored representation of a hosted site.
type Site struct {
	ID          int64          `json:"-"`
	Type        string         `json:"type"`
	Domain      string         `json:"domain"`
	SiteUser    string         `json:"user"`
	RootPath    string         `json:"root"`
	App         string         `json:"app"`
	NodeVersion sql.NullString `json:"-"`
	Port        int            `json:"-"`
	Upstream    string         `json:"-"`
	PHPVersion  string         `json:"-"`
	Status      string         `json:"status"`
	StatusText  string         `json:"statusText"`
}

// badge/icon are presentation, derived from type so the API stays declarative.
var typeBadge = map[string]string{
	"wordpress": "b-wp", "php": "b-php", "nodejs": "b-node",
	"python": "b-py", "static": "b-static", "reverseproxy": "b-proxy",
}
var typeIcon = map[string]string{
	"wordpress": "◆", "php": "php", "nodejs": "▲",
	"python": "py", "static": "▤", "reverseproxy": "⇄",
}

// SiteView is the JSON shape the UI consumes (matches web/src/lib/data.js).
type SiteView struct {
	Domain     string  `json:"domain"`
	Root       string  `json:"root"`
	User       string  `json:"user"`
	App        string  `json:"app"`
	Badge      string  `json:"badge"`
	Ic         string  `json:"ic"`
	Node       *string `json:"node"`
	Status     string  `json:"status"`
	StatusText string  `json:"statusText"`
}

func (s Site) View() SiteView {
	var node *string
	if s.NodeVersion.Valid && s.NodeVersion.String != "" {
		node = &s.NodeVersion.String
	}
	return SiteView{
		Domain: s.Domain, Root: s.RootPath, User: s.SiteUser, App: s.App,
		Badge: typeBadge[s.Type], Ic: typeIcon[s.Type], Node: node,
		Status: s.Status, StatusText: s.StatusText,
	}
}

// DatabaseServer is a local DB engine auraCP can provision databases on.
type DatabaseServer struct {
	ID        int64  `json:"-"`
	Engine    string `json:"engine"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Version   string `json:"version"`
	IsDefault bool   `json:"isDefault"`
}

// Package site orchestrates the full lifecycle of a hosted site: the Linux
// user, the backend service, the Caddy vhost, and the stored record. It is the
// single entry point the API/CLI call; the per-step work lives in osuser,
// runtime, and webserver. Every step validates input before touching the system.
package site

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/auracp/auracp/internal/osuser"
	"github.com/auracp/auracp/internal/paths"
	"github.com/auracp/auracp/internal/runtime"
	"github.com/auracp/auracp/internal/store"
	"github.com/auracp/auracp/internal/system"
	"github.com/auracp/auracp/internal/validate"
	"github.com/auracp/auracp/internal/webserver"
)

type Manager struct {
	r     *system.Runner
	store *store.Store
	os    *osuser.Manager
	web   *webserver.Manager
	rt    *runtime.Manager
}

func New(r *system.Runner, st *store.Store) *Manager {
	return &Manager{
		r: r, store: st,
		os:  osuser.New(r),
		web: webserver.New(r),
		rt:  runtime.New(r),
	}
}

// Spec is the create request (validated by Create).
type Spec struct {
	Type      string
	Domain    string
	User      string
	Password  string
	PHPVer    string // php/wordpress
	StartFile string // nodejs
	Module    string // python
	Upstream  string // reverseproxy (user-supplied URL)
	NodeReady bool   // tag node availability on the record
}

func hasBackend(t string) bool {
	switch t {
	case "php", "wordpress", "nodejs", "python":
		return true
	}
	return false
}

// Create provisions a new site end-to-end and records it. On a failure mid-way
// it rolls back the resources it already created.
func (m *Manager) Create(ctx context.Context, s Spec) (store.Site, error) {
	if err := validate.SiteType(s.Type); err != nil {
		return store.Site{}, err
	}
	if err := validate.Domain(s.Domain); err != nil {
		return store.Site{}, err
	}
	if err := validate.Username(s.User); err != nil {
		return store.Site{}, err
	}

	// resolve backend port / upstream
	var port int
	var upstream string
	switch {
	case s.Type == "reverseproxy":
		if s.Upstream == "" {
			return store.Site{}, fmt.Errorf("reverse proxy requires an upstream URL")
		}
		upstream = s.Upstream
	case hasBackend(s.Type):
		if s.Type == "php" || s.Type == "wordpress" {
			if err := validate.PHPVersion(s.PHPVer); err != nil {
				return store.Site{}, err
			}
		}
		p, err := m.store.NextPort()
		if err != nil {
			return store.Site{}, err
		}
		port = p
		upstream = paths.Upstream(port)
	}

	// 1) Linux user + dirs + SFTP jail
	if err := m.os.Create(ctx, s.User, s.Domain); err != nil {
		return store.Site{}, err
	}
	rollback := func() { _ = m.os.Delete(ctx, s.User) }
	if s.Password != "" {
		_ = m.os.SetPassword(ctx, s.User, s.Password)
	}

	// 2) backend service (php/node/python); static & proxy have none
	if hasBackend(s.Type) {
		if err := m.rt.Apply(ctx, runtime.Spec{
			Type: s.Type, Domain: s.Domain, User: s.User, Port: port,
			StartFile: s.StartFile, Module: s.Module, PHPVer: s.PHPVer,
		}); err != nil {
			rollback()
			return store.Site{}, err
		}
	}

	// 3) front Caddy vhost + reload (auto-HTTPS kicks in here)
	if err := m.web.Apply(ctx, webserver.Spec{
		Type: s.Type, Domain: s.Domain, User: s.User, Upstream: upstream,
	}); err != nil {
		if hasBackend(s.Type) {
			_ = m.rt.Remove(ctx, s.Domain)
		}
		rollback()
		return store.Site{}, err
	}

	// 4) record desired state
	rec := store.Site{
		Type: s.Type, Domain: s.Domain, SiteUser: s.User,
		RootPath: paths.DocRoot(s.User, s.Domain), App: appLabel(s),
		Port: port, Upstream: upstream, PHPVersion: s.PHPVer,
		Status: "up", StatusText: "Online",
	}
	if s.NodeReady {
		rec.NodeVersion = sql.NullString{String: "24", Valid: true}
	}
	if err := m.store.CreateSite(rec); err != nil {
		return store.Site{}, err
	}
	return rec, nil
}

// Delete tears a site down: vhost, backend, user, and record.
func (m *Manager) Delete(ctx context.Context, domain string) error {
	st, err := m.store.SiteByDomain(domain)
	if err != nil {
		return err
	}
	if err := m.web.Remove(ctx, domain); err != nil {
		return err
	}
	if hasBackend(st.Type) {
		if err := m.rt.Remove(ctx, domain); err != nil {
			return err
		}
	}
	if err := m.os.Delete(ctx, st.SiteUser); err != nil {
		return err
	}
	return m.store.DeleteSite(domain)
}

func appLabel(s Spec) string {
	switch s.Type {
	case "wordpress":
		return "WordPress"
	case "php":
		return "PHP " + s.PHPVer
	case "nodejs":
		return "Node.js"
	case "python":
		return "Python 3"
	case "static":
		return "Static"
	case "reverseproxy":
		return "Reverse Proxy"
	}
	return s.Type
}

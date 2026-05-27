// Package perm is auraCP's granular permission model: each panel user has a set
// of per-resource CRUD capabilities. ROLE_ADMIN implicitly has everything.
package perm

import "encoding/json"

// Resources that can be permissioned.
var Resources = []string{
	"sites", "databases", "backups", "cron", "files", "ssh_users", "users", "settings",
}

type CRUD struct {
	Create bool `json:"create"`
	Read   bool `json:"read"`
	Update bool `json:"update"`
	Delete bool `json:"delete"`
}

func all() CRUD  { return CRUD{true, true, true, true} }
func read() CRUD { return CRUD{false, true, false, false} }
func none() CRUD { return CRUD{} }

// Set maps a resource name to its CRUD capabilities.
type Set map[string]CRUD

// Can reports whether the set permits action ("create"|"read"|"update"|"delete")
// on resource.
func (s Set) Can(resource, action string) bool {
	c, ok := s[resource]
	if !ok {
		return false
	}
	switch action {
	case "create":
		return c.Create
	case "read":
		return c.Read
	case "update":
		return c.Update
	case "delete":
		return c.Delete
	}
	return false
}

// DefaultForRole returns sensible default capabilities for a role.
func DefaultForRole(role string) Set {
	switch role {
	case "ROLE_ADMIN":
		s := Set{}
		for _, r := range Resources {
			s[r] = all()
		}
		return s
	case "ROLE_SITE_MANAGER":
		return Set{
			"sites": all(), "databases": all(), "backups": all(),
			"cron": all(), "files": all(), "ssh_users": all(),
			"users": none(), "settings": read(),
		}
	default: // ROLE_USER
		return Set{
			"sites": read(), "databases": read(), "backups": read(),
			"cron": read(), "files": read(), "ssh_users": none(),
			"users": none(), "settings": none(),
		}
	}
}

// Marshal serialises a set to JSON for storage.
func Marshal(s Set) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// Parse loads a set from JSON; on error/empty it falls back to the role default.
func Parse(jsonStr, role string) Set {
	if jsonStr == "" {
		return DefaultForRole(role)
	}
	var s Set
	if err := json.Unmarshal([]byte(jsonStr), &s); err != nil || s == nil {
		return DefaultForRole(role)
	}
	return s
}

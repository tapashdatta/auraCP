// Package perm is auraCP's granular permission model: each panel user has a set
// of per-resource CRUD capabilities. ROLE_ADMIN implicitly has everything.
package perm

import (
	"encoding/json"
	"sync"
)

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

// v0.2.21: in-memory role default overrides, populated at daemon startup
// from the settings table (key: role_perm_<role>). Admin can edit role
// defaults via the Roles & Permissions tab; persistence + this map keep
// them in sync. ROLE_ADMIN is NOT overridable — full access is a property
// of the role, not a configurable matrix.
var (
	overridesMu sync.RWMutex
	overrides   = map[string]Set{}
)

func SetOverride(role string, s Set) {
	if role == "ROLE_ADMIN" {
		return // ignored; admins always have all
	}
	overridesMu.Lock()
	defer overridesMu.Unlock()
	overrides[role] = s
}

func ClearOverride(role string) {
	overridesMu.Lock()
	defer overridesMu.Unlock()
	delete(overrides, role)
}

func HasOverride(role string) bool {
	overridesMu.RLock()
	defer overridesMu.RUnlock()
	_, ok := overrides[role]
	return ok
}

// CompiledDefault returns the hard-coded default — bypasses any runtime
// override. Useful for the UI's "reset to factory" button.
func CompiledDefault(role string) Set {
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

// DefaultForRole returns the effective default capabilities for a role —
// admin-customised override if present, compiled-in default otherwise.
func DefaultForRole(role string) Set {
	if role == "ROLE_ADMIN" {
		return CompiledDefault(role)
	}
	overridesMu.RLock()
	if s, ok := overrides[role]; ok {
		overridesMu.RUnlock()
		// Return a defensive copy so callers can't mutate the override.
		out := Set{}
		for k, v := range s {
			out[k] = v
		}
		return out
	}
	overridesMu.RUnlock()
	return CompiledDefault(role)
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

// AllowedSites parses a JSON-array site scope. Empty string = "all sites"
// (back-compat default for pre-v0.2.15 users); a JSON array of domains
// restricts the user to that set; "*" inside the array also means "all".
// Returns (set, allFlag): when allFlag is true, ignore set. v0.2.15+.
func AllowedSites(sitesScopeJSON string) (set map[string]bool, all bool) {
	if sitesScopeJSON == "" {
		return nil, true
	}
	var domains []string
	if err := json.Unmarshal([]byte(sitesScopeJSON), &domains); err != nil {
		// Malformed JSON — fail open (admin can fix it), don't lock the user out.
		return nil, true
	}
	if len(domains) == 0 {
		// Explicit empty array means "no sites" — operator's choice; honour it.
		return map[string]bool{}, false
	}
	out := make(map[string]bool, len(domains))
	for _, d := range domains {
		if d == "*" {
			return nil, true
		}
		out[d] = true
	}
	return out, false
}

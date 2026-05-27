// Package validate guards all externally-supplied identifiers before they reach
// the filesystem, a shell-free exec, or a config template. Reject-by-default.
package validate

import (
	"fmt"
	"regexp"
	"strconv"
)

var (
	// RFC-1123-ish hostname, 1–253 chars, labels of [a-z0-9-].
	domainRe = regexp.MustCompile(`^(?i)([a-z0-9](-?[a-z0-9])*)(\.[a-z0-9](-?[a-z0-9])*)+$`)
	// Linux username: start with a letter, then lowercase/digits/-/_, max 32.
	userRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	// DB / db-user identifier.
	identRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,63}$`)
)

func Domain(d string) error {
	if len(d) > 253 || !domainRe.MatchString(d) {
		return fmt.Errorf("invalid domain name: %q", d)
	}
	return nil
}

func Username(u string) error {
	if !userRe.MatchString(u) {
		return fmt.Errorf("invalid site user: %q (use a-z, 0-9, -, _; start with a letter)", u)
	}
	return nil
}

func Identifier(s string) error {
	if !identRe.MatchString(s) {
		return fmt.Errorf("invalid identifier: %q", s)
	}
	return nil
}

func Port(p int) error {
	if p < 1 || p > 65535 {
		return fmt.Errorf("invalid port: %d", p)
	}
	return nil
}

// SiteType returns nil for the supported types.
func SiteType(t string) error {
	switch t {
	case "wordpress", "php", "nodejs", "python", "static", "reverseproxy":
		return nil
	}
	return fmt.Errorf("unknown site type: %q", t)
}

// PHPVersion enforces the 8.3+ minimal-package policy.
func PHPVersion(v string) error {
	switch v {
	case "8.3", "8.4", "8.5":
		return nil
	}
	return fmt.Errorf("unsupported PHP version: %q (8.3+ only)", v)
}

// ParsePort parses and validates a port from a string.
func ParsePort(s string) (int, error) {
	p, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("port not a number: %q", s)
	}
	return p, Port(p)
}

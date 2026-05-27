// Package files is a path-safe file browser scoped to a site's document root.
// Listing reads as root; the path is strictly contained within the docroot so
// a crafted "sub" can never escape the site (no .. traversal, no symlink-out).
package files

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/auracp/auracp/internal/paths"
	"github.com/auracp/auracp/internal/validate"
)

type Entry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size"`
	Mode string `json:"mode"`
}

// List returns the entries of <docroot>/<sub>, rejecting any path that resolves
// outside the document root.
func List(user, domain, sub string) ([]Entry, error) {
	if err := validate.Username(user); err != nil {
		return nil, err
	}
	if err := validate.Domain(domain); err != nil {
		return nil, err
	}
	root := paths.DocRoot(user, domain)
	target := filepath.Clean(filepath.Join(root, sub))
	// Containment check: target must be root or strictly under it.
	if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
		return nil, fmt.Errorf("path escapes site root")
	}
	// Resolve symlinks and re-check, so a symlink inside can't point out.
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		if resolved != root && !strings.HasPrefix(resolved, root+string(os.PathSeparator)) {
			return nil, fmt.Errorf("path escapes site root")
		}
	}

	des, err := os.ReadDir(target)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(des))
	for _, de := range des {
		fi, err := de.Info()
		if err != nil {
			continue
		}
		out = append(out, Entry{
			Name: de.Name(), Dir: de.IsDir(),
			Size: fi.Size(), Mode: fi.Mode().String(),
		})
	}
	return out, nil
}

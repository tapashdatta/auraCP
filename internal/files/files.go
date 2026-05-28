// Package files is a path-safe file browser scoped to a site's document root.
// Listing reads as root; the path is strictly contained within the docroot so
// a crafted "sub" can never escape the site (no .. traversal, no symlink-out).
// Uploads and downloads chown the file to the site user so site code can read
// them without elevation and the panel never serves files outside the site.
package files

import (
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
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

// resolve returns the absolute, contained target path, refusing any input that
// would escape the docroot. Caller owns the validate.Username/.Domain checks.
func resolve(user, domain, sub string) (string, string, error) {
	if err := validate.Username(user); err != nil {
		return "", "", err
	}
	if err := validate.Domain(domain); err != nil {
		return "", "", err
	}
	root := paths.DocRoot(user, domain)
	target := filepath.Clean(filepath.Join(root, sub))
	if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
		return "", "", fmt.Errorf("path escapes site root")
	}
	return root, target, nil
}

// Save streams an uploaded file into <docroot>/<sub>/<name>, chowning it to
// the site user so the application code reads it as itself. Overwrites
// existing files (operator already had to click Upload).
func Save(siteUser, domain, sub, name string, body io.Reader) error {
	// Sanitise the filename: only the basename, no traversal markers.
	clean := filepath.Base(filepath.Clean(name))
	if clean == "" || clean == "." || clean == ".." || clean == "/" {
		return fmt.Errorf("invalid filename: %q", name)
	}
	_, dir, err := resolve(siteUser, domain, sub)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	target := filepath.Join(dir, clean)
	// Re-check containment AFTER joining filename in case clean snuck in tricks.
	if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dir)+string(os.PathSeparator)) {
		return fmt.Errorf("invalid target path")
	}
	dst, err := os.Create(target)
	if err != nil {
		return err
	}
	defer dst.Close()
	if _, err := io.Copy(dst, body); err != nil {
		_ = os.Remove(target)
		return err
	}
	return chownToUser(target, siteUser)
}

// Open returns a *os.File for download. Caller is responsible for closing it
// and copying the bytes to the HTTP response.
func Open(siteUser, domain, sub string) (*os.File, os.FileInfo, error) {
	_, target, err := resolve(siteUser, domain, sub)
	if err != nil {
		return nil, nil, err
	}
	f, err := os.Open(target)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if fi.IsDir() {
		f.Close()
		return nil, nil, fmt.Errorf("path is a directory")
	}
	return f, fi, nil
}

// Delete removes a file (or empty directory). Refuses to nuke the docroot
// itself; that's a site-delete operation, not a file-manager one.
func Delete(siteUser, domain, sub string) error {
	root, target, err := resolve(siteUser, domain, sub)
	if err != nil {
		return err
	}
	if target == root {
		return fmt.Errorf("refusing to delete the document root itself")
	}
	return os.RemoveAll(target)
}

// chownToUser sets the file's ownership to the site user. We're running as
// root in auracpd; the panel never serves files via Linux ACLs but per-app
// runtimes need to read them as the site user.
func chownToUser(path, siteUser string) error {
	u, err := user.Lookup(siteUser)
	if err != nil {
		return nil // best-effort: lookup may fail in dev environments
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return os.Chown(path, uid, gid)
}

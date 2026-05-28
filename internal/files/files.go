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

// Rename moves <sub> to a sibling with newName (basename only). New target
// must stay inside the docroot. Refuses overwrites — caller must Delete first.
func Rename(siteUser, domain, sub, newName string) error {
	clean := filepath.Base(filepath.Clean(newName))
	if clean == "" || clean == "." || clean == ".." || clean == "/" || strings.ContainsAny(clean, "/\\") {
		return fmt.Errorf("invalid name: %q", newName)
	}
	root, target, err := resolve(siteUser, domain, sub)
	if err != nil {
		return err
	}
	if target == root {
		return fmt.Errorf("refusing to rename the document root")
	}
	dst := filepath.Join(filepath.Dir(target), clean)
	if !strings.HasPrefix(filepath.Clean(dst), root) {
		return fmt.Errorf("invalid target path")
	}
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("destination already exists: %s", clean)
	}
	return os.Rename(target, dst)
}

// Mkdir creates a new directory at <sub>/<name>, chowned to the site user so
// the application can write into it.
func Mkdir(siteUser, domain, sub, name string) error {
	clean := filepath.Base(filepath.Clean(name))
	if clean == "" || clean == "." || clean == ".." || clean == "/" || strings.ContainsAny(clean, "/\\") {
		return fmt.Errorf("invalid folder name: %q", name)
	}
	_, dir, err := resolve(siteUser, domain, sub)
	if err != nil {
		return err
	}
	target := filepath.Join(dir, clean)
	if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dir)+string(os.PathSeparator)) {
		return fmt.Errorf("invalid target path")
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		return err
	}
	return chownToUser(target, siteUser)
}

// Touch creates an empty file at <sub>/<name>. Refuses to overwrite an
// existing file (caller can use WriteText for that).
func Touch(siteUser, domain, sub, name string) error {
	clean := filepath.Base(filepath.Clean(name))
	if clean == "" || clean == "." || clean == ".." || clean == "/" || strings.ContainsAny(clean, "/\\") {
		return fmt.Errorf("invalid filename: %q", name)
	}
	_, dir, err := resolve(siteUser, domain, sub)
	if err != nil {
		return err
	}
	target := filepath.Join(dir, clean)
	if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dir)+string(os.PathSeparator)) {
		return fmt.Errorf("invalid target path")
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	f.Close()
	return chownToUser(target, siteUser)
}

// EditMaxBytes is the upper bound for files openable in the in-browser editor.
// Anything larger is rejected and the operator is told to use SFTP. 1 MiB
// covers ~30k lines of typical source — enough for any config or template.
const EditMaxBytes int64 = 1 << 20

// ReadText opens <sub> as UTF-8-ish text. Refuses files > EditMaxBytes and
// files that contain a NUL byte in the first 8 KiB (heuristic for binary).
func ReadText(siteUser, domain, sub string) (string, error) {
	_, target, err := resolve(siteUser, domain, sub)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(target)
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return "", fmt.Errorf("path is a directory")
	}
	if fi.Size() > EditMaxBytes {
		return "", fmt.Errorf("file is %d bytes; editor only opens files ≤ %d bytes (use SFTP for larger)", fi.Size(), EditMaxBytes)
	}
	b, err := os.ReadFile(target)
	if err != nil {
		return "", err
	}
	// Cheap binary sniff: a NUL in the first 8 KiB is overwhelmingly binary.
	sniff := b
	if len(sniff) > 8192 {
		sniff = sniff[:8192]
	}
	for _, c := range sniff {
		if c == 0 {
			return "", fmt.Errorf("file appears to be binary; refusing to open in text editor")
		}
	}
	return string(b), nil
}

// WriteText overwrites <sub> with content, preserving the original mode if it
// existed and chowning to the site user. Refuses content > EditMaxBytes so
// the editor can't be used to dump arbitrary blobs.
func WriteText(siteUser, domain, sub, content string) error {
	if int64(len(content)) > EditMaxBytes {
		return fmt.Errorf("content too large (%d bytes); editor limit is %d", len(content), EditMaxBytes)
	}
	root, target, err := resolve(siteUser, domain, sub)
	if err != nil {
		return err
	}
	if target == root {
		return fmt.Errorf("refusing to overwrite the document root")
	}
	// Preserve mode if file exists, else 0644.
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(target); err == nil {
		if fi.IsDir() {
			return fmt.Errorf("path is a directory")
		}
		mode = fi.Mode().Perm()
	}
	// Write to a temp file in the same directory, then rename. Avoids leaving
	// a half-written file on disk if the write is interrupted.
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".auracp-edit-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return chownToUser(target, siteUser)
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

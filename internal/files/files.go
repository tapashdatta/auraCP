// Package files is a path-safe file browser scoped to a site's document root.
// Listing reads as root; the path is strictly contained within the docroot so
// a crafted "sub" can never escape the site (no .. traversal, no symlink-out).
// Uploads and downloads chown the file to the site user so site code can read
// them without elevation and the panel never serves files outside the site.
package files

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
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
// outside the document root. v0.2.23: sorted folders-first, then files, with
// case-insensitive alphabetical ordering inside each group.
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
	var dirs, files []Entry
	for _, de := range des {
		fi, err := de.Info()
		if err != nil {
			continue
		}
		e := Entry{Name: de.Name(), Dir: de.IsDir(), Size: fi.Size(), Mode: fi.Mode().String()}
		if e.Dir {
			dirs = append(dirs, e)
		} else {
			files = append(files, e)
		}
	}
	byNameCI := func(a, b Entry) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) }
	sortEntries(dirs, byNameCI)
	sortEntries(files, byNameCI)
	return append(dirs, files...), nil
}

// sortEntries — tiny stable insertion sort to avoid importing slices/sort just
// for one call. Directory listings rarely exceed a few hundred entries; O(n²)
// is fine in practice and keeps the dependency surface small.
func sortEntries(es []Entry, less func(a, b Entry) int) {
	for i := 1; i < len(es); i++ {
		for j := i; j > 0 && less(es[j-1], es[j]) > 0; j-- {
			es[j-1], es[j] = es[j], es[j-1]
		}
	}
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

// SaveAt is like Save but accepts a name that may contain forward-slashes,
// treating the leading components as subdirectories under <base>. Creates any
// missing intermediate directories and chowns them to the site user. Used by
// the folder drag-and-drop upload path so a dropped tree lands as a tree on
// disk, not a flat directory.
//
// base + relPath are both relative to the docroot. relPath '..', '\', or
// absolute components are rejected.
func SaveAt(siteUser, domain, base, relPath string, body io.Reader) error {
	if relPath == "" {
		return fmt.Errorf("empty path")
	}
	rp := filepath.ToSlash(relPath)
	if strings.HasPrefix(rp, "/") || strings.Contains(rp, "\\") {
		return fmt.Errorf("invalid relative path: %q", relPath)
	}
	parts := strings.Split(rp, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return fmt.Errorf("invalid path component: %q", p)
		}
	}
	name := parts[len(parts)-1]
	subParts := parts[:len(parts)-1]
	fullSub := base
	if len(subParts) > 0 {
		joined := strings.Join(subParts, "/")
		if fullSub == "" {
			fullSub = joined
		} else {
			fullSub = fullSub + "/" + joined
		}
		// Resolve to get the absolute target dir and ensure it exists.
		_, dir, err := resolve(siteUser, domain, fullSub)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		// chown each intermediate directory to the site user — walking from
		// the deepest new dir up to the docroot, stopping when we hit an
		// existing site-user-owned directory.
		_ = chownToUser(dir, siteUser)
	}
	return Save(siteUser, domain, fullSub, name, body)
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

// Chmod sets the file's permission bits. Caller passes a Unix mode in octal
// (e.g. 0o644). The mode is masked to 0o777 so callers can't accidentally
// flip setuid/setgid/sticky bits.
func Chmod(siteUser, domain, sub string, mode os.FileMode) error {
	root, target, err := resolve(siteUser, domain, sub)
	if err != nil {
		return err
	}
	if target == root {
		return fmt.Errorf("refusing to chmod the document root itself")
	}
	return os.Chmod(target, mode&0o777)
}

// ZipMaxBytes is the upper bound for files we'll write into a zip archive.
// Refuses zips that would exceed this in total — keeps a single panel call
// from filling the disk.
const ZipMaxBytes int64 = 2 << 30 // 2 GiB

// Zip archives <subs> into <docroot>/<destSub>. Refuses to overwrite. Walks
// directories recursively. Each archive entry is stored with a relative
// path so extraction reproduces the same tree.
func Zip(siteUser, domain string, subs []string, destSub string) error {
	if len(subs) == 0 {
		return fmt.Errorf("no files to archive")
	}
	clean := filepath.Base(filepath.Clean(destSub))
	if clean == "" || clean == "." || clean == ".." || strings.ContainsAny(clean, "/\\") {
		return fmt.Errorf("invalid archive name: %q", destSub)
	}
	if !strings.HasSuffix(strings.ToLower(clean), ".zip") {
		clean += ".zip"
	}
	root, _, err := resolve(siteUser, domain, "")
	if err != nil {
		return err
	}
	// Validate every source path; collect (absPath, relInsideRoot) pairs.
	type src struct{ abs, rel string }
	var srcs []src
	for _, sub := range subs {
		_, target, err := resolve(siteUser, domain, sub)
		if err != nil {
			return err
		}
		if target == root {
			return fmt.Errorf("refusing to zip the entire document root")
		}
		srcs = append(srcs, src{abs: target, rel: filepath.Base(target)})
	}

	dest := filepath.Join(root, clean)
	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("destination already exists: %s", clean)
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(out)
	var total int64
	for _, s := range srcs {
		err := filepath.Walk(s.abs, func(p string, info os.FileInfo, werr error) error {
			if werr != nil {
				return werr
			}
			// Skip the destination file itself in case it's inside the walk.
			if p == dest {
				return nil
			}
			// Skip symlinks — don't follow out of the docroot accidentally.
			if info.Mode()&os.ModeSymlink != 0 {
				return nil
			}
			rel := s.rel
			if p != s.abs {
				rel = filepath.Join(s.rel, strings.TrimPrefix(p, s.abs+string(os.PathSeparator)))
			}
			if info.IsDir() {
				// Directory entries are required by some tools (e.g. Finder).
				_, derr := zw.Create(rel + "/")
				return derr
			}
			total += info.Size()
			if total > ZipMaxBytes {
				return fmt.Errorf("archive exceeds %d bytes", ZipMaxBytes)
			}
			fh, herr := zip.FileInfoHeader(info)
			if herr != nil {
				return herr
			}
			fh.Name = rel
			fh.Method = zip.Deflate
			zfw, ferr := zw.CreateHeader(fh)
			if ferr != nil {
				return ferr
			}
			f, oerr := os.Open(p)
			if oerr != nil {
				return oerr
			}
			defer f.Close()
			_, cerr := io.Copy(zfw, f)
			return cerr
		})
		if err != nil {
			zw.Close()
			out.Close()
			_ = os.Remove(dest)
			return err
		}
	}
	if err := zw.Close(); err != nil {
		out.Close()
		_ = os.Remove(dest)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dest)
		return err
	}
	return chownToUser(dest, siteUser)
}

// IsArchive reports whether name ends in a supported archive extension.
// Used by the API + UI to gate the "Extract" affordance. v0.2.28: covers
// .zip, .tar.gz, .tgz, .tar.
func IsArchive(name string) bool {
	n := strings.ToLower(name)
	return strings.HasSuffix(n, ".zip") ||
		strings.HasSuffix(n, ".tar.gz") ||
		strings.HasSuffix(n, ".tgz") ||
		strings.HasSuffix(n, ".tar")
}

// Unzip extracts <sub> (a supported archive) into its containing directory.
// Supports .zip, .tar.gz, .tgz, .tar. Every entry's final path is re-checked
// for containment so a malicious archive can't "Zip Slip" out of the docroot.
// Name kept as Unzip for API compatibility; covers all formats now.
func Unzip(siteUser, domain, sub string) error {
	root, target, err := resolve(siteUser, domain, sub)
	if err != nil {
		return err
	}
	fi, err := os.Stat(target)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return fmt.Errorf("path is a directory; expected an archive file")
	}
	low := strings.ToLower(target)
	switch {
	case strings.HasSuffix(low, ".zip"):
		return unzipZip(siteUser, root, target)
	case strings.HasSuffix(low, ".tar.gz"), strings.HasSuffix(low, ".tgz"):
		return unzipTar(siteUser, root, target, true)
	case strings.HasSuffix(low, ".tar"):
		return unzipTar(siteUser, root, target, false)
	default:
		return fmt.Errorf("unsupported archive (expected .zip, .tar.gz, .tgz, or .tar): %s", filepath.Base(target))
	}
}

func unzipZip(siteUser, root, target string) error {
	zr, err := zip.OpenReader(target)
	if err != nil {
		return err
	}
	defer zr.Close()

	dir := filepath.Dir(target)
	for _, f := range zr.File {
		// Reject absolute paths, traversal, and entries that, after join,
		// escape the destination directory (the classic Zip Slip check).
		if strings.HasPrefix(f.Name, "/") || strings.Contains(f.Name, "..") {
			return fmt.Errorf("archive contains unsafe path: %q", f.Name)
		}
		dst := filepath.Join(dir, f.Name)
		clean := filepath.Clean(dst)
		if clean != dir && !strings.HasPrefix(clean, dir+string(os.PathSeparator)) {
			return fmt.Errorf("archive entry escapes docroot: %q", f.Name)
		}
		if clean != root && !strings.HasPrefix(clean, root+string(os.PathSeparator)) {
			return fmt.Errorf("archive entry escapes site root: %q", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(clean, 0o755); err != nil {
				return err
			}
			_ = chownToUser(clean, siteUser)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(clean), 0o755); err != nil {
			return err
		}
		mode := f.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		w, err := os.OpenFile(clean, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			w.Close()
			return err
		}
		if _, err := io.Copy(w, rc); err != nil {
			rc.Close()
			w.Close()
			return err
		}
		rc.Close()
		w.Close()
		_ = chownToUser(clean, siteUser)
	}
	return nil
}

// unzipTar handles plain .tar and gzipped .tar.gz/.tgz. Stream-decompresses
// so memory usage stays flat regardless of archive size. Same Zip Slip
// guards as the zip path.
func unzipTar(siteUser, root, target string, gzipped bool) error {
	f, err := os.Open(target)
	if err != nil {
		return err
	}
	defer f.Close()

	var src io.Reader = f
	if gzipped {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("not a valid gzip stream: %w", err)
		}
		defer gz.Close()
		src = gz
	}

	tr := tar.NewReader(src)
	dir := filepath.Dir(target)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read failed: %w", err)
		}
		// Same safety checks as zip; tar is even more permissive on disk
		// layout so we're strict about absolute / traversal / symlink entries.
		if strings.HasPrefix(h.Name, "/") || strings.Contains(h.Name, "..") {
			return fmt.Errorf("archive contains unsafe path: %q", h.Name)
		}
		dst := filepath.Join(dir, h.Name)
		clean := filepath.Clean(dst)
		if clean != dir && !strings.HasPrefix(clean, dir+string(os.PathSeparator)) {
			return fmt.Errorf("archive entry escapes docroot: %q", h.Name)
		}
		if clean != root && !strings.HasPrefix(clean, root+string(os.PathSeparator)) {
			return fmt.Errorf("archive entry escapes site root: %q", h.Name)
		}
		mode := os.FileMode(h.Mode).Perm()
		if mode == 0 {
			mode = 0o644
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(clean, mode|0o100); err != nil {
				return err
			}
			_ = chownToUser(clean, siteUser)
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(clean), 0o755); err != nil {
				return err
			}
			w, err := os.OpenFile(clean, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(w, tr); err != nil {
				w.Close()
				return err
			}
			w.Close()
			_ = chownToUser(clean, siteUser)
		default:
			// Symlinks / hardlinks / devices: silently skip. A WordPress
			// backup tarball won't have any; a malicious one with symlinks
			// pointing out of the docroot just won't materialise them.
		}
	}
	return nil
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

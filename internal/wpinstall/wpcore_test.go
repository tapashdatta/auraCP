package wpinstall

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeTarFixture builds a minimal in-memory tarball that mimics
// WordPress's structure: a single top-level directory whose contents
// extract to the docroot. Used by extractTar tests so we don't have to
// download the real ~25 MB wordpress.org tarball on every test run.
func makeTarFixture(t *testing.T, files map[string]string) *tar.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
		hdr := &tar.Header{
			Name:     "wordpress/" + name,
			Mode:     0o644,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}
		if strings.HasSuffix(name, "/") {
			hdr.Typeflag = tar.TypeDir
			hdr.Size = 0
			content = ""
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if content != "" {
			if _, err := tw.Write([]byte(content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return tar.NewReader(&buf)
}

func TestExtractTarStripsLeadingDir(t *testing.T) {
	dir := t.TempDir()
	tr := makeTarFixture(t, map[string]string{
		"index.php":              "<?php // wp index",
		"wp-includes/version.php": "<?php $wp_version='6.5';",
		"wp-admin/about.php":     "<?php // about",
	})
	n, err := extractTar(tr, dir)
	if err != nil {
		t.Fatalf("extractTar: %v", err)
	}
	if n != 3 {
		t.Errorf("expected 3 files extracted, got %d", n)
	}
	// Verify the leading `wordpress/` was stripped — files land at the
	// docroot, not under docroot/wordpress/.
	for _, rel := range []string{
		"index.php",
		"wp-includes/version.php",
		"wp-admin/about.php",
	} {
		path := filepath.Join(dir, rel)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected file at %s: %v", path, err)
		}
	}
	// And the docroot should NOT contain a `wordpress/` subdir.
	if _, err := os.Stat(filepath.Join(dir, "wordpress")); err == nil {
		t.Error("docroot should not contain a `wordpress/` subdir after extract")
	}
}

// TestExtractTarRejectsPathTraversal verifies the zip-slip defense.
// A tarball entry with `..` should not write outside the destination
// dir even if the strip would expose the escape.
func TestExtractTarRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	// Hostile entry — after stripping `wordpress/`, the relative path
	// is `../escape.txt`, which joined to dst tries to write outside.
	_ = tw.WriteHeader(&tar.Header{
		Name:     "wordpress/../escape.txt",
		Mode:     0o644,
		Size:     5,
		Typeflag: tar.TypeReg,
	})
	tw.Write([]byte("PWNED"))
	tw.Close()

	tr := tar.NewReader(&buf)
	_, err := extractTar(tr, dir)
	if err == nil {
		t.Fatal("expected extractTar to reject path traversal, but it succeeded")
	}
	if !strings.Contains(err.Error(), "escapes destination") {
		t.Errorf("expected 'escapes destination' in error; got: %v", err)
	}
	// And ensure no file landed where the hostile entry pointed.
	parent := filepath.Dir(dir)
	if _, err := os.Stat(filepath.Join(parent, "escape.txt")); err == nil {
		t.Error("hostile tar entry should NOT have created /tmp/escape.txt")
	}
}

package driver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/auracp/auracp/pkg/dbadmin"
)

func TestTunnel_RefusesMissingKeyPath(t *testing.T) {
	_, err := OpenTunnel(context.Background(), &dbadmin.SSHTunnel{
		Host:     "example.com",
		Port:     22,
		Username: "user",
		KeyPath:  "", // empty
	}, "127.0.0.1:3306", 4)
	if err == nil {
		t.Fatal("expected error for missing KeyPath")
	}
	if !strings.Contains(err.Error(), "password auth not supported") {
		t.Errorf("error doesn't mention password-auth refusal: %v", err)
	}
}

func TestTunnel_RefusesNilCfg(t *testing.T) {
	_, err := OpenTunnel(context.Background(), nil, "127.0.0.1:3306", 4)
	if err == nil {
		t.Fatal("expected error for nil cfg")
	}
}

func TestTunnel_RefusesMissingHostPort(t *testing.T) {
	_, err := OpenTunnel(context.Background(), &dbadmin.SSHTunnel{
		Host:     "",
		Username: "user",
		KeyPath:  "/some/key",
	}, "127.0.0.1:3306", 4)
	if err == nil {
		t.Fatal("expected error for missing Host")
	}
}

func TestTunnel_RefusesMissingUsername(t *testing.T) {
	_, err := OpenTunnel(context.Background(), &dbadmin.SSHTunnel{
		Host:    "example.com",
		Port:    22,
		KeyPath: "/some/key",
	}, "127.0.0.1:3306", 4)
	if err == nil {
		t.Fatal("expected error for missing Username")
	}
}

func TestVerifyKeyMode_Refuses0644(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key")
	if err := os.WriteFile(keyPath, []byte("dummy"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := verifyKeyMode(keyPath)
	if err == nil {
		t.Fatal("expected refusal for mode 0644")
	}
	if !strings.Contains(err.Error(), "unsafe permissions") {
		t.Errorf("error doesn't mention unsafe permissions: %v", err)
	}
}

func TestVerifyKeyMode_Accepts0600(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key")
	if err := os.WriteFile(keyPath, []byte("dummy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyKeyMode(keyPath); err != nil {
		t.Errorf("expected to accept 0600, got %v", err)
	}
}

func TestVerifyKeyMode_MissingFile(t *testing.T) {
	err := verifyKeyMode("/nope/never/here")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
	if errors.Is(err, os.ErrNotExist) {
		// Good — file-not-found wrapping preserved.
	}
}

func TestVerifyKeyMode_Accepts0400(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key")
	if err := os.WriteFile(keyPath, []byte("dummy"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := verifyKeyMode(keyPath); err != nil {
		t.Errorf("expected to accept 0400, got %v", err)
	}
}

func TestVerifyKeyMode_RefusesGroupReadable(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key")
	if err := os.WriteFile(keyPath, []byte("dummy"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := verifyKeyMode(keyPath); err == nil {
		t.Error("expected refusal for group-readable 0640")
	}
}

func TestOpenTunnel_RefusesMissingKnownHostsPath(t *testing.T) {
	// Post-review fix: refuse to dial without a host-key pinning
	// source. Even with valid Username + KeyPath, KnownHostsPath
	// must be set.
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key")
	if err := os.WriteFile(keyPath, []byte("dummy"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := OpenTunnel(context.Background(), &dbadmin.SSHTunnel{
		Host:     "example.com",
		Port:     22,
		Username: "user",
		KeyPath:  keyPath,
		// KnownHostsPath intentionally empty.
	}, "127.0.0.1:3306", 4)
	if err == nil {
		t.Fatal("expected error for missing KnownHostsPath")
	}
	if !strings.Contains(err.Error(), "KnownHostsPath required") {
		t.Errorf("error doesn't mention KnownHostsPath: %v", err)
	}
}

func TestOpenTunnel_RefusesInvalidKey(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key")
	if err := os.WriteFile(keyPath, []byte("not a real key"), 0o600); err != nil {
		t.Fatal(err)
	}
	khPath := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(khPath, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := OpenTunnel(context.Background(), &dbadmin.SSHTunnel{
		Host:           "example.com",
		Port:           22,
		Username:       "user",
		KeyPath:        keyPath,
		KnownHostsPath: khPath,
	}, "127.0.0.1:3306", 4)
	if err == nil {
		t.Fatal("expected error for invalid SSH key bytes")
	}
	if !strings.Contains(err.Error(), "parse key") {
		t.Errorf("error doesn't mention parse failure: %v", err)
	}
}

func TestVerifyKeyParentDir_RefusesWorldWritable(t *testing.T) {
	dir := t.TempDir()
	// Set parent dir mode to 0777.
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "key")
	if err := os.WriteFile(keyPath, []byte("dummy"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := verifyKeyParentDir(keyPath)
	if err == nil {
		t.Fatal("expected refusal for world-writable parent dir")
	}
	if !strings.Contains(err.Error(), "group- or world-writable") {
		t.Errorf("error doesn't mention parent-dir mode: %v", err)
	}
}

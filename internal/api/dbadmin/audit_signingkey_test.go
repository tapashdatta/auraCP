package dbadmin

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/auracp/auracp/internal/store"
)

// TestSigningKey_LoadsFromFile is the FIX-1 (PD-SEC-01) regression test:
// loadOrCreateSigningKey MUST read its 32-byte HMAC key from the on-disk
// secrets file, not from the panel settings table. Writing 32 random
// bytes (base64) to the test override path and calling
// loadOrCreateSigningKey must round-trip the same bytes back.
func TestSigningKey_LoadsFromFile(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "aura-db-audit.key")

	var want [32]byte
	if _, err := rand.Read(want[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(want[:])), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	restore := SetSigningKeyPathForTest(keyPath)
	defer restore()

	st, err := store.Open(filepath.Join(dir, "auracp.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	got, err := loadOrCreateSigningKey(st)
	if err != nil {
		t.Fatalf("loadOrCreateSigningKey: %v", err)
	}
	if !bytes.Equal(got, want[:]) {
		t.Fatalf("loaded key does not match on-disk key")
	}
	// The legacy settings row must remain absent.
	if v, ok := st.GetSetting(SigningKeySettingKey); ok && v != "" {
		t.Fatalf("loadOrCreateSigningKey wrote to legacy settings row: %q", v)
	}
}

// TestSigningKey_MigratesLegacySettingsRow verifies the boot-time
// migration: if a legacy aura_db_audit_signing_key row is in the
// settings table at first boot of the new code, we copy it out to the
// on-disk file and DELETE the row so subsequent GET /api/settings
// requests cannot read it.
func TestSigningKey_MigratesLegacySettingsRow(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "aura-db-audit.key")
	restore := SetSigningKeyPathForTest(keyPath)
	defer restore()

	var legacy [32]byte
	for i := range legacy {
		legacy[i] = byte(i + 1)
	}
	st, err := store.Open(filepath.Join(dir, "auracp.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	if err := st.SetSetting(SigningKeySettingKey, base64.StdEncoding.EncodeToString(legacy[:])); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	got, err := loadOrCreateSigningKey(st)
	if err != nil {
		t.Fatalf("loadOrCreateSigningKey: %v", err)
	}
	if !bytes.Equal(got, legacy[:]) {
		t.Fatalf("migration did not preserve the legacy key bytes")
	}
	if v, ok := st.GetSetting(SigningKeySettingKey); ok && v != "" {
		t.Fatalf("legacy settings row not deleted after migration: %q", v)
	}
	// File must now exist with mode 0600.
	fi, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}
	if fi.Mode().Perm()&^0o600 != 0 {
		t.Fatalf("key file mode = %o, want 0600", fi.Mode().Perm())
	}

	// Subsequent call is idempotent — no error, same bytes.
	got2, err := loadOrCreateSigningKey(st)
	if err != nil {
		t.Fatalf("loadOrCreateSigningKey (idempotent): %v", err)
	}
	if !bytes.Equal(got2, legacy[:]) {
		t.Fatalf("second call returned different bytes")
	}
}

// TestSigningKey_CreatesFreshWhenAbsent exercises the cold-start path:
// no settings row, no file. The function mints 32 random bytes, writes
// them to disk with mode 0600, and returns them.
func TestSigningKey_CreatesFreshWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "aura-db-audit.key")
	restore := SetSigningKeyPathForTest(keyPath)
	defer restore()

	st, err := store.Open(filepath.Join(dir, "auracp.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	got, err := loadOrCreateSigningKey(st)
	if err != nil {
		t.Fatalf("loadOrCreateSigningKey: %v", err)
	}
	if len(got) != 32 {
		t.Fatalf("len = %d, want 32", len(got))
	}
	fi, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm()&^0o600 != 0 {
		t.Fatalf("mode = %o, want 0600", fi.Mode().Perm())
	}
}

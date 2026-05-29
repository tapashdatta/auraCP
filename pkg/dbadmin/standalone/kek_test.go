package standalone

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadKEK_EnvVar(t *testing.T) {
	var arr [32]byte
	for i := range arr {
		arr[i] = byte(i)
	}
	t.Setenv(KEKEnvVar, base64.StdEncoding.EncodeToString(arr[:]))
	t.Setenv(KEKFileEnvVar, "")
	kek, err := LoadKEK("")
	if err != nil {
		t.Fatalf("LoadKEK: %v", err)
	}
	if *kek.Bytes() != arr {
		t.Fatal("KEK bytes mismatch")
	}
}

func TestLoadKEK_RejectsBroadMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kek.key")
	if err := os.WriteFile(path, make([]byte, 32), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(KEKEnvVar, "")
	t.Setenv(KEKFileEnvVar, path)
	if _, err := LoadKEK(""); err == nil {
		t.Fatal("expected error for mode 0644 KEK file")
	}
}

func TestLoadOrGenerateKEK_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "kek.key")
	t.Setenv(KEKEnvVar, "")
	t.Setenv(KEKFileEnvVar, "")
	kek, err := LoadOrGenerateKEK(path)
	if err != nil {
		t.Fatalf("LoadOrGenerateKEK: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o400 {
		t.Fatalf("want mode 0400, got %o", st.Mode().Perm())
	}
	// Calling again must load the same key.
	again, err := LoadOrGenerateKEK(path)
	if err != nil {
		t.Fatal(err)
	}
	if *kek.Bytes() != *again.Bytes() {
		t.Fatal("expected idempotent load")
	}
}

func TestRotateKEK_ReEncryptsAll(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var oldKey, newKey [32]byte
	for i := range oldKey {
		oldKey[i] = byte(i)
		newKey[i] = byte(255 - i)
	}
	kek := &KEK{key: &oldKey}

	c := NewConnections(store, kek)
	policy := fastPolicy()
	user, err := store.CreateUser(ctx, "alice", "correct-horse-battery", policy)
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("01234567890123456789")
	if err := store.EnrollTOTP(ctx, kek, user.ID, secret); err != nil {
		t.Fatal(err)
	}

	id, err := c.Save(ctx, mkConn("prod-db", "127.0.0.1", 3306, user.ID), credentialsValue("secret"))
	if err != nil {
		t.Fatal(err)
	}

	// Pass empty keyPath — the test rotates the in-DB ciphertexts only
	// (no on-disk key file involved).
	connsN, mfaN, err := RotateKEK(ctx, store, &oldKey, &newKey, "")
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if connsN != 1 || mfaN != 1 {
		t.Fatalf("expected 1+1 rotated, got %d+%d", connsN, mfaN)
	}

	// Old KEK must no longer decrypt.
	oldConns := NewConnections(store, &KEK{key: &oldKey})
	if _, err := oldConns.Credentials(ctx, id); err == nil {
		t.Fatal("old KEK should no longer decrypt")
	}
	// New KEK decrypts.
	newConns := NewConnections(store, &KEK{key: &newKey})
	creds, err := newConns.Credentials(ctx, id)
	if err != nil {
		t.Fatalf("new KEK decrypt: %v", err)
	}
	if creds.Password != "secret" {
		t.Fatalf("password mismatch after rotate: %q", creds.Password)
	}
}

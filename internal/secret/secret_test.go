package secret

import (
	"testing"
)

// TestEncryptAAD_RoundTrip verifies a value encrypted with a label
// round-trips through DecryptAAD with the same label.
func TestEncryptAAD_RoundTrip(t *testing.T) {
	box, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	enc, err := box.EncryptAAD("hello", "dbadmin:creds:v1")
	if err != nil {
		t.Fatalf("EncryptAAD: %v", err)
	}
	got, err := box.DecryptAAD(enc, "dbadmin:creds:v1")
	if err != nil {
		t.Fatalf("DecryptAAD: %v", err)
	}
	if got != "hello" {
		t.Fatalf("round-trip = %q, want hello", got)
	}
}

// TestEncryptAAD_DifferentLabelFails is the PR #10.5 / FIX-PD-SEC-03
// regression test: a ciphertext minted under label A must not be
// decryptable under label B even when the same KEK is in play.
func TestEncryptAAD_DifferentLabelFails(t *testing.T) {
	box, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	enc, err := box.EncryptAAD("hello", "label-a")
	if err != nil {
		t.Fatalf("EncryptAAD: %v", err)
	}
	if _, err := box.DecryptAAD(enc, "label-b"); err == nil {
		t.Fatal("DecryptAAD with mismatched label succeeded — AAD binding broken")
	}
	// Same KEK without AAD must also fail — the sub-key derivation
	// effectively domain-separates the key, so the un-AAD'd Decrypt
	// cannot recover the value either.
	if _, err := box.Decrypt(enc); err == nil {
		t.Fatal("box.Decrypt of AAD ciphertext succeeded — domain separation broken")
	}
}

// TestEncryptAAD_EmptyLabelRejected guards against operators
// accidentally passing an empty string as the AAD (which would defeat
// the purpose of AAD entirely).
func TestEncryptAAD_EmptyLabelRejected(t *testing.T) {
	box, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := box.EncryptAAD("hello", ""); err == nil {
		t.Fatal("EncryptAAD accepted empty AAD")
	}
	if _, err := box.DecryptAAD("anything", ""); err == nil {
		t.Fatal("DecryptAAD accepted empty AAD")
	}
}

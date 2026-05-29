package standalone

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	var k [32]byte
	if _, err := rand.Read(k[:]); err != nil {
		t.Fatal(err)
	}
	msg := []byte("hello standalone crypto")
	ct, err := seal(&k, msg, nil)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if len(ct) < gcmNonceSize+len(msg) {
		t.Fatalf("ciphertext too short: %d", len(ct))
	}
	pt, err := open(&k, ct, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(pt, msg) {
		t.Fatalf("plaintext mismatch: got %q want %q", pt, msg)
	}
}

func TestOpenRejectsTampered(t *testing.T) {
	var k [32]byte
	rand.Read(k[:])
	ct, err := seal(&k, []byte("payload"), nil)
	if err != nil {
		t.Fatal(err)
	}
	ct[len(ct)-1] ^= 0xff // flip last byte of GCM tag
	if _, err := open(&k, ct, nil); err == nil {
		t.Fatal("open should have rejected tampered ciphertext")
	}
}

func TestOpenRejectsWrongKey(t *testing.T) {
	var k1, k2 [32]byte
	rand.Read(k1[:])
	rand.Read(k2[:])
	ct, _ := seal(&k1, []byte("payload"), nil)
	if _, err := open(&k2, ct, nil); err == nil {
		t.Fatal("open with wrong key should fail")
	}
}

func TestOpenRejectsShortCiphertext(t *testing.T) {
	var k [32]byte
	rand.Read(k[:])
	if _, err := open(&k, []byte{0x00}, nil); err == nil {
		t.Fatal("open should have rejected truncated ciphertext")
	}
}

// TestSeal_RejectsSwappedAAD verifies SEC-04: a ciphertext bound to one
// row context (e.g. "conn:A") must not decrypt with a different context
// (e.g. "conn:B") — even though the same KEK / nonce / GCM tag are valid
// in raw cipher terms.
func TestSeal_RejectsSwappedAAD(t *testing.T) {
	var k [32]byte
	rand.Read(k[:])
	plaintext := []byte("conn-A creds")
	ctA, err := seal(&k, plaintext, []byte("conn:A"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	// Decrypting with the matching AAD must succeed.
	got, err := open(&k, ctA, []byte("conn:A"))
	if err != nil || !bytes.Equal(got, plaintext) {
		t.Fatalf("open with correct AAD: err=%v got=%q", err, got)
	}
	// Decrypting with a different AAD must fail.
	if _, err := open(&k, ctA, []byte("conn:B")); err == nil {
		t.Fatal("open should have rejected swapped AAD (conn:A → conn:B)")
	}
	// Decrypting v2 ciphertext with nil AAD must fail (the empty AAD
	// is itself a binding context that doesn't match "conn:A").
	if _, err := open(&k, ctA, nil); err == nil {
		t.Fatal("open should have rejected nil AAD against v2 ciphertext")
	}
}

// TestOpen_AcceptsLegacyV1Ciphertext verifies the v1 fallback path: rows
// written before SEC-04 (no version byte, no AAD) must still decrypt so
// the migration doesn't break existing deployments. New writes always
// use V2 with row-binding AAD.
func TestOpen_AcceptsLegacyV1Ciphertext(t *testing.T) {
	// Hand-build a v1 ciphertext (no version byte) and confirm open()
	// transparently decodes it.
	var k [32]byte
	rand.Read(k[:])
	v2, err := seal(&k, []byte("payload"), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Strip the cryptoV2AAD version byte to simulate a v1-format blob.
	v1 := v2[1:]
	pt, err := open(&k, v1, nil)
	if err != nil {
		t.Fatalf("v1 open: %v", err)
	}
	if !bytes.Equal(pt, []byte("payload")) {
		t.Fatalf("plaintext mismatch: %q", pt)
	}
}

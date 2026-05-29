// Package secret encrypts sensitive values (DB passwords, Cloudflare tokens)
// at rest using NaCl secretbox. The key lives at /etc/auracp/secret.key (0600),
// generated once on first use.
package secret

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/nacl/secretbox"
)

type Box struct {
	key [32]byte
}

// Open loads the key at dir/secret.key, creating it (0600) if absent.
func Open(dir string) (*Box, error) {
	path := filepath.Join(dir, "secret.key")
	b := &Box{}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		raw, derr := base64.StdEncoding.DecodeString(string(data))
		if derr != nil || len(raw) != 32 {
			return nil, fmt.Errorf("secret.key is corrupt")
		}
		copy(b.key[:], raw)
	case errors.Is(err, os.ErrNotExist):
		if _, rerr := rand.Read(b.key[:]); rerr != nil {
			return nil, rerr
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		enc := base64.StdEncoding.EncodeToString(b.key[:])
		if err := os.WriteFile(path, []byte(enc), 0o600); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	return b, nil
}

// Encrypt returns a base64 string safe to store in SQLite.
func (b *Box) Encrypt(plain string) (string, error) {
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	sealed := secretbox.Seal(nonce[:], []byte(plain), &nonce, &b.key)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

func (b *Box) Decrypt(enc string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || len(raw) < 24 {
		return "", fmt.Errorf("invalid ciphertext")
	}
	var nonce [24]byte
	copy(nonce[:], raw[:24])
	out, ok := secretbox.Open(nil, raw[24:], &nonce, &b.key)
	if !ok {
		return "", fmt.Errorf("decrypt failed")
	}
	return string(out), nil
}

// deriveAADKey returns a sub-key bound to a usage label ("AAD"). NaCl
// secretbox is XSalsa20-Poly1305 which has no native AAD slot; we get
// context binding by domain-separating the encryption key via HKDF-like
// HMAC. Different labels produce different sub-keys, so a ciphertext
// produced under one label cannot be decrypted under another even with
// the same master key. The label is the AAD in everything but name.
//
// PR #10.5 / FIX-PD-SEC-03: previously all encrypted values (panel DB
// passwords, Cloudflare token, dbadmin connection creds) shared the
// same KEK with no context separation, so a ciphertext blob copied from
// one table into another (e.g. via a SQL injection that could write to
// either) would decrypt cleanly. With AAD binding, attempting to
// decrypt a "dbadmin:creds:" ciphertext with the "panel:db:" label
// fails the Poly1305 tag verification.
func (b *Box) deriveAADKey(label string) [32]byte {
	mac := hmac.New(sha256.New, b.key[:])
	mac.Write([]byte("auracp/secret/aad/v1/"))
	mac.Write([]byte(label))
	var out [32]byte
	copy(out[:], mac.Sum(nil))
	return out
}

// EncryptAAD is like Encrypt but binds the ciphertext to an "additional
// authenticated data" label. Decryption with a different label fails.
// Use distinct labels for distinct value classes (e.g. "dbadmin:creds:"
// for dbadmin connection credentials) so a ciphertext extracted from
// one storage location cannot be replayed into another.
//
// The wire format is identical to Encrypt's so the on-disk size is the
// same; the difference is the derived sub-key, not the layout.
func (b *Box) EncryptAAD(plain, aad string) (string, error) {
	if aad == "" {
		return "", fmt.Errorf("secret: empty AAD")
	}
	key := b.deriveAADKey(aad)
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	sealed := secretbox.Seal(nonce[:], []byte(plain), &nonce, &key)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// DecryptAAD reverses EncryptAAD. The aad parameter MUST match the one
// passed at encryption time; mismatched labels fail with "decrypt
// failed" (the standard Poly1305 reject path).
func (b *Box) DecryptAAD(enc, aad string) (string, error) {
	if aad == "" {
		return "", fmt.Errorf("secret: empty AAD")
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || len(raw) < 24 {
		return "", fmt.Errorf("invalid ciphertext")
	}
	key := b.deriveAADKey(aad)
	var nonce [24]byte
	copy(nonce[:], raw[:24])
	out, ok := secretbox.Open(nil, raw[24:], &nonce, &key)
	if !ok {
		return "", fmt.Errorf("decrypt failed")
	}
	return string(out), nil
}

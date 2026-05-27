// Package secret encrypts sensitive values (DB passwords, Cloudflare tokens)
// at rest using NaCl secretbox. The key lives at /etc/auracp/secret.key (0600),
// generated once on first use.
package secret

import (
	"crypto/rand"
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

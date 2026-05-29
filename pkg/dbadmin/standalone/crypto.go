package standalone

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

// GCM nonce size, fixed by the spec.
const gcmNonceSize = 12

// Ciphertext layout versions. A leading byte distinguishes formats so we
// can transition without re-encrypting every existing row at once:
//
//   - cryptoV1Legacy = no version byte; raw nonce||ct||tag. Open() falls
//     back to this when the leading byte isn't a known version marker.
//   - cryptoV2AAD = 0x02 || nonce || ct || tag, sealed with row-context
//     AAD (e.g. []byte("conn:<id>"), []byte("mfa:<user_id>")). Required
//     for new writes to defend against cross-row ciphertext swaps (SEC-04).
const (
	cryptoV2AAD byte = 0x02
)

// errCipherText is returned for short / malformed ciphertexts.
var errCipherText = errors.New("standalone: ciphertext too short or malformed")

// connAAD builds the row-binding AAD for a connection's credentials blob.
func connAAD(connID string) []byte {
	return []byte("conn:" + connID)
}

// mfaAAD builds the row-binding AAD for a user's MFA secret blob.
func mfaAAD(userID string) []byte {
	return []byte("mfa:" + userID)
}

// seal encrypts plaintext under kek with row-binding AAD. Returned blob
// has the cryptoV2AAD layout: version || nonce || ct || tag. The aad is
// authenticated but NOT included in the ciphertext — callers MUST supply
// the same aad to open(), or decryption fails. Pass nil aad only for
// data that isn't bound to a row context.
func seal(kek *[32]byte, plaintext, aad []byte) ([]byte, error) {
	if kek == nil {
		return nil, errors.New("standalone: nil KEK")
	}
	block, err := aes.NewCipher(kek[:])
	if err != nil {
		return nil, fmt.Errorf("standalone: NewCipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("standalone: NewGCM: %w", err)
	}
	nonce := make([]byte, gcmNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("standalone: rand for nonce: %w", err)
	}
	ct := gcm.Seal(nil, nonce, plaintext, aad)
	out := make([]byte, 0, 1+len(nonce)+len(ct))
	out = append(out, cryptoV2AAD)
	out = append(out, nonce...)
	out = append(out, ct...)
	return out, nil
}

// open reverses seal. blob is either:
//
//   - cryptoV2AAD format (version || nonce || ct || tag) — aad is
//     required and authenticated, OR
//   - legacy v1 format (nonce || ct || tag) written before SEC-04 fix —
//     transparently accepted with empty AAD for backward compatibility.
//     New writes use V2; legacy rows are upgraded on the next save.
func open(kek *[32]byte, blob, aad []byte) ([]byte, error) {
	if kek == nil {
		return nil, errors.New("standalone: nil KEK")
	}
	if len(blob) < gcmNonceSize+16 {
		// Need at least nonce + GCM tag.
		return nil, errCipherText
	}
	block, err := aes.NewCipher(kek[:])
	if err != nil {
		return nil, fmt.Errorf("standalone: NewCipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("standalone: NewGCM: %w", err)
	}

	// V2 path: leading byte is the version marker. We require an extra
	// byte beyond the legacy minimum length to disambiguate.
	if len(blob) >= 1+gcmNonceSize+16 && blob[0] == cryptoV2AAD {
		nonce := blob[1 : 1+gcmNonceSize]
		ct := blob[1+gcmNonceSize:]
		pt, err := gcm.Open(nil, nonce, ct, aad)
		if err != nil {
			return nil, fmt.Errorf("standalone: gcm.Open(v2): %w", err)
		}
		return pt, nil
	}

	// Legacy v1 path: no AAD was used at write time. Decrypt with no
	// AAD regardless of what the caller supplied; the cryptoV2AAD fix
	// can only protect rows written under v2.
	nonce := blob[:gcmNonceSize]
	ct := blob[gcmNonceSize:]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("standalone: gcm.Open(v1): %w", err)
	}
	return pt, nil
}

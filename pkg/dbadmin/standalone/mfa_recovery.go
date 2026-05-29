package standalone

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"strings"
)

// RecoveryCodeCount is the number of single-use recovery codes generated
// at enrollment.
const RecoveryCodeCount = 8

// recoveryCodeBytes is the entropy per code before encoding (10 bytes
// → 16 chars base32 sans padding, dashed for legibility).
const recoveryCodeBytes = 10

// GenerateRecoveryCodes returns 8 freshly minted single-use codes,
// formatted as XXXX-XXXX-XXXX-XXXX (uppercase, no padding).
func GenerateRecoveryCodes() ([]string, error) {
	out := make([]string, RecoveryCodeCount)
	for i := range out {
		var b [recoveryCodeBytes]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, err
		}
		raw := strings.TrimRight(base32.StdEncoding.EncodeToString(b[:]), "=")
		// 16 chars -> insert dash every 4.
		var sb strings.Builder
		for j := 0; j < len(raw); j++ {
			if j != 0 && j%4 == 0 {
				sb.WriteByte('-')
			}
			sb.WriteByte(raw[j])
		}
		out[i] = sb.String()
	}
	return out, nil
}

// NormalizeRecoveryCode strips whitespace + dashes and uppercases the
// input so verify is shape-agnostic.
func NormalizeRecoveryCode(in string) string {
	r := strings.ToUpper(in)
	r = strings.ReplaceAll(r, "-", "")
	r = strings.ReplaceAll(r, " ", "")
	return strings.TrimSpace(r)
}

// ErrInvalidRecoveryCode is returned by callers when a recovery code
// does not match any stored hash.
var ErrInvalidRecoveryCode = errors.New("standalone: invalid recovery code")

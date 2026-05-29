package standalone

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // TOTP RFC 6238 specifies SHA-1 as the default HMAC.
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTPSecretLen is the byte length of a freshly generated TOTP secret.
const TOTPSecretLen = 20

// GenerateTOTPSecret returns 20 random bytes suitable for TOTP.
func GenerateTOTPSecret() ([]byte, error) {
	b := make([]byte, TOTPSecretLen)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// TOTPProvisioningURI builds an otpauth:// URI for QR enrollment.
// issuer is typically "aura-db"; accountName is the user's username.
func TOTPProvisioningURI(secret []byte, issuer, accountName string) string {
	b32 := strings.TrimRight(base32.StdEncoding.EncodeToString(secret), "=")
	label := url.PathEscape(issuer + ":" + accountName)
	q := url.Values{}
	q.Set("secret", b32)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", "6")
	q.Set("period", "30")
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// VerifyTOTP returns (matchedStep, nil) if code matches the secret within
// the ±1 step window around t (30s period). matchedStep is the absolute
// step counter (t.Unix()/30 + off) that produced the match — callers
// persist this so a future verification can reject replays (a code with
// matchedStep <= user.LastTOTPStep MUST be rejected). Returns
// (0, errInvalidTOTP) on mismatch.
func VerifyTOTP(secret []byte, code string, t time.Time) (int64, error) {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return 0, errInvalidTOTP
	}
	step := t.Unix() / 30
	for _, off := range []int64{-1, 0, 1} {
		candidate := step + off
		want := computeTOTP(secret, uint64(candidate))
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return candidate, nil
		}
	}
	return 0, errInvalidTOTP
}

var errInvalidTOTP = errors.New("standalone: invalid TOTP code")

func computeTOTP(secret []byte, step uint64) string {
	mac := hmac.New(sha1.New, secret) //nolint:gosec
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], step)
	mac.Write(buf[:])
	h := mac.Sum(nil)
	offset := h[len(h)-1] & 0x0f
	v := binary.BigEndian.Uint32(h[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1_000_000)
}

package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// RFC 6238 TOTP, hand-rolled (SHA-1, 6 digits, 30s period) to avoid a dependency.

func NewTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.TrimRight(base32.StdEncoding.EncodeToString(b), "="), nil
}

// TOTPURI builds the otpauth:// URI for authenticator-app enrollment (and QR).
func TOTPURI(secret, issuer, account string) string {
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", issuer)
	v.Set("algorithm", "SHA1")
	v.Set("digits", "6")
	v.Set("period", "30")
	label := url.PathEscape(issuer + ":" + account)
	return "otpauth://totp/" + label + "?" + v.Encode()
}

// VerifyTOTP checks input against the secret, allowing ±1 step for clock skew.
func VerifyTOTP(secret, input string) bool {
	input = strings.TrimSpace(input)
	if len(input) != 6 {
		return false
	}
	now := time.Now().Unix() / 30
	for _, d := range []int64{-1, 0, 1} {
		c, err := totpCode(secret, now+d)
		if err == nil && subtle.ConstantTimeCompare([]byte(c), []byte(input)) == 1 {
			return true
		}
	}
	return false
}

func totpCode(secret string, counter int64) (string, error) {
	key, err := base32.StdEncoding.DecodeString(padBase32(secret))
	if err != nil {
		return "", err
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(counter))
	h := hmac.New(sha1.New, key)
	h.Write(buf[:])
	sum := h.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	val := (uint32(sum[off]&0x7f) << 24) | (uint32(sum[off+1]) << 16) |
		(uint32(sum[off+2]) << 8) | uint32(sum[off+3])
	return fmt.Sprintf("%06d", val%1_000_000), nil
}

func padBase32(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if m := len(s) % 8; m != 0 {
		s += strings.Repeat("=", 8-m)
	}
	return s
}

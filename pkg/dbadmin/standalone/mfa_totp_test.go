package standalone

import (
	"strings"
	"testing"
	"time"
)

func TestTOTPRoundTrip(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	code := computeTOTP(secret, uint64(now.Unix()/30))
	step, err := VerifyTOTP(secret, code, now)
	if err != nil {
		t.Fatalf("VerifyTOTP: %v", err)
	}
	if step != now.Unix()/30 {
		t.Fatalf("expected matched step %d, got %d", now.Unix()/30, step)
	}
}

func TestTOTPRejectsInvalid(t *testing.T) {
	secret, _ := GenerateTOTPSecret()
	if _, err := VerifyTOTP(secret, "000000", time.Now()); err == nil {
		t.Fatal("expected rejection for arbitrary code")
	}
	if _, err := VerifyTOTP(secret, "abc", time.Now()); err == nil {
		t.Fatal("expected rejection for too-short code")
	}
}

func TestTOTPProvisioningURI(t *testing.T) {
	secret := []byte("01234567890123456789")
	uri := TOTPProvisioningURI(secret, "aura-db", "alice")
	if !strings.HasPrefix(uri, "otpauth://totp/") {
		t.Fatalf("unexpected URI scheme: %q", uri)
	}
	if !strings.Contains(uri, "issuer=aura-db") {
		t.Fatalf("expected issuer in URI: %q", uri)
	}
}

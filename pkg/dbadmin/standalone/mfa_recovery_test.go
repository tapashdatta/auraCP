package standalone

import (
	"testing"
)

func TestGenerateRecoveryCodes(t *testing.T) {
	codes, err := GenerateRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != RecoveryCodeCount {
		t.Fatalf("want %d codes; got %d", RecoveryCodeCount, len(codes))
	}
	seen := make(map[string]bool)
	for _, c := range codes {
		if seen[c] {
			t.Fatalf("duplicate code: %q", c)
		}
		seen[c] = true
	}
}

func TestNormalizeRecoveryCode(t *testing.T) {
	got := NormalizeRecoveryCode(" abcd-EFGH-1234-jklm ")
	want := "ABCDEFGH1234JKLM"
	if got != want {
		t.Fatalf("normalize: got %q want %q", got, want)
	}
}

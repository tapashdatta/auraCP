package wpinstall

import (
	"strings"
	"testing"
)

// TestLocalSaltsShape pins the local salt fallback's format. If wp-cli
// changes wp-config.php's expected define() shape upstream, the fallback
// fails loud here rather than at runtime when api.wordpress.org is down.
func TestLocalSaltsShape(t *testing.T) {
	out := localSalts()
	requiredKeys := []string{
		"AUTH_KEY", "SECURE_AUTH_KEY", "LOGGED_IN_KEY", "NONCE_KEY",
		"AUTH_SALT", "SECURE_AUTH_SALT", "LOGGED_IN_SALT", "NONCE_SALT",
	}
	for _, k := range requiredKeys {
		if !strings.Contains(out, "define( '"+k+"',") {
			t.Errorf("localSalts() missing key: %s\n%s", k, out)
		}
	}
	// Each define line should end with a 64-hex-char value.
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		// `define( 'KEY', 'value' );`
		// We loosely check: contains a ', a value, and ends with ');
		if !strings.HasSuffix(line, "' );") {
			t.Errorf("local salt line bad shape: %q", line)
		}
	}
	// Sanity: each call should produce distinct values (high-entropy).
	out2 := localSalts()
	if out == out2 {
		t.Error("localSalts() should be non-deterministic (entropy from crypto/rand)")
	}
}

// TestTarballURL verifies locale → URL mapping. We deliberately route
// en_US (and empty) to the global latest endpoint and other locales to
// their localized hostname.
func TestTarballURL(t *testing.T) {
	cases := []struct {
		locale string
		want   string
	}{
		{"", "https://wordpress.org/latest.tar.gz"},
		{"en_US", "https://wordpress.org/latest.tar.gz"},
		{"de_DE", "https://de_de.wordpress.org/latest-de_DE.tar.gz"},
		{"fr_FR", "https://fr_fr.wordpress.org/latest-fr_FR.tar.gz"},
		{"ja", "https://ja.wordpress.org/latest-ja.tar.gz"},
	}
	for _, c := range cases {
		got := tarballURL(c.locale)
		if got != c.want {
			t.Errorf("tarballURL(%q) = %q, want %q", c.locale, got, c.want)
		}
	}
}

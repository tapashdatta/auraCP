package api

import "testing"

// TestSafeNextPath — FIX-6 (PR #11 must-fix). The panel login flow accepts
// ?next= and (after a successful sign-in) redirects to it. Without
// validation, the panel becomes an open redirector; e.g.
// /login?next=//evil.example/ would land the operator at evil.example
// after authenticating. safeNextPath enforces:
//
//   - next must be a single-slash absolute path under THIS host
//   - "//host/..." and "/\host\..." are rejected (protocol-relative)
//   - "scheme:" (javascript:, data:, etc.) before the first slash is rejected
//   - missing/empty next defaults to "/"
func TestSafeNextPath(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// Happy path: a legitimate in-app target survives unchanged.
		{"legitimate dbadmin path", "/dbadmin/", "/dbadmin/"},
		{"deep panel path", "/sites/example.com", "/sites/example.com"},
		{"path with query", "/dbadmin/?tab=conns", "/dbadmin/?tab=conns"},

		// Open-redirect attempts: every one must collapse to "/".
		{"malicious protocol-relative double slash", "//evil.example/", "/"},
		{"malicious protocol-relative with path", "//evil.example/foo", "/"},
		{"malicious backslash escape", "/\\evil.example/foo", "/"},
		{"javascript scheme on first segment", "/javascript:alert(1)", "/"},
		{"data scheme on first segment", "/data:text/html,foo", "/"},

		// Non-absolute or absent → default "/".
		{"empty", "", "/"},
		{"relative path", "dbadmin/", "/"},
		{"scheme-prefixed", "https://evil.example/", "/"},
		{"fragment-only", "#foo", "/"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := safeNextPath(c.in); got != c.want {
				t.Fatalf("safeNextPath(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

package main

import "testing"

// TestTLSMinVersion confirms the version string -> uint16 mapping is
// consistent with config defaults.
func TestTLSMinVersion(t *testing.T) {
	cases := []struct {
		in   string
		want uint16
	}{
		{"TLS1.3", 0x0304},
		{"TLS1.2", 0x0303},
		{"", 0x0304},
		{"garbage", 0x0304},
	}
	for _, c := range cases {
		if got := tlsMinVersion(c.in); got != c.want {
			t.Fatalf("tlsMinVersion(%q) = %x; want %x", c.in, got, c.want)
		}
	}
}

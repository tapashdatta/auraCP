package standalone

import "testing"

func TestIPClassFromHost(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"1.2.3.4", "1.2.3.0/24"},
		{"10.0.0.1", "10.0.0.0/24"},
		{"2001:db8::1", "2001:db8::/56"},
		{"::1", "::/56"},
		{"bogus", "unknown"},
	}
	for _, c := range cases {
		got := IPClassFromHost(c.in)
		if got != c.want {
			t.Fatalf("IPClassFromHost(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestHashUAString_Length(t *testing.T) {
	h := HashUAString("Mozilla/5.0")
	if len(h) != 16 {
		t.Fatalf("hash should be 16 hex chars; got %d", len(h))
	}
}

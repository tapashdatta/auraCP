package httpapi

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// SEC-3 / ux-4 (PR #16.5): RFC 5987 percent-encoding for the
// Content-Disposition filename* parameter.
func TestRFC5987Encode(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"plain.csv", "plain.csv"},
		{"with space.csv", "with%20space.csv"},
		{"percent%50.csv", "percent%2550.csv"},
		// non-ASCII bytes: U+00E9 is 0xC3 0xA9 in UTF-8.
		{"café.csv", "caf%C3%A9.csv"},
		// reserved chars
		// `&` is in the RFC 5987 attr-char set; `,` and `"` are not.
		{`weird,"&.csv`, "weird%2C%22&.csv"},
		{"safe!#$&+-.^_`|~chars.csv", "safe!#$&+-.^_`|~chars.csv"},
	}
	for _, c := range cases {
		if got := rfc5987Encode(c.in); got != c.want {
			t.Errorf("rfc5987Encode(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestASCIIFilename(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"plain.csv", "plain.csv"},
		{"café.csv", "caf__.csv"}, // 2 bytes of é → 2 underscores.
		{"naughty\"quote.csv", "naughty_quote.csv"},
		{"back\\slash.csv", "back_slash.csv"},
		{"tab\there.csv", "tab_here.csv"}, // tab (0x09) < 0x20 → underscore.
	}
	for _, c := range cases {
		if got := asciiFilename(c.in); got != c.want {
			t.Errorf("asciiFilename(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// C11 / SEC-7 (PR #16.5): empty userID → ErrEmptyUserID instead of the
// previous silent bypass.
func TestExportLockManager_EmptyUserID(t *testing.T) {
	m := newExportLockManager()
	ok, err := m.tryAcquire("")
	if ok {
		t.Error("expected acquired=false for empty userID")
	}
	if !errors.Is(err, ErrEmptyUserID) {
		t.Errorf("expected ErrEmptyUserID, got %v", err)
	}
}

// Acquire / release / re-acquire cycle for a single user.
func TestExportLockManager_AcquireReleaseAcquire(t *testing.T) {
	m := newExportLockManager()
	if ok, err := m.tryAcquire("alice"); err != nil || !ok {
		t.Fatalf("first acquire failed: ok=%v err=%v", ok, err)
	}
	if ok, _ := m.tryAcquire("alice"); ok {
		t.Error("expected second concurrent acquire to fail")
	}
	m.release("alice")
	if ok, err := m.tryAcquire("alice"); err != nil || !ok {
		t.Fatalf("post-release re-acquire failed: ok=%v err=%v", ok, err)
	}
}

// SEC-4 (PR #16.5): idle slots older than the TTL get evicted on the
// next tryAcquire so the map cannot grow without bound.
func TestExportLockManager_TTLEviction(t *testing.T) {
	m := newExportLockManager()
	// Freeze "now" so we can advance it deterministically.
	base := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	cur := base
	m.now = func() time.Time { return cur }

	// Populate three slots, release each (so they're idle).
	for _, u := range []string{"a", "b", "c"} {
		if ok, err := m.tryAcquire(u); err != nil || !ok {
			t.Fatalf("acquire %s: ok=%v err=%v", u, ok, err)
		}
		m.release(u)
	}
	if len(m.slots) != 3 {
		t.Fatalf("expected 3 slots, got %d", len(m.slots))
	}

	// Jump past the TTL — next acquire should evict all idle.
	cur = base.Add(exportSlotIdleTTL + time.Second)
	if ok, err := m.tryAcquire("d"); err != nil || !ok {
		t.Fatalf("post-TTL acquire failed: ok=%v err=%v", ok, err)
	}
	// "a", "b", "c" must have been evicted; only "d" remains.
	if _, present := m.slots["a"]; present {
		t.Error("slot a should have been evicted")
	}
	if _, present := m.slots["d"]; !present {
		t.Error("slot d should still be present")
	}
}

// Cap-enforced LRU: when the map is full, the oldest idle entry gets
// dropped to make room.
func TestExportLockManager_CapEvictsOldest(t *testing.T) {
	m := newExportLockManager()
	base := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	cur := base
	m.now = func() time.Time { return cur }

	// Manually seed the map at the cap with distinct lastSeen values.
	for i := 0; i < exportSlotsMax; i++ {
		slot := &exportSlot{}
		slot.lastSeen.Store(base.Add(time.Duration(i) * time.Millisecond).UnixNano())
		m.slots["u"+itoa(i)] = slot
	}
	if len(m.slots) != exportSlotsMax {
		t.Fatalf("seed: got %d slots", len(m.slots))
	}
	// Avoid the TTL evict path firing (idle entries are all "fresh").
	cur = base.Add(10 * time.Millisecond)
	if ok, err := m.tryAcquire("new"); err != nil || !ok {
		t.Fatalf("cap acquire: ok=%v err=%v", ok, err)
	}
	// "u0" was the oldest idle; should be evicted.
	if _, present := m.slots["u0"]; present {
		t.Error("oldest idle slot u0 should have been evicted")
	}
	if _, present := m.slots["new"]; !present {
		t.Error("new slot should be present")
	}
}

// Small itoa avoids the strconv import in test-only code.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	pos := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		pos--
		b[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

// C14 (PR #16.5): countingWriter byte count is atomic.
func TestCountingWriter_AtomicCount(t *testing.T) {
	var sb strings.Builder
	cw := newCountingWriter(&sb)
	if _, err := cw.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := cw.Write([]byte(" world")); err != nil {
		t.Fatal(err)
	}
	if got := cw.BytesWritten(); got != 11 {
		t.Errorf("BytesWritten = %d, want 11", got)
	}
	if sb.String() != "hello world" {
		t.Errorf("write content lost: %q", sb.String())
	}
}

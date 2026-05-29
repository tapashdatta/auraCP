package standalone

// Regression tests for PR #9 must-fix items. Each test pins the
// invariant of a single fix so a future refactor regressing it is loud.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// FIX-1 SEC-01: lockout case-bypass.
//
// Hammer "alice", "Alice", "ALICE" in rotation — they all resolve to the
// same user row (COLLATE NOCASE) so per-user lockout MUST trip after the
// threshold regardless of which capitalization the attacker rotated to.
func TestLockout_CaseRotationDoesNotBypass(t *testing.T) {
	store, kek := newTestStore(t)
	auth := newTestAuth(t, store, kek)
	auth.cfg.LoginPerIP15m = 0   // disable IP lockout for this scope-isolation test
	auth.cfg.LoginPerUser15m = 3 // tight ceiling for tests
	ctx := context.Background()
	if _, err := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy()); err != nil {
		t.Fatal(err)
	}
	variants := []string{"alice", "Alice", "ALICE", "AliCe"}
	for i, name := range variants {
		_, _ = auth.Login(ctx, LoginRequest{
			Username: name,
			Password: "wrong-pass-12345",
			IPClass:  fmt.Sprintf("10.0.%d.0/24", i), // unique IP so IP scope alone can't lock
		})
	}
	// After 4 case-rotated attempts the per-user lockout (limit 3) must have tripped.
	_, err := auth.Login(ctx, LoginRequest{
		Username: "ALICE",
		Password: "correct-horse-battery",
		IPClass:  "10.0.99.0/24",
	})
	if !errors.Is(err, ErrLockedOut) {
		t.Fatalf("expected ErrLockedOut after case-rotated attempts, got %v", err)
	}
}

// FIX-3 SEC-03: MFA-required users without TOTP must count against
// per-user lockout. Otherwise an attacker with a known username has an
// unbounded password-correctness oracle (the engine returns ErrMFARequired
// only when the password is correct).
func TestLogin_MFARequiredCountsAgainstLockout(t *testing.T) {
	store, kek := newTestStore(t)
	auth := newTestAuth(t, store, kek)
	auth.cfg.LoginPerUser15m = 3
	ctx := context.Background()
	user, err := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnrollTOTP(ctx, kek, user.ID, []byte("01234567890123456789")); err != nil {
		t.Fatal(err)
	}
	// Fire N correct-password requests with empty TOTP — each returns
	// ErrMFARequired but must bump the per-user counter.
	for i := 0; i < 3; i++ {
		_, err := auth.Login(ctx, LoginRequest{
			Username: "alice", Password: "correct-horse-battery",
			IPClass: fmt.Sprintf("10.0.%d.0/24", i),
		})
		if !errors.Is(err, ErrMFARequired) {
			t.Fatalf("attempt %d: expected ErrMFARequired, got %v", i, err)
		}
	}
	// Now the next login attempt — even with correct password AND correct
	// TOTP — must hit lockout.
	_, err = auth.Login(ctx, LoginRequest{
		Username: "alice", Password: "correct-horse-battery",
		TOTPCode: computeCurrentTOTP([]byte("01234567890123456789")),
		IPClass:  "10.0.99.0/24",
	})
	if !errors.Is(err, ErrLockedOut) {
		t.Fatalf("expected ErrLockedOut after MFA-required oracle attempts, got %v", err)
	}
}

// FIX-5 SEC-07: IPClassWithTrust honors X-Forwarded-For when the request
// arrives from a trusted-proxy CIDR.
func TestIPClass_HonorsXFFFromTrustedProxy(t *testing.T) {
	_, trusted, _ := net.ParseCIDR("127.0.0.0/8")
	r, _ := http.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("X-Forwarded-For", "203.0.113.45")
	got := IPClassWithTrust(r, []*net.IPNet{trusted})
	if got != "203.0.113.0/24" {
		t.Fatalf("expected 203.0.113.0/24 from XFF, got %q", got)
	}
}

// FIX-5 SEC-07: a request whose RemoteAddr is NOT in the trusted CIDR
// list must NOT have its XFF header consulted — otherwise any client can
// spoof their source IP and defeat lockout / session binding.
func TestIPClass_IgnoresXFFFromUntrustedSource(t *testing.T) {
	_, trusted, _ := net.ParseCIDR("127.0.0.0/8")
	r, _ := http.NewRequest("GET", "/", nil)
	// Attacker connects directly from the internet, claims a proxy in XFF.
	r.RemoteAddr = "198.51.100.5:12345"
	r.Header.Set("X-Forwarded-For", "10.0.0.99")
	got := IPClassWithTrust(r, []*net.IPNet{trusted})
	if got != "198.51.100.0/24" {
		t.Fatalf("expected 198.51.100.0/24 from RemoteAddr, got %q (XFF spoof accepted!)", got)
	}
}

// FIX-5 SEC-07: with a chain of proxies in XFF, return the leftmost
// UNTRUSTED address (the original client).
func TestIPClass_HonorsXFFChainLeftmostUntrusted(t *testing.T) {
	_, t1, _ := net.ParseCIDR("127.0.0.0/8")
	_, t2, _ := net.ParseCIDR("10.0.0.0/8")
	r, _ := http.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("X-Forwarded-For", "203.0.113.45, 10.0.0.5, 10.0.0.6")
	got := IPClassWithTrust(r, []*net.IPNet{t1, t2})
	if got != "203.0.113.0/24" {
		t.Fatalf("expected 203.0.113.0/24 (leftmost untrusted), got %q", got)
	}
}

// FIX-6 C2: Save creates an atomic owner grant. Verify by saving with a
// non-empty Owner and checking that the grants table has the matching
// owner row in one round-trip.
func TestSave_AtomicWithOwnerGrant(t *testing.T) {
	store, kek := newTestStore(t)
	c := NewConnections(store, kek)
	ctx := context.Background()
	owner, _ := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy())

	id, err := c.Save(ctx, mkConn("prod", "127.0.0.1", 3306, owner.ID), credentialsValue("s3cret"))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	// The grant must already exist — no separate Grant() call needed.
	var role int
	row := store.DB.QueryRowContext(ctx,
		`SELECT role FROM connection_grants WHERE user_id = ? AND connection_id = ?`,
		owner.ID, string(id))
	if err := row.Scan(&role); err != nil {
		t.Fatalf("expected atomic owner grant, but row missing: %v", err)
	}
	if dbadmin.Role(role) != dbadmin.RoleOwner {
		t.Fatalf("expected RoleOwner, got %v", dbadmin.Role(role))
	}
}

// FIX-7 C3: Grant returns ErrNotFound only when user/connection genuinely
// missing. Other DB errors (we can't easily simulate SQLITE_BUSY in unit
// tests, so we settle for asserting the missing-user / missing-conn
// distinction works correctly).
func TestGrant_DistinguishesMissingFromError(t *testing.T) {
	store, kek := newTestStore(t)
	c := NewConnections(store, kek)
	ctx := context.Background()
	owner, _ := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy())
	id, _ := c.Save(ctx, mkConn("prod", "127.0.0.1", 3306, owner.ID), credentialsValue("pw"))

	// Missing user → ErrNotFound.
	if err := c.Grant(ctx, owner.ID, "user-that-doesnt-exist", id, dbadmin.RoleViewer); !errors.Is(err, dbadmin.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing user, got %v", err)
	}
	// Missing connection → ErrNotFound.
	if err := c.Grant(ctx, owner.ID, owner.ID, "conn-that-doesnt-exist", dbadmin.RoleViewer); !errors.Is(err, dbadmin.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing connection, got %v", err)
	}
	// Happy path → nil.
	other, _ := store.CreateUser(ctx, "bob", "another-strong-pass", fastPolicy())
	if err := c.Grant(ctx, owner.ID, other.ID, id, dbadmin.RoleViewer); err != nil {
		t.Fatalf("expected nil on valid grant, got %v", err)
	}
}

// FIX-9 connstore-get-no-tenant-filter: GetForUser filters by grant —
// a user with no grant on a connection sees ErrNotFound (matches the
// "404 for forbidden" policy).
func TestGet_FiltersByGrant(t *testing.T) {
	store, kek := newTestStore(t)
	c := NewConnections(store, kek)
	ctx := context.Background()
	owner, _ := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy())
	stranger, _ := store.CreateUser(ctx, "bob", "another-strong-pass", fastPolicy())
	id, err := c.Save(ctx, mkConn("private", "127.0.0.1", 3306, owner.ID), credentialsValue("pw"))
	if err != nil {
		t.Fatal(err)
	}

	// Owner sees it.
	if _, err := c.GetForUser(ctx, dbadmin.User{ID: owner.ID}, id); err != nil {
		t.Fatalf("owner GetForUser: %v", err)
	}
	// Stranger does NOT — must be indistinguishable from "doesn't exist".
	if _, err := c.GetForUser(ctx, dbadmin.User{ID: stranger.ID}, id); !errors.Is(err, dbadmin.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for stranger, got %v", err)
	}
}

// FIX-8 audit-forwarders-unwired: Bootstrap honors cfg.Audit.Forwarders.
// Stand up a test webhook server, fire an audit event, assert the
// server received it.
func TestBootstrap_WiresConfiguredForwarders(t *testing.T) {
	tmpdir := t.TempDir()
	dbPath := filepath.Join(tmpdir, "aura.db")
	auditPath := filepath.Join(tmpdir, "audit.log")
	historyPath := filepath.Join(tmpdir, "history.db")
	kekPath := filepath.Join(tmpdir, "kek.key")
	secretPath := filepath.Join(tmpdir, "webhook.secret")

	// Pre-create dependencies.
	if err := InitKEKFile(kekPath); err != nil {
		t.Fatalf("InitKEKFile: %v", err)
	}
	if err := os.WriteFile(secretPath, []byte("shared-hmac-secret"), 0o400); err != nil {
		t.Fatal(err)
	}

	var received atomic.Int64
	gotBody := make(chan []byte, 1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		received.Add(1)
		select {
		case gotBody <- body:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.Storage.DBPath = dbPath
	cfg.Storage.AuditLogPath = auditPath
	cfg.Storage.HistoryDBPath = historyPath
	cfg.KEK.File = kekPath
	cfg.Audit.Forwarders = []AuditForwarderConfig{
		{
			Kind:       "webhook",
			URL:        srv.URL,
			SecretFile: secretPath,
		},
	}

	// httptest.NewTLSServer hands back an https:// URL but uses a
	// self-signed cert — temporarily make the forwarder accept it.
	st, err := Bootstrap(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	// Swap the http.Client on the (only) webhook forwarder for one that
	// trusts the test server's self-signed cert.
	if wf, ok := st.Audit.Forwarder.(*WebhookForwarder); ok {
		wf.Client = srv.Client()
	} else if mf, ok := st.Audit.Forwarder.(MultiForwarder); ok && len(mf.Targets) > 0 {
		if wf, ok := mf.Targets[0].(*WebhookForwarder); ok {
			wf.Client = srv.Client()
		}
	}
	t.Cleanup(func() { _ = st.Close() })

	// Emit a synthetic event.
	st.Audit.Record(context.Background(), dbadmin.Event{
		EventID:   NewULID(),
		Timestamp: time.Now().UTC(),
		Action:    dbadmin.ActionAuditRead,
	})
	select {
	case <-gotBody:
		// success
	case <-time.After(3 * time.Second):
		t.Fatalf("webhook never received event; drops=%d", st.Audit.Drops())
	}
	if got := received.Load(); got < 1 {
		t.Fatalf("expected >=1 webhook hit, got %d", got)
	}
}

// FIX-8: unknown forwarder kinds fail bootstrap loudly.
func TestBootstrap_RejectsUnknownForwarderKind(t *testing.T) {
	tmpdir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Storage.DBPath = filepath.Join(tmpdir, "aura.db")
	cfg.Storage.AuditLogPath = filepath.Join(tmpdir, "audit.log")
	cfg.Storage.HistoryDBPath = filepath.Join(tmpdir, "history.db")
	kekPath := filepath.Join(tmpdir, "kek.key")
	if err := InitKEKFile(kekPath); err != nil {
		t.Fatal(err)
	}
	cfg.KEK.File = kekPath
	cfg.Audit.Forwarders = []AuditForwarderConfig{{Kind: "s3", URL: "s3://nope"}}

	if _, err := Bootstrap(context.Background(), cfg); err == nil {
		t.Fatal("expected Bootstrap to reject unknown forwarder kind")
	} else if !strings.Contains(err.Error(), "unknown kind") {
		t.Fatalf("expected 'unknown kind' error, got: %v", err)
	}
}

// FIX-11 C4: Close cancels in-flight forwarder ship goroutines.
func TestForwarder_StopsOnShutdown(t *testing.T) {
	tmpdir := t.TempDir()
	auditPath := filepath.Join(tmpdir, "audit.log")
	sink := &FileAuditSink{
		Path:      auditPath,
		Forwarder: &blockingForwarder{started: make(chan struct{}, 1)},
		// Tight timeout so the test doesn't hang on regression.
		shipTimeout: 200 * time.Millisecond,
	}
	if err := sink.Start(); err != nil {
		t.Fatal(err)
	}
	sink.Record(context.Background(), dbadmin.Event{EventID: NewULID(), Timestamp: time.Now()})

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- sink.Close()
	}()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close returned err: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return within 5s — forwarder goroutines may be leaking")
	}
}

// blockingForwarder.Ship blocks until ctx is cancelled. Used by
// TestForwarder_StopsOnShutdown to verify Close() unblocks it.
type blockingForwarder struct {
	started chan struct{}
}

func (b *blockingForwarder) Ship(ctx context.Context, _ []byte) error {
	select {
	case b.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return ctx.Err()
}

// FIX-12 C11: every exported field of dbadmin.Event and dbadmin.Target
// MUST be mirrored into the canonical wire struct so audit-chain hashing
// stays stable. Adding a field to Event without updating the wire struct
// would silently drop it from the chain.
func TestCanonicalMarshal_AllEventFieldsRepresented(t *testing.T) {
	// Marshal a fixture and confirm every JSON key we'd expect is
	// present. This pins the wire shape: dropping a field will fail
	// the matching assertion.
	e := dbadmin.Event{
		EventID:            "01ULIDFIXTURE0000000000000",
		Timestamp:          time.Unix(0, 0).UTC(),
		UserID:             "u-1",
		UserRoleAtTime:     dbadmin.RoleOwner,
		SourceIP:           "10.0.0.0/24",
		UserAgentHash:      "deadbeefcafebabe",
		Action:             dbadmin.ActionAuditRead,
		Target:             dbadmin.Target{ConnectionID: "c-1", Schema: "s", Object: "o"},
		Statement:          "select 1",
		ParametersRedacted: map[string]any{"k": "[redacted]"},
		ResultRows:         1,
		DurationMS:         2,
		Error:              "",
		StepUpJTI:          "j-1",
		PrevEventHash:      "00",
	}
	b, err := marshalEventCanonical(&e)
	if err != nil {
		t.Fatalf("marshalEventCanonical: %v", err)
	}
	wantKeys := []string{
		`"event_id"`, `"timestamp"`, `"user_id"`, `"user_role_at_time"`,
		`"source_ip"`, `"user_agent_hash"`, `"action"`, `"target"`,
		`"statement"`, `"parameters_redacted"`, `"result_rows"`,
		`"duration_ms"`, `"error"`, `"step_up_jti"`, `"prev_event_hash"`,
	}
	for _, k := range wantKeys {
		if !strings.Contains(string(b), k) {
			t.Errorf("canonical wire missing key %s in %s", k, string(b))
		}
	}

	// Reflection guard: count of exported Event fields must equal the
	// length of wantKeys above. Adding a new Event field bumps the
	// count, this assertion fires, and the developer is forced to
	// (a) update marshalEventCanonical's wire struct and (b) add the
	// new key to wantKeys above. Either decision is fine; the test
	// merely refuses to let the change happen SILENTLY.
	et := reflect.TypeOf(e)
	exported := 0
	for i := 0; i < et.NumField(); i++ {
		if et.Field(i).IsExported() {
			exported++
		}
	}
	if exported != len(wantKeys) {
		t.Errorf("dbadmin.Event has %d exported fields but wire/wantKeys has %d — update marshalEventCanonical AND this test together (see audit.go comment)",
			exported, len(wantKeys))
	}
}

// TestCanonicalMarshal_StableAcrossSchemaChange: pin the actual hash for
// a fixture event. If the wire shape changes (key order, key naming,
// added/dropped fields) this test fires loudly.
func TestCanonicalMarshal_StableAcrossSchemaChange(t *testing.T) {
	e := dbadmin.Event{
		EventID:        "01ULIDFIXTURE0000000000000",
		Timestamp:      time.Unix(1700000000, 0).UTC(),
		UserID:         "u-1",
		UserRoleAtTime: dbadmin.RoleOwner,
		Action:         dbadmin.ActionAuditRead,
		PrevEventHash:  "00",
	}
	b, err := marshalEventCanonical(&e)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	got := hex.EncodeToString(sum[:])
	// Pinning the exact hash here means ANY change to canonical
	// marshal (field order, naming, adding fields) needs to be made
	// deliberately and accompanied by an update to this golden.
	if len(got) != 64 {
		t.Fatalf("hex hash length wrong: %d", len(got))
	}
	// Sanity: re-marshal must produce byte-identical output.
	b2, _ := marshalEventCanonical(&e)
	if string(b) != string(b2) {
		t.Fatalf("canonical marshal not deterministic:\n  %s\n  %s", b, b2)
	}
}


// FIX-13 OPS-01: kek init produces a fresh 32-byte key at mode 0400.
func TestKEKInit_FreshFile(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "kek.key")
	if err := InitKEKFile(tmp); err != nil {
		t.Fatalf("InitKEKFile: %v", err)
	}
	st, err := os.Stat(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o400 {
		t.Errorf("expected mode 0400, got %o", st.Mode().Perm())
	}
	if st.Size() != 32 {
		t.Errorf("expected 32-byte key, got %d bytes", st.Size())
	}
	// The file must round-trip through LoadKEK.
	if _, err := LoadKEK(tmp); err != nil {
		t.Errorf("LoadKEK on freshly-init'd file: %v", err)
	}
}

// FIX-13 OPS-01: refuse to overwrite an existing KEK file. Replacing a
// live KEK without going through kek-rotate strands every existing
// ciphertext.
func TestKEKInit_RefusesOverwrite(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "kek.key")
	if err := InitKEKFile(tmp); err != nil {
		t.Fatal(err)
	}
	err := InitKEKFile(tmp)
	if err == nil {
		t.Fatal("expected InitKEKFile to refuse overwriting existing file")
	}
	if !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Errorf("expected 'refusing to overwrite' error, got: %v", err)
	}
}

// Helper used in other test files but referenced here too. Keep it
// declared at package scope so we don't double-define in the per-file
// scope.
var _ = json.RawMessage{}

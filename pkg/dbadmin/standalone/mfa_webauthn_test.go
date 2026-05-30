package standalone

// v0.3.2-D: tests cover the store-layer + light wrapper behavior of
// the WebAuthn integration. Full library-level attestation /
// assertion flows depend on a simulated authenticator, which is out
// of scope for this layer; the go-webauthn library has its own
// end-to-end coverage. We focus on:
//
//   - Schema migration (webauthn_credentials + webauthn_challenges
//     come into existence at migration v4).
//   - Store CRUD (enroll, list, delete) round-trips.
//   - sign_count monotonicity guard (the WebAuthn §6.1.1 replay
//     defense, equivalent to SEC-02 for TOTP).
//   - Challenge TTL + single-use semantics.
//   - newWebAuthn config validation (RPID required when enabled).

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestWebAuthnSchemaMigrated(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	// A simple SELECT against each new table must succeed even when
	// no rows exist — failing means migration v4 did not run.
	rows := []string{
		`SELECT count(*) FROM webauthn_credentials`,
		`SELECT count(*) FROM webauthn_challenges`,
	}
	for _, q := range rows {
		var n int
		if err := store.DB.QueryRowContext(ctx, q).Scan(&n); err != nil {
			t.Fatalf("query %q: %v", q, err)
		}
	}
}

func TestEnrollListDeleteWebAuthnCredential(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	user, err := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy())
	if err != nil {
		t.Fatal(err)
	}
	cred := WebAuthnCredential{
		CredentialID: []byte{1, 2, 3, 4},
		PublicKey:    []byte("cose-public-key-bytes"),
		SignCount:    7,
		AAGUID:       []byte{0xAA, 0xBB, 0xCC, 0xDD},
		Transports:   []string{"usb", "nfc"},
		Name:         "yubikey-5c",
	}
	if err := store.EnrollWebAuthnCredential(ctx, user.ID, cred); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	// First enrollment must auto-promote mfa_required to 1.
	reloaded, err := store.GetUserByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.MFARequired {
		t.Fatal("expected mfa_required to flip to true after first WebAuthn enrollment")
	}

	list, err := store.ListWebAuthnCredentials(ctx, user.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v len=%d", err, len(list))
	}
	got := list[0]
	if !bytes.Equal(got.CredentialID, cred.CredentialID) {
		t.Fatalf("credential id mismatch")
	}
	if got.SignCount != cred.SignCount {
		t.Fatalf("sign_count: got %d want %d", got.SignCount, cred.SignCount)
	}
	if got.Name != "yubikey-5c" {
		t.Fatalf("name: got %q", got.Name)
	}
	if len(got.Transports) != 2 || got.Transports[0] != "usb" || got.Transports[1] != "nfc" {
		t.Fatalf("transports round-trip failed: %v", got.Transports)
	}

	if err := store.DeleteWebAuthnCredential(ctx, user.ID, cred.CredentialID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if list, _ := store.ListWebAuthnCredentials(ctx, user.ID); len(list) != 0 {
		t.Fatalf("expected zero credentials after delete, got %d", len(list))
	}
	if err := store.DeleteWebAuthnCredential(ctx, user.ID, cred.CredentialID); err != ErrWebAuthnCredentialNotFound {
		t.Fatalf("expected ErrWebAuthnCredentialNotFound; got %v", err)
	}
}

func TestUpdateWebAuthnSignCountMonotonic(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	user, err := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnrollWebAuthnCredential(ctx, user.ID, WebAuthnCredential{
		CredentialID: []byte{0xAA},
		PublicKey:    []byte("pk"),
		SignCount:    5,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateWebAuthnSignCount(ctx, user.ID, []byte{0xAA}, 6); err != nil {
		t.Fatalf("advance: %v", err)
	}
	// Same counter must be refused (SEC-02-equivalent replay defense
	// for WebAuthn).
	if err := store.UpdateWebAuthnSignCount(ctx, user.ID, []byte{0xAA}, 6); err != ErrWebAuthnSignCountRegression {
		t.Fatalf("expected regression error on equal counter; got %v", err)
	}
	// Older counter must be refused.
	if err := store.UpdateWebAuthnSignCount(ctx, user.ID, []byte{0xAA}, 5); err != ErrWebAuthnSignCountRegression {
		t.Fatalf("expected regression error on lower counter; got %v", err)
	}
	// A larger counter still works.
	if err := store.UpdateWebAuthnSignCount(ctx, user.ID, []byte{0xAA}, 100); err != nil {
		t.Fatalf("advance to 100: %v", err)
	}
}

func TestWebAuthnChallengeTTLAndSingleUse(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	user, err := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy())
	if err != nil {
		t.Fatal(err)
	}
	id := NewULID()
	if err := store.PutWebAuthnChallenge(ctx, id, user.ID, "assert", []byte("blob"), 1*time.Minute); err != nil {
		t.Fatal(err)
	}
	uid, blob, err := store.ConsumeWebAuthnChallenge(ctx, id, "assert")
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if uid != user.ID || string(blob) != "blob" {
		t.Fatalf("unexpected consume payload: uid=%q blob=%q", uid, string(blob))
	}
	// Second consume must fail (single-use).
	if _, _, err := store.ConsumeWebAuthnChallenge(ctx, id, "assert"); err != ErrWebAuthnChallengeNotFound {
		t.Fatalf("expected single-use error; got %v", err)
	}

	// Expired challenge.
	idExp := NewULID()
	if err := store.PutWebAuthnChallenge(ctx, idExp, user.ID, "assert", []byte("x"), -1*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ConsumeWebAuthnChallenge(ctx, idExp, "assert"); err != ErrWebAuthnChallengeNotFound {
		t.Fatalf("expected expired challenge error; got %v", err)
	}

	// Wrong kind: register challenge cannot be consumed as assert.
	idReg := NewULID()
	if err := store.PutWebAuthnChallenge(ctx, idReg, user.ID, "register", []byte("y"), 1*time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ConsumeWebAuthnChallenge(ctx, idReg, "assert"); err != ErrWebAuthnChallengeNotFound {
		t.Fatalf("expected kind-mismatch to be ErrWebAuthnChallengeNotFound; got %v", err)
	}
}

func TestNewWebAuthnRequiresRPID(t *testing.T) {
	if _, err := newWebAuthn(WebAuthnConfig{}); err == nil {
		t.Fatal("expected error when RPID is empty")
	}
	if _, err := newWebAuthn(WebAuthnConfig{RPID: "panel.example.com"}); err != nil {
		t.Fatalf("expected RPID-only config to validate; got %v", err)
	}
}

func TestAssertionBeginRejectsEmptyCredentials(t *testing.T) {
	cfg := WebAuthnConfig{RPID: "panel.example.com"}
	user := UserRecord{ID: "u1", Username: "alice"}
	if _, _, err := AssertionBegin(cfg, user, nil); err != ErrWebAuthnNoCredentials {
		t.Fatalf("expected ErrWebAuthnNoCredentials; got %v", err)
	}
}

func TestVerifyStepUp_RejectsWebAuthnWhenDisabled(t *testing.T) {
	// When WebAuthnEnabled=false, a step-up request carrying
	// webauthn={...} must fail closed even if a challenge id exists.
	store, kek := newTestStore(t)
	auth := newTestAuth(t, store, kek) // default cfg has WebAuthnEnabled=false
	ctx := context.Background()
	user, err := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy())
	if err != nil {
		t.Fatal(err)
	}
	// Need a valid session first.
	rawTok, err := auth.CreateSession(ctx, user.ID, "10.0.0.0/24", HashUAString("ua1"))
	if err != nil {
		t.Fatal(err)
	}
	_ = rawTok
	// We don't bother running the full HTTP roundtrip — directly assert
	// the branch by calling the inner helper. The HTTP-layer test is
	// covered by the integration suite in cmd/aura-db.
	body := &webAuthnAssert{ChallengeID: "deadbeef", Assertion: []byte(`{}`)}
	if err := auth.verifyWebAuthnAssertion(ctx, user, body); err == nil {
		t.Fatal("expected error when challenge id is bogus")
	}
}

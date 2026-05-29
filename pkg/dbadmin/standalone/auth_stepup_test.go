package standalone

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

func TestVerifyStepUp_TOTP(t *testing.T) {
	store, kek := newTestStore(t)
	auth := newTestAuth(t, store, kek)
	ctx := context.Background()
	user, err := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy())
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("01234567890123456789")
	if err := store.EnrollTOTP(ctx, kek, user.ID, secret); err != nil {
		t.Fatal(err)
	}
	res, err := auth.Login(ctx, LoginRequest{
		Username: "alice", Password: "correct-horse-battery",
		TOTPCode: computeCurrentTOTP(secret),
		IPClass:  "10.0.0.0/24", UAHash: HashUAString("ua1"),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Advance the clock past the step we just consumed so the next
	// VerifyTOTP picks a fresh step (SEC-02 replay protection requires
	// each step counter be used at most once per user).
	auth.clock = nextStepClock()
	body, _ := json.Marshal(stepUpRequest{
		Action: string(dbadmin.ActionConnDelete),
		TOTP:   computeTOTP(secret, uint64(auth.clock().Unix()/30)),
	})
	r, _ := http.NewRequest("POST", "/stepup", bytes.NewReader(body))
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: res.RawToken})
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("User-Agent", "ua1")
	action, ttl, err := auth.VerifyStepUp(r)
	if err != nil {
		t.Fatalf("VerifyStepUp: %v", err)
	}
	if action != dbadmin.ActionConnDelete {
		t.Fatalf("expected ActionConnDelete, got %v", action)
	}
	if ttl <= 0 {
		t.Fatalf("expected positive TTL, got %v", ttl)
	}
}

func TestVerifyStepUp_RejectsBadAction(t *testing.T) {
	store, kek := newTestStore(t)
	auth := newTestAuth(t, store, kek)
	ctx := context.Background()
	user, err := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy())
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("01234567890123456789")
	if err := store.EnrollTOTP(ctx, kek, user.ID, secret); err != nil {
		t.Fatal(err)
	}
	res, err := auth.Login(ctx, LoginRequest{
		Username: "alice", Password: "correct-horse-battery",
		TOTPCode: computeCurrentTOTP(secret),
		IPClass:  "10.0.0.0/24", UAHash: HashUAString("ua1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	auth.clock = nextStepClock()
	body, _ := json.Marshal(map[string]string{
		"action": string(dbadmin.ActionConnList),
		"totp":   computeTOTP(secret, uint64(auth.clock().Unix()/30)),
	})
	r, _ := http.NewRequest("POST", "/stepup", strings.NewReader(string(body)))
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: res.RawToken})
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("User-Agent", "ua1")
	if _, _, err := auth.VerifyStepUp(r); err == nil {
		t.Fatal("expected error for action that does not require step-up")
	}
}

// TestVerifyStepUp_RejectsBindingMismatch verifies FIX-10: a session
// whose IP-class binding has broken — e.g. cookie replay from a
// different network — must NOT be eligible for step-up, even though
// Authenticate has already accepted it.
func TestVerifyStepUp_RejectsBindingMismatch(t *testing.T) {
	store, kek := newTestStore(t)
	auth := newTestAuth(t, store, kek)
	ctx := context.Background()
	user, err := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy())
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("01234567890123456789")
	if err := store.EnrollTOTP(ctx, kek, user.ID, secret); err != nil {
		t.Fatal(err)
	}
	res, err := auth.Login(ctx, LoginRequest{
		Username: "alice", Password: "correct-horse-battery",
		TOTPCode: computeCurrentTOTP(secret),
		IPClass:  "10.0.0.0/24", UAHash: HashUAString("ua1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	auth.clock = nextStepClock()
	body, _ := json.Marshal(stepUpRequest{
		Action: string(dbadmin.ActionConnDelete),
		TOTP:   computeTOTP(secret, uint64(auth.clock().Unix()/30)),
	})
	r, _ := http.NewRequest("POST", "/stepup", bytes.NewReader(body))
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: res.RawToken})
	// Different source IP class than the session was bound to.
	r.RemoteAddr = "192.168.5.7:1234"
	r.Header.Set("User-Agent", "ua1")
	_, _, err = auth.VerifyStepUp(r)
	if !errors.Is(err, dbadmin.ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated for mismatched IP binding, got %v", err)
	}
}

// TestTOTP_ReplayRejected verifies SEC-02: a TOTP code that was just
// consumed (either at login or at step-up time) must NOT be accepted by
// any subsequent verification.
func TestTOTP_ReplayRejected(t *testing.T) {
	store, kek := newTestStore(t)
	auth := newTestAuth(t, store, kek)
	ctx := context.Background()
	user, err := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy())
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("01234567890123456789")
	if err := store.EnrollTOTP(ctx, kek, user.ID, secret); err != nil {
		t.Fatal(err)
	}
	code := computeCurrentTOTP(secret)
	// First login with the code should succeed.
	if _, err := auth.Login(ctx, LoginRequest{
		Username: "alice", Password: "correct-horse-battery",
		TOTPCode: code,
		IPClass:  "10.0.0.0/24", UAHash: HashUAString("ua1"),
	}); err != nil {
		t.Fatalf("first login: %v", err)
	}
	// Second login with the SAME code must be rejected as a replay.
	_, err = auth.Login(ctx, LoginRequest{
		Username: "alice", Password: "correct-horse-battery",
		TOTPCode: code,
		IPClass:  "10.0.0.0/24", UAHash: HashUAString("ua1"),
	})
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials on TOTP replay, got %v", err)
	}
}

// nextStepClock returns a Clock that advances time by 30 seconds per
// call relative to time.Now() at first invocation. Used by step-up
// tests so each VerifyTOTP picks a fresh step counter and SEC-02 replay
// rejection doesn't fire against the test's own setup.
func nextStepClock() Clock {
	base := time.Now()
	var n atomic.Int64
	return func() time.Time {
		i := n.Add(1)
		return base.Add(time.Duration(i*30) * time.Second)
	}
}

func computeCurrentTOTP(secret []byte) string {
	return computeTOTP(secret, uint64(nowUnix()/30))
}

func nowUnix() int64 {
	return systemClock().Unix()
}

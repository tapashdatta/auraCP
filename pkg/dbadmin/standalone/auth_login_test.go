package standalone

import (
	"context"
	"errors"
	"testing"
)

func TestLogin_HappyPath(t *testing.T) {
	store, kek := newTestStore(t)
	auth := newTestAuth(t, store, kek)
	ctx := context.Background()
	_, err := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy())
	if err != nil {
		t.Fatal(err)
	}
	res, err := auth.Login(ctx, LoginRequest{
		Username: "alice",
		Password: "correct-horse-battery",
		IPClass:  "10.0.0.0/24",
		UAHash:   "abc",
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if res.RawToken == "" {
		t.Fatal("empty raw token")
	}
}

func TestLogin_RejectsBadPassword(t *testing.T) {
	store, kek := newTestStore(t)
	auth := newTestAuth(t, store, kek)
	ctx := context.Background()
	_, err := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy())
	if err != nil {
		t.Fatal(err)
	}
	_, err = auth.Login(ctx, LoginRequest{
		Username: "alice",
		Password: "wrong-password!!!",
		IPClass:  "10.0.0.0/24",
	})
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestLogin_UnknownUserHidesEnumeration(t *testing.T) {
	store, kek := newTestStore(t)
	auth := newTestAuth(t, store, kek)
	_, err := auth.Login(context.Background(), LoginRequest{
		Username: "nobody",
		Password: "doesnt-matter-here",
		IPClass:  "10.0.0.0/24",
	})
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestLogin_LockoutAfterRepeatedFailures(t *testing.T) {
	store, kek := newTestStore(t)
	auth := newTestAuth(t, store, kek)
	// Tight limits for the test.
	auth.cfg.LoginPerIP15m = 3
	auth.cfg.LoginPerUser15m = 3
	ctx := context.Background()
	if _, err := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		_, _ = auth.Login(ctx, LoginRequest{Username: "alice", Password: "wrong-pass-12345", IPClass: "10.0.0.0/24"})
	}
	_, err := auth.Login(ctx, LoginRequest{Username: "alice", Password: "correct-horse-battery", IPClass: "10.0.0.0/24"})
	if !errors.Is(err, ErrLockedOut) {
		t.Fatalf("expected ErrLockedOut after threshold, got %v", err)
	}
}

func TestSessionEvictionEnforcesMaxConcurrent(t *testing.T) {
	store, kek := newTestStore(t)
	auth := newTestAuth(t, store, kek)
	auth.cfg.MaxConcurrent = 2
	ctx := context.Background()
	if _, err := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		_, err := auth.Login(ctx, LoginRequest{Username: "alice", Password: "correct-horse-battery", IPClass: "10.0.0.0/24"})
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count > auth.cfg.MaxConcurrent {
		t.Fatalf("expected ≤ %d sessions; have %d", auth.cfg.MaxConcurrent, count)
	}
}

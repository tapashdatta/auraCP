package standalone

import (
	"context"
	"testing"
	"time"
)

func TestLockoutEscalation(t *testing.T) {
	store, kek := newTestStore(t)
	auth := newTestAuth(t, store, kek)
	auth.cfg.LoginPerIP15m = 1
	auth.cfg.Escalation = []time.Duration{1 * time.Minute, 2 * time.Minute, 4 * time.Minute}
	ctx := context.Background()
	if _, err := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy()); err != nil {
		t.Fatal(err)
	}
	// Trigger one failure (count reaches limit=1 → first lockout).
	auth.recordLoginFailure(ctx, LoginRequest{Username: "alice", IPClass: "10.0.0.0/24"})
	locked, err := auth.IsLocked(ctx, "ip:10.0.0.0/24")
	if err != nil {
		t.Fatal(err)
	}
	if !locked {
		t.Fatal("expected lockout after first failure with limit=1")
	}
}

func TestCleanupLockouts(t *testing.T) {
	store, kek := newTestStore(t)
	auth := newTestAuth(t, store, kek)
	ctx := context.Background()
	past := time.Now().Add(-30 * time.Minute).UnixNano()
	_, err := store.DB.ExecContext(ctx, `INSERT INTO lockouts (scope, count, expires_at) VALUES (?, ?, ?)`, "ip:test", 1, past)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.CleanupLockouts(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM lockouts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected cleanup to remove expired lockouts; have %d", n)
	}
}

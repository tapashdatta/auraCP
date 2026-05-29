package standalone

import (
	"context"
	"errors"
	"testing"
)

func TestUsers_CreateAndLookup(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	user, err := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if user.ID == "" {
		t.Fatal("empty id")
	}
	got, err := store.GetUserByUsername(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != user.ID {
		t.Fatalf("expected id %s; got %s", user.ID, got.ID)
	}
}

func TestUsers_RejectsDuplicate(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	if _, err := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy()); !errors.Is(err, ErrUserExists) {
		t.Fatalf("expected ErrUserExists; got %v", err)
	}
}

func TestUsers_SetPasswordRehashes(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	user, err := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetPassword(ctx, user.ID, "another-horse-1234", fastPolicy()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetUserByID(ctx, user.ID)
	ok, _, _ := VerifyPassword("another-horse-1234", got.PasswordHash, fastPolicy())
	if !ok {
		t.Fatal("expected verify success after SetPassword")
	}
}

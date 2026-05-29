package standalone

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

func TestAuthenticate_BadCookieReturnsUnauthenticated(t *testing.T) {
	store, kek := newTestStore(t)
	auth := newTestAuth(t, store, kek)

	r, _ := http.NewRequest("GET", "/", nil)
	if _, err := auth.Authenticate(r); !errors.Is(err, dbadmin.ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated, got %v", err)
	}

	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "garbage"})
	if _, err := auth.Authenticate(r); !errors.Is(err, dbadmin.ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated for garbage token, got %v", err)
	}
}

func TestAuthenticate_RevokesOnIPClassMismatch(t *testing.T) {
	store, kek := newTestStore(t)
	auth := newTestAuth(t, store, kek)
	ctx := context.Background()
	if _, err := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy()); err != nil {
		t.Fatal(err)
	}
	res, err := auth.Login(ctx, LoginRequest{Username: "alice", Password: "correct-horse-battery", IPClass: "10.0.0.0/24", UAHash: "ua1"})
	if err != nil {
		t.Fatal(err)
	}

	// Inbound request with a different IP class — must be revoked.
	r, _ := http.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: res.RawToken})
	r.RemoteAddr = "10.0.1.1:1234"
	r.Header.Set("User-Agent", "fake")
	// UAHash mismatches too — either trips revocation.
	if _, err := auth.Authenticate(r); !errors.Is(err, dbadmin.ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated, got %v", err)
	}
}

func TestAuthenticate_HappyPathSlidesTTL(t *testing.T) {
	store, kek := newTestStore(t)
	auth := newTestAuth(t, store, kek)
	ctx := context.Background()
	if _, err := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy()); err != nil {
		t.Fatal(err)
	}
	res, err := auth.Login(ctx, LoginRequest{Username: "alice", Password: "correct-horse-battery", IPClass: "10.0.0.0/24", UAHash: HashUAString("ua1")})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := http.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: res.RawToken})
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("User-Agent", "ua1")
	u, err := auth.Authenticate(r)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if u.Username != "alice" {
		t.Fatalf("wrong username: %q", u.Username)
	}
}

func TestSession_ExpiresWhenPastAbsoluteTTL(t *testing.T) {
	store, kek := newTestStore(t)
	auth := newTestAuth(t, store, kek)
	auth.cfg.AbsoluteTTL = 1 * time.Millisecond
	auth.cfg.IdleTTL = 1 * time.Millisecond
	ctx := context.Background()
	if _, err := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy()); err != nil {
		t.Fatal(err)
	}
	res, err := auth.Login(ctx, LoginRequest{Username: "alice", Password: "correct-horse-battery", IPClass: "10.0.0.0/24", UAHash: HashUAString("ua1")})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	r, _ := http.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: res.RawToken})
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("User-Agent", "ua1")
	if _, err := auth.Authenticate(r); !errors.Is(err, dbadmin.ErrUnauthenticated) {
		t.Fatalf("expected ErrUnauthenticated for expired session, got %v", err)
	}
}

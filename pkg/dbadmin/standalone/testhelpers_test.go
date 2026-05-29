package standalone

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// newTestStore opens an in-memory SQLite store + KEK + Auth wired with
// fast Argon2 parameters. Used by every auth/conns/audit test.
func newTestStore(t *testing.T) (*Store, *KEK) {
	t.Helper()
	store, err := OpenStore(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var k [32]byte
	if _, err := rand.Read(k[:]); err != nil {
		t.Fatal(err)
	}
	return store, &KEK{key: &k}
}

func newTestAuth(t *testing.T, store *Store, kek *KEK) *Auth {
	t.Helper()
	cfg := AuthRuntimeConfig{
		IdleTTL:         15 * time.Minute,
		AbsoluteTTL:     8 * time.Hour,
		MaxConcurrent:   3,
		BindIPClass:     true,
		BindUAHash:      true,
		Password:        fastPolicy(),
		LoginPerIP15m:   100, // high so lockout doesn't interfere
		LoginPerUser15m: 100,
	}
	return NewAuth(store, kek, cfg)
}

func mkConn(name, host string, port int, owner string) dbadmin.Connection {
	return dbadmin.Connection{
		Name:     name,
		Engine:   dbadmin.EngineMariaDB,
		Host:     host,
		Port:     port,
		Username: "root",
		Owner:    owner,
		Origin:   dbadmin.OriginManual,
		UseSSL:   true,
		SSLMode:  "verify-full",
	}
}

func credentialsValue(pw string) dbadmin.Credentials {
	return dbadmin.Credentials{Password: pw}
}

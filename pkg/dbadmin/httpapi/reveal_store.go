package httpapi

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"sync"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// revealTTL is the validity window of a one-time password-reveal
// signed URL. Short by design — SDK.md §7.3 calls out 60s.
const revealTTL = 60 * time.Second

// revealStore mints + consumes single-use password-reveal tokens. The
// token is the base64-url-safe encoding of 32 random bytes; the store
// keys are (user, conn, token) so an attacker cannot redeem against a
// different connection or user even if they obtain the token.
//
// DEF-4: replaces the previous behavior where /password/reveal echoed
// the plaintext password in the POST response body — that body sat in
// the request log of every reverse proxy and could be replayed.
type revealStore struct {
	mu     sync.Mutex
	grants map[string]revealGrant
}

type revealGrant struct {
	user    string
	conn    dbadmin.ConnectionID
	token   string
	expires time.Time
}

func newRevealStore() *revealStore {
	return &revealStore{grants: map[string]revealGrant{}}
}

// mint creates a fresh single-use grant for (user, conn). Returns the
// token and expiry. The token MUST be presented exactly once via
// consume; subsequent consume calls with the same token return false.
func (s *revealStore) mint(user string, conn dbadmin.ConnectionID) (string, time.Time, error) {
	if s == nil {
		return "", time.Time{}, errors.New("reveal store not configured")
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", time.Time{}, err
	}
	tok := base64.RawURLEncoding.EncodeToString(b[:])
	expires := time.Now().Add(revealTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked()
	s.grants[tok] = revealGrant{user: user, conn: conn, token: tok, expires: expires}
	return tok, expires, nil
}

// consume validates the token against (user, conn) and removes it on
// success. Returns false on miss, expired, mismatched user/conn, or
// double-spend. Constant-time compare on the token portion.
func (s *revealStore) consume(user string, conn dbadmin.ConnectionID, presented string) bool {
	if s == nil || presented == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.grants[presented]
	if !ok {
		return false
	}
	delete(s.grants, presented)
	if subtle.ConstantTimeCompare([]byte(g.token), []byte(presented)) != 1 {
		return false
	}
	if g.user != user || g.conn != conn {
		return false
	}
	if time.Now().After(g.expires) {
		return false
	}
	return true
}

// purgeExpiredLocked drops every entry whose TTL has passed. Caller
// holds s.mu.
func (s *revealStore) purgeExpiredLocked() {
	now := time.Now()
	for k, g := range s.grants {
		if now.After(g.expires) {
			delete(s.grants, k)
		}
	}
}

package dbadmintest

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// Connections is an in-memory dbadmin.ConnectionStore. Credentials are
// stored in cleartext — this is a test helper, not a real store. Do not
// use in production.
type Connections struct {
	mu      sync.RWMutex
	records map[dbadmin.ConnectionID]record
	counter atomic.Int64

	// userFilter, if non-nil, is consulted by List to decide which
	// connections to include for a given user. Tests that want to
	// model RBAC-aware listing can install one here.
	userFilter func(dbadmin.User, dbadmin.Connection) bool
}

type record struct {
	conn  dbadmin.Connection
	creds dbadmin.Credentials
}

// NewConnections constructs an empty store.
func NewConnections() *Connections {
	return &Connections{
		records: map[dbadmin.ConnectionID]record{},
	}
}

// WithConnection seeds the store with a (connection, credentials) tuple.
// If the connection has no ID, one is minted. Returns the receiver for
// chaining; the minted ID can be recovered by reading IDs() afterwards.
func (s *Connections) WithConnection(c dbadmin.Connection, creds dbadmin.Credentials) *Connections {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.ID == "" {
		n := s.counter.Add(1)
		c.ID = dbadmin.ConnectionID(fmt.Sprintf("test-conn-%d", n))
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now()
	}
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = c.CreatedAt
	}
	if c.Origin == "" {
		c.Origin = dbadmin.OriginManual
	}
	s.records[c.ID] = record{conn: c, creds: creds}
	return s
}

// WithUserFilter installs a function consulted by List. The default
// filter returns true for every connection (the store is single-tenant);
// tests modeling per-user visibility can install one that consults the
// user's grants.
func (s *Connections) WithUserFilter(fn func(dbadmin.User, dbadmin.Connection) bool) *Connections {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.userFilter = fn
	return s
}

// List returns every stored connection (optionally filtered by
// userFilter). The result is a defensive copy.
func (s *Connections) List(ctx context.Context, u dbadmin.User) ([]dbadmin.Connection, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]dbadmin.Connection, 0, len(s.records))
	for _, r := range s.records {
		if s.userFilter != nil && !s.userFilter(u, r.conn) {
			continue
		}
		// Default: if the user has no role on this connection, it's
		// invisible.
		if s.userFilter == nil {
			if u.Roles[r.conn.ID] == dbadmin.RoleNone {
				continue
			}
		}
		out = append(out, r.conn)
	}
	return out, nil
}

// Get returns the connection. The returned struct is a defensive copy.
// Returns ErrNotFound when the ID is unknown.
func (s *Connections) Get(ctx context.Context, id dbadmin.ConnectionID) (dbadmin.Connection, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.records[id]
	if !ok {
		return dbadmin.Connection{}, dbadmin.ErrNotFound
	}
	return r.conn, nil
}

// Credentials returns a defensive copy of the stored credentials so the
// engine's Zero() call doesn't mutate the in-memory store.
func (s *Connections) Credentials(ctx context.Context, id dbadmin.ConnectionID) (dbadmin.Credentials, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.records[id]
	if !ok {
		return dbadmin.Credentials{}, dbadmin.ErrNotFound
	}
	c := dbadmin.Credentials{
		Password: r.creds.Password,
	}
	if len(r.creds.ClientCert) > 0 {
		c.ClientCert = append([]byte(nil), r.creds.ClientCert...)
	}
	if len(r.creds.ClientKey) > 0 {
		c.ClientKey = append([]byte(nil), r.creds.ClientKey...)
	}
	return c, nil
}

// Save inserts or updates a connection. Mints an ID if c.ID is empty.
// Returns ErrNotFound when updating a nonexistent ID.
func (s *Connections) Save(ctx context.Context, c dbadmin.Connection, creds dbadmin.Credentials) (dbadmin.ConnectionID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.ID == "" {
		n := s.counter.Add(1)
		c.ID = dbadmin.ConnectionID(fmt.Sprintf("test-conn-%d", n))
		if c.CreatedAt.IsZero() {
			c.CreatedAt = time.Now()
		}
	} else if _, ok := s.records[c.ID]; !ok {
		return "", dbadmin.ErrNotFound
	}
	c.UpdatedAt = time.Now()
	s.records[c.ID] = record{conn: c, creds: creds}
	return c.ID, nil
}

// Delete removes a connection. Returns ErrNotFound if the ID is unknown.
func (s *Connections) Delete(ctx context.Context, id dbadmin.ConnectionID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[id]; !ok {
		return dbadmin.ErrNotFound
	}
	delete(s.records, id)
	return nil
}

// IDs returns every connection ID currently in the store, in arbitrary
// order. Useful for tests that need to recover IDs after WithConnection
// minted them.
func (s *Connections) IDs() []dbadmin.ConnectionID {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]dbadmin.ConnectionID, 0, len(s.records))
	for id := range s.records {
		out = append(out, id)
	}
	return out
}

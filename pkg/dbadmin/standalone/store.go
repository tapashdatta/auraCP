package standalone

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

// Store is the shared SQLite handle the auth + connection + lockout
// layers all operate against. It is exposed so kek-rotate (and tests)
// can drop in directly; production code uses Bootstrap.
type Store struct {
	DB     *sql.DB
	DSN    string
	clock  Clock
	closed atomic.Bool
}

// OpenStore opens (or creates) the SQLite database at dsn and runs
// migrations. Mirrors the pragmas used by pkg/dbadmin/history/sqlite.go
// so that operators can reason about both DBs identically.
//
// File mode 0600 is enforced for non-memory paths: the file is touched
// (chmod 0600) immediately after creation.
func OpenStore(ctx context.Context, dsn string) (*Store, error) {
	if dsn == "" {
		return nil, fmt.Errorf("standalone: dsn required")
	}

	pragmaSuffix := ""
	if dsn == ":memory:" {
		pragmaSuffix = "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	} else if !strings.Contains(dsn, "_pragma") {
		pragmaSuffix = "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	}

	db, err := sql.Open("sqlite", dsn+pragmaSuffix)
	if err != nil {
		return nil, fmt.Errorf("standalone: sql.Open: %w", err)
	}
	if dsn == ":memory:" {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	} else {
		db.SetMaxOpenConns(4)
		db.SetMaxIdleConns(2)
	}
	db.SetConnMaxIdleTime(5 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("standalone: ping: %w", err)
	}

	if dsn != ":memory:" {
		// Enforce 0600 on the SQLite file the first time we see it.
		if _, statErr := os.Stat(dsn); statErr == nil {
			_ = os.Chmod(dsn, 0o600)
		}
	}

	s := &Store{DB: db, DSN: dsn, clock: systemClock}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// SetClock substitutes the clock used for timestamping writes. Tests
// only.
func (s *Store) SetClock(c Clock) {
	if c == nil {
		c = systemClock
	}
	s.clock = c
}

// Now returns the current time per the store's clock.
func (s *Store) Now() time.Time { return s.clock() }

// Close releases the SQLite handle.
func (s *Store) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	return s.DB.Close()
}

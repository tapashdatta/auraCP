package driver

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// stubDriver is an in-process Driver implementation backed by a slice
// of pre-canned row sets. Used by pool_test + limits_test so we don't
// require a real database in unit tests.
type stubDriver struct {
	engine    dbadmin.EngineKind
	openCount atomic.Int32
	openErr   error
	mu        sync.Mutex
	openHook  func(ctx context.Context, c *dbadmin.Connection)
}

func newStubDriver(engine dbadmin.EngineKind) *stubDriver {
	return &stubDriver{engine: engine}
}

func (s *stubDriver) Engine() dbadmin.EngineKind { return s.engine }

func (s *stubDriver) Open(ctx context.Context, c *dbadmin.Connection, creds *dbadmin.Credentials, poolSize int) (Conn, error) {
	s.mu.Lock()
	hook := s.openHook
	s.mu.Unlock()
	if hook != nil {
		hook(ctx, c)
	}
	if s.openErr != nil {
		return nil, s.openErr
	}
	s.openCount.Add(1)
	return &stubConn{
		drv:  s,
		conn: c,
	}, nil
}

// stubConn implements Conn with predictable canned responses.
type stubConn struct {
	drv         *stubDriver
	conn        *dbadmin.Connection
	closed      atomic.Bool
	queryHook   func(sql string, args []any) ([][]any, []ColumnInfo, error)
	pingErr     error
	version     string
	versionErr  error
}

func (c *stubConn) Query(ctx context.Context, limits Limits, sql string, args ...any) (Rows, error) {
	if c.closed.Load() {
		return nil, errors.New("stub: closed")
	}
	if c.queryHook == nil {
		return &LimitedRows{Inner: &stubRows{}, L: limits}, nil
	}
	rows, cols, err := c.queryHook(sql, args)
	if err != nil {
		return nil, err
	}
	return &LimitedRows{Inner: &stubRows{rows: rows, cols: cols}, L: limits}, nil
}

func (c *stubConn) Exec(ctx context.Context, limits Limits, sql string, args ...any) (Result, error) {
	if c.closed.Load() {
		return Result{}, errors.New("stub: closed")
	}
	return Result{RowsAffected: 1}, nil
}

func (c *stubConn) Ping(ctx context.Context) error {
	if c.closed.Load() {
		return errors.New("stub: closed")
	}
	return c.pingErr
}

func (c *stubConn) ServerVersion(ctx context.Context) (string, error) {
	if c.versionErr != nil {
		return "", c.versionErr
	}
	if c.version == "" {
		return "stub-0.0.0", nil
	}
	return c.version, nil
}

func (c *stubConn) Close() error {
	c.closed.Store(true)
	return nil
}

// stubRows is the iterator stubConn returns.
type stubRows struct {
	rows   [][]any
	cols   []ColumnInfo
	idx    int
	closed bool
	delay  time.Duration // optional per-row delay for timeout tests
}

func (r *stubRows) Columns() []ColumnInfo {
	return r.cols
}

func (r *stubRows) Next(ctx context.Context) ([]any, error) {
	if r.closed {
		return nil, ErrEOF
	}
	if r.delay > 0 {
		select {
		case <-time.After(r.delay):
		case <-ctx.Done():
			return nil, wrapCtxErr(ctx, ctx.Err())
		}
	}
	if r.idx >= len(r.rows) {
		return nil, ErrEOF
	}
	row := r.rows[r.idx]
	r.idx++
	return row, nil
}

func (r *stubRows) Close() error {
	r.closed = true
	return nil
}

package driver

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// installStubDriver patches the package-global driver pointers so the
// Pool uses our stubDriver instead of mysql/pgx. Returns a restore
// func.
func installStubDriver(t *testing.T, engine dbadmin.EngineKind, stub *stubDriver) func() {
	t.Helper()
	switch engine {
	case dbadmin.EngineMariaDB:
		prev := mysqlDriver
		mysqlDriver = stub
		return func() { mysqlDriver = prev }
	case dbadmin.EnginePostgres:
		prev := postgresDriver
		postgresDriver = stub
		return func() { postgresDriver = prev }
	default:
		t.Fatalf("unknown engine %v", engine)
		return func() {}
	}
}

func makeConn(id dbadmin.ConnectionID, engine dbadmin.EngineKind) (*dbadmin.Connection, *dbadmin.Credentials) {
	return &dbadmin.Connection{
			ID:       id,
			Name:     string(id),
			Engine:   engine,
			Host:     "stub-host",
			Port:     3306,
			Username: "u",
		}, &dbadmin.Credentials{
			Password: "p",
		}
}

func TestPool_WithdrawOpensOnDemand(t *testing.T) {
	stub := newStubDriver(dbadmin.EngineMariaDB)
	restore := installStubDriver(t, dbadmin.EngineMariaDB, stub)
	defer restore()

	pool := NewPool(PoolOptions{
		Resolver: func(ctx context.Context, id dbadmin.ConnectionID) (*dbadmin.Connection, *dbadmin.Credentials, error) {
			c, creds := makeConn(id, dbadmin.EngineMariaDB)
			return c, creds, nil
		},
		IdleTimeout: time.Minute,
	})
	defer pool.Close()

	ctx := context.Background()
	c1, rel1, err := pool.Withdraw(ctx, "conn-A")
	if err != nil {
		t.Fatal(err)
	}
	if c1 == nil {
		t.Fatal("Withdraw returned nil Conn")
	}
	if stub.openCount.Load() != 1 {
		t.Errorf("openCount = %d, want 1", stub.openCount.Load())
	}

	// Second Withdraw for the same ID reuses the same Conn.
	c2, rel2, err := pool.Withdraw(ctx, "conn-A")
	if err != nil {
		t.Fatal(err)
	}
	if c2 != c1 {
		t.Error("expected the same Conn pointer for repeat Withdraw")
	}
	if stub.openCount.Load() != 1 {
		t.Errorf("openCount after reuse = %d, want 1", stub.openCount.Load())
	}

	rel1()
	rel2()

	// Withdraw for a different ID opens a new Conn.
	_, rel3, err := pool.Withdraw(ctx, "conn-B")
	if err != nil {
		t.Fatal(err)
	}
	defer rel3()
	if stub.openCount.Load() != 2 {
		t.Errorf("openCount after second ID = %d, want 2", stub.openCount.Load())
	}
}

func TestPool_ConcurrentFirstOpenIsSerialized(t *testing.T) {
	// Two goroutines Withdraw the same ID simultaneously. The Pool
	// must open the backend Conn EXACTLY ONCE. Without serialization,
	// stubDriver.openCount would briefly equal 2.
	stub := newStubDriver(dbadmin.EngineMariaDB)
	restore := installStubDriver(t, dbadmin.EngineMariaDB, stub)
	defer restore()

	// Slow Open so the race window is visible.
	stub.openHook = func(ctx context.Context, c *dbadmin.Connection) {
		time.Sleep(20 * time.Millisecond)
	}

	pool := NewPool(PoolOptions{
		Resolver: func(ctx context.Context, id dbadmin.ConnectionID) (*dbadmin.Connection, *dbadmin.Credentials, error) {
			c, creds := makeConn(id, dbadmin.EngineMariaDB)
			return c, creds, nil
		},
		IdleTimeout: time.Minute,
	})
	defer pool.Close()

	const racers = 8
	conns := make([]Conn, racers)
	releases := make([]func(), racers)
	errs := make([]error, racers)

	var wg sync.WaitGroup
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func(i int) {
			defer wg.Done()
			conns[i], releases[i], errs[i] = pool.Withdraw(context.Background(), "conn-X")
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("racer %d err: %v", i, err)
		}
	}
	for i := 1; i < racers; i++ {
		if conns[i] != conns[0] {
			t.Errorf("racer %d got a different Conn (parallel-open race)", i)
		}
	}
	if stub.openCount.Load() != 1 {
		t.Errorf("openCount = %d, want 1 (parallel-open race)", stub.openCount.Load())
	}
	for _, rel := range releases {
		rel()
	}
}

func TestPool_IdleEviction(t *testing.T) {
	stub := newStubDriver(dbadmin.EngineMariaDB)
	restore := installStubDriver(t, dbadmin.EngineMariaDB, stub)
	defer restore()

	pool := NewPool(PoolOptions{
		Resolver: func(ctx context.Context, id dbadmin.ConnectionID) (*dbadmin.Connection, *dbadmin.Credentials, error) {
			c, creds := makeConn(id, dbadmin.EngineMariaDB)
			return c, creds, nil
		},
		// Tight timeout for the test; sweeper runs every 30s minimum,
		// so we drive eviction manually.
		IdleTimeout: 10 * time.Millisecond,
	})
	defer pool.Close()

	_, rel, err := pool.Withdraw(context.Background(), "conn-A")
	if err != nil {
		t.Fatal(err)
	}
	rel()

	if got := pool.Stats().OpenConns; got != 1 {
		t.Errorf("OpenConns after first Withdraw = %d, want 1", got)
	}

	time.Sleep(20 * time.Millisecond)
	pool.sweepOnce()

	if got := pool.Stats().OpenConns; got != 0 {
		t.Errorf("OpenConns after sweep = %d, want 0", got)
	}
}

func TestPool_InUseConnsNotEvicted(t *testing.T) {
	stub := newStubDriver(dbadmin.EngineMariaDB)
	restore := installStubDriver(t, dbadmin.EngineMariaDB, stub)
	defer restore()

	pool := NewPool(PoolOptions{
		Resolver: func(ctx context.Context, id dbadmin.ConnectionID) (*dbadmin.Connection, *dbadmin.Credentials, error) {
			c, creds := makeConn(id, dbadmin.EngineMariaDB)
			return c, creds, nil
		},
		IdleTimeout: 10 * time.Millisecond,
	})
	defer pool.Close()

	// Withdraw but DON'T release. inUse stays > 0.
	_, rel, err := pool.Withdraw(context.Background(), "conn-A")
	if err != nil {
		t.Fatal(err)
	}
	defer rel()

	time.Sleep(20 * time.Millisecond)
	pool.sweepOnce()

	if got := pool.Stats().OpenConns; got != 1 {
		t.Errorf("OpenConns after sweep with one in-use = %d, want 1", got)
	}
	if got := pool.Stats().InUseConns; got != 1 {
		t.Errorf("InUseConns = %d, want 1", got)
	}
}

func TestPool_DropClosesAndForgets(t *testing.T) {
	stub := newStubDriver(dbadmin.EngineMariaDB)
	restore := installStubDriver(t, dbadmin.EngineMariaDB, stub)
	defer restore()

	pool := NewPool(PoolOptions{
		Resolver: func(ctx context.Context, id dbadmin.ConnectionID) (*dbadmin.Connection, *dbadmin.Credentials, error) {
			c, creds := makeConn(id, dbadmin.EngineMariaDB)
			return c, creds, nil
		},
		IdleTimeout: time.Minute,
	})
	defer pool.Close()

	conn, rel, err := pool.Withdraw(context.Background(), "conn-A")
	if err != nil {
		t.Fatal(err)
	}
	rel()
	if err := pool.Drop("conn-A"); err != nil {
		t.Fatal(err)
	}
	// Drop closed the Conn.
	if sc, ok := conn.(*stubConn); ok && !sc.closed.Load() {
		t.Error("Drop did not close the underlying Conn")
	}
	if got := pool.Stats().OpenConns; got != 0 {
		t.Errorf("OpenConns after Drop = %d, want 0", got)
	}
	// Drop of unknown ID is fine.
	if err := pool.Drop("nope"); err != nil {
		t.Errorf("Drop(unknown) err = %v, want nil", err)
	}
}

func TestPool_CloseShutsDown(t *testing.T) {
	stub := newStubDriver(dbadmin.EngineMariaDB)
	restore := installStubDriver(t, dbadmin.EngineMariaDB, stub)
	defer restore()

	pool := NewPool(PoolOptions{
		Resolver: func(ctx context.Context, id dbadmin.ConnectionID) (*dbadmin.Connection, *dbadmin.Credentials, error) {
			c, creds := makeConn(id, dbadmin.EngineMariaDB)
			return c, creds, nil
		},
		IdleTimeout: time.Minute,
	})

	conn, rel, err := pool.Withdraw(context.Background(), "conn-A")
	if err != nil {
		t.Fatal(err)
	}
	rel()

	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	// Repeated Close is safe.
	if err := pool.Close(); err != nil {
		t.Errorf("second Close err = %v, want nil", err)
	}
	// Underlying Conn closed.
	if sc, ok := conn.(*stubConn); ok && !sc.closed.Load() {
		t.Error("Close did not close the underlying Conn")
	}
	// Withdraw after Close fails.
	_, _, err = pool.Withdraw(context.Background(), "conn-B")
	if !errors.Is(err, ErrPoolClosed) {
		t.Errorf("post-Close Withdraw err = %v, want ErrPoolClosed", err)
	}
}

func TestPool_ResolverError(t *testing.T) {
	pool := NewPool(PoolOptions{
		Resolver: func(ctx context.Context, id dbadmin.ConnectionID) (*dbadmin.Connection, *dbadmin.Credentials, error) {
			return nil, nil, errors.New("resolver fail")
		},
		IdleTimeout: time.Minute,
	})
	defer pool.Close()

	_, _, err := pool.Withdraw(context.Background(), "conn-A")
	if err == nil {
		t.Fatal("expected resolver error to propagate")
	}
}

func TestPool_NilResolverPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("nil Resolver should panic")
		}
	}()
	_ = NewPool(PoolOptions{})
}

func TestPool_OpenFailureReturnsError(t *testing.T) {
	stub := newStubDriver(dbadmin.EngineMariaDB)
	stub.openErr = errors.New("can't open")
	restore := installStubDriver(t, dbadmin.EngineMariaDB, stub)
	defer restore()

	pool := NewPool(PoolOptions{
		Resolver: func(ctx context.Context, id dbadmin.ConnectionID) (*dbadmin.Connection, *dbadmin.Credentials, error) {
			c, creds := makeConn(id, dbadmin.EngineMariaDB)
			return c, creds, nil
		},
		IdleTimeout: time.Minute,
	})
	defer pool.Close()

	_, _, err := pool.Withdraw(context.Background(), "conn-A")
	if err == nil {
		t.Fatal("expected Open error to propagate")
	}
	if pool.Stats().OpenConns != 0 {
		t.Errorf("a failed open leaked a Conn into stats: %+v", pool.Stats())
	}
}

func TestPool_LoggerHookFires(t *testing.T) {
	stub := newStubDriver(dbadmin.EngineMariaDB)
	restore := installStubDriver(t, dbadmin.EngineMariaDB, stub)
	defer restore()

	var logged atomic.Int32
	pool := NewPool(PoolOptions{
		Resolver: func(ctx context.Context, id dbadmin.ConnectionID) (*dbadmin.Connection, *dbadmin.Credentials, error) {
			c, creds := makeConn(id, dbadmin.EngineMariaDB)
			return c, creds, nil
		},
		IdleTimeout: 10 * time.Millisecond,
		Logger: func(format string, args ...any) {
			logged.Add(1)
			_ = fmt.Sprintf(format, args...) // exercise the format string
		},
	})
	defer pool.Close()

	_, rel, err := pool.Withdraw(context.Background(), "conn-A")
	if err != nil {
		t.Fatal(err)
	}
	rel()

	time.Sleep(20 * time.Millisecond)
	pool.sweepOnce()
	if got := logged.Load(); got < 2 {
		t.Errorf("logger fired %d times, want >= 2 (open + evict)", got)
	}
}

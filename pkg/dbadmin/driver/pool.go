package driver

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// Pool manages the set of open backend Conns across many Aura DB
// connections. Each (ConnectionID) gets at most one Conn at a time; the
// Conn's own connection-pool handles per-query parallelism within it.
//
// Idle eviction:
//   - Each Conn carries a lastUsed timestamp.
//   - A background goroutine runs every IdleTimeout/2 and closes any
//     Conn whose lastUsed is older than IdleTimeout.
//   - The Pool can be shut down via Close(); pending Withdraw calls
//     return ErrPoolClosed.
//
// Lifecycle:
//
//	pool := driver.NewPool(driver.PoolOptions{
//	    Resolver:    func(id) (*Connection, *Credentials, error) { ... },
//	    IdleTimeout: 5 * time.Minute,
//	    PoolSize:    4,
//	})
//	defer pool.Close()
//	conn, release, err := pool.Withdraw(ctx, connectionID)
//	if err != nil { ... }
//	defer release()
//	rows, err := conn.Query(ctx, "SELECT 1")
type Pool struct {
	opts PoolOptions

	mu        sync.Mutex
	conns     map[dbadmin.ConnectionID]*pooledConn
	openLocks map[dbadmin.ConnectionID]*sync.Mutex // per-ID singleflight gate for first open
	closed    atomic.Bool
	doneCh    chan struct{}
	tickerW   sync.WaitGroup
}

// PoolOptions configure a Pool.
type PoolOptions struct {
	// Resolver fetches the Connection metadata + Credentials for a
	// given ID. Called when the pool needs to open a fresh Conn.
	// Implementations usually delegate to the engine's
	// ConnectionStore — but the Pool doesn't import that to avoid
	// a cycle.
	//
	// Resolver MUST return decrypted credentials; the Pool does not
	// decrypt.
	Resolver func(context.Context, dbadmin.ConnectionID) (*dbadmin.Connection, *dbadmin.Credentials, error)

	// IdleTimeout is how long a Conn may sit unused before the
	// idle-eviction goroutine closes it. Default 5 minutes; the
	// engine reads this from Config.Query.PoolIdleTimeout.
	IdleTimeout time.Duration

	// PoolSize is the per-Conn backend-pool size, forwarded into
	// Driver.Open. The engine reads this from Config.Query.PoolSizePerConn.
	PoolSize int

	// Logger is an optional callback for pool lifecycle events.
	// Receives messages like "opened conn id=X", "evicted conn id=X
	// idle=2m". Hook into the host's logger; default nil = silent.
	Logger func(format string, args ...any)
}

// pooledConn holds one open Conn plus bookkeeping.
type pooledConn struct {
	id       dbadmin.ConnectionID
	conn     Conn
	lastUsed atomic.Int64 // unix-nano; updated on every Withdraw
	inUse    atomic.Int32 // ref count; 0 = idle
	closed   atomic.Bool
}

// NewPool constructs a Pool with the given options. The idle-eviction
// goroutine is started immediately.
func NewPool(opts PoolOptions) *Pool {
	if opts.Resolver == nil {
		panic("driver/pool: Resolver is required")
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = 5 * time.Minute
	}
	if opts.PoolSize < 1 {
		opts.PoolSize = 4
	}

	p := &Pool{
		opts:      opts,
		conns:     make(map[dbadmin.ConnectionID]*pooledConn),
		openLocks: make(map[dbadmin.ConnectionID]*sync.Mutex),
		doneCh:    make(chan struct{}),
	}
	p.tickerW.Add(1)
	go p.idleSweeper()
	return p
}

// ErrPoolClosed is returned by Withdraw after Close has been called.
var ErrPoolClosed = errors.New("driver/pool: closed")

// Withdraw returns a Conn for the given Aura DB connection ID, opening
// it on demand if necessary. The release func MUST be called when the
// caller is done with the Conn — it decrements the ref count so the
// idle sweeper can close the Conn once nothing else holds it.
//
// Concurrent Withdraws for the same ID return the SAME underlying Conn
// — they don't open multiple backend pools. The first-caller-wins
// pattern minimizes connection churn for high-traffic sites.
func (p *Pool) Withdraw(ctx context.Context, id dbadmin.ConnectionID) (Conn, func(), error) {
	if p.closed.Load() {
		return nil, nil, ErrPoolClosed
	}

	// Fast path: cached Conn under the global lock.
	p.mu.Lock()
	if pc, ok := p.conns[id]; ok && !pc.closed.Load() {
		pc.inUse.Add(1)
		pc.lastUsed.Store(time.Now().UnixNano())
		p.mu.Unlock()
		return pc.conn, p.releaseFn(pc), nil
	}
	// Slow path: acquire the per-ID open lock so concurrent first-
	// opens for the same ID serialize. We hold the open lock for the
	// (potentially slow) backend dial, but DON'T hold the global mu,
	// so unrelated IDs proceed in parallel.
	openLock := p.openLocks[id]
	if openLock == nil {
		openLock = &sync.Mutex{}
		p.openLocks[id] = openLock
	}
	p.mu.Unlock()

	openLock.Lock()
	defer openLock.Unlock()

	// Recheck closed flag — Pool.Close may have fired while we
	// waited for openLock. If closed, refuse before dialing.
	if p.closed.Load() {
		return nil, nil, ErrPoolClosed
	}

	// Recheck under the open lock: another goroutine may have just
	// opened. Reuse if so.
	p.mu.Lock()
	if pc, ok := p.conns[id]; ok && !pc.closed.Load() {
		pc.inUse.Add(1)
		pc.lastUsed.Store(time.Now().UnixNano())
		p.mu.Unlock()
		return pc.conn, p.releaseFn(pc), nil
	}
	p.mu.Unlock()

	// First-opener for this ID. Dial.
	conn, err := p.openForID(ctx, id)
	if err != nil {
		return nil, nil, err
	}

	// Post-review fix: Pool.Close may have fired during the dial.
	// Recheck closed under the lock; if closed, close the freshly-
	// dialed Conn (and its tunnel) and return ErrPoolClosed.
	p.mu.Lock()
	if p.closed.Load() {
		p.mu.Unlock()
		_ = conn.Close()
		return nil, nil, ErrPoolClosed
	}
	pc := &pooledConn{
		id:   id,
		conn: conn,
	}
	pc.lastUsed.Store(time.Now().UnixNano())
	pc.inUse.Store(1)
	p.conns[id] = pc
	p.mu.Unlock()

	if p.opts.Logger != nil {
		p.opts.Logger("driver/pool: opened conn id=%s", id)
	}
	return conn, p.releaseFn(pc), nil
}

// openForID resolves the connection metadata, picks the right driver,
// and opens a fresh Conn. Pulled out so the locking in Withdraw stays
// readable.
func (p *Pool) openForID(ctx context.Context, id dbadmin.ConnectionID) (Conn, error) {
	c, creds, err := p.opts.Resolver(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("driver/pool: resolve %s: %w", id, err)
	}
	if c == nil {
		return nil, fmt.Errorf("driver/pool: resolver returned nil connection for %s", id)
	}
	if creds == nil {
		return nil, fmt.Errorf("driver/pool: resolver returned nil credentials for %s", id)
	}
	defer creds.Zero()

	drv, err := For(c.Engine)
	if err != nil {
		return nil, err
	}
	return drv.Open(ctx, c, creds, p.opts.PoolSize)
}

// releaseFn returns a closure that decrements pc.inUse and updates
// lastUsed. Post-review fix: wrapped in sync.Once so accidental double-
// release (the usual `defer release()` plus an explicit release()
// pattern) is a no-op rather than double-decrementing.
func (p *Pool) releaseFn(pc *pooledConn) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			pc.inUse.Add(-1)
			pc.lastUsed.Store(time.Now().UnixNano())
		})
	}
}

// Drop forcibly closes and removes a Conn for the given ID. Used when
// the engine knows the connection metadata changed (e.g., host/port
// updated) or the underlying database is gone. Returns nil if the ID
// has no open Conn.
//
// In-flight queries on the Conn complete with errors when the backend
// closes their connections.
func (p *Pool) Drop(id dbadmin.ConnectionID) error {
	p.mu.Lock()
	pc, ok := p.conns[id]
	if !ok {
		// Still drop any openLock entry — Drop may run for an ID
		// that was opened then idle-evicted, leaving the openLock
		// behind.
		delete(p.openLocks, id)
		p.mu.Unlock()
		return nil
	}
	delete(p.conns, id)
	delete(p.openLocks, id)
	p.mu.Unlock()

	pc.closed.Store(true)
	err := pc.conn.Close()
	if p.opts.Logger != nil {
		p.opts.Logger("driver/pool: dropped conn id=%s", id)
	}
	return err
}

// Close stops the idle sweeper and closes every open Conn. Safe to
// call multiple times; subsequent calls return nil. Withdraws after
// Close return ErrPoolClosed.
func (p *Pool) Close() error {
	if !p.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(p.doneCh)
	p.tickerW.Wait()

	p.mu.Lock()
	conns := make([]*pooledConn, 0, len(p.conns))
	for _, pc := range p.conns {
		conns = append(conns, pc)
	}
	// Post-review fix: don't nil the map (that breaks any concurrent
	// Withdraw's slow-path map insertion). The slow path now rechecks
	// p.closed under the lock and refuses; leaving the empty map in
	// place lets that check fail safely.
	p.conns = make(map[dbadmin.ConnectionID]*pooledConn)
	p.openLocks = make(map[dbadmin.ConnectionID]*sync.Mutex)
	p.mu.Unlock()

	var firstErr error
	for _, pc := range conns {
		pc.closed.Store(true)
		if err := pc.conn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// idleSweeper closes idle Conns past the configured timeout. Runs every
// IdleTimeout/2 (or 1s minimum). PR #3.5: floor lowered from 30s to 1s
// so operators with very-aggressive eviction (IdleTimeout < 60s) get
// timely closes. The sweep is cheap — one map walk under a brief lock.
func (p *Pool) idleSweeper() {
	defer p.tickerW.Done()

	interval := p.opts.IdleTimeout / 2
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-p.doneCh:
			return
		case <-ticker.C:
			p.sweepOnce()
		}
	}
}

// sweepOnce walks the pool and evicts idle Conns. Hold the lock briefly
// while collecting evictees, then close them outside the lock.
func (p *Pool) sweepOnce() {
	cutoff := time.Now().Add(-p.opts.IdleTimeout).UnixNano()

	p.mu.Lock()
	var evictees []*pooledConn
	for id, pc := range p.conns {
		if pc.inUse.Load() > 0 {
			continue
		}
		if pc.lastUsed.Load() > cutoff {
			continue
		}
		evictees = append(evictees, pc)
		delete(p.conns, id)
		// Post-review fix: also drop the openLock so the map can't
		// grow unbounded over many transient site lifecycles.
		delete(p.openLocks, id)
	}
	p.mu.Unlock()

	for _, pc := range evictees {
		pc.closed.Store(true)
		_ = pc.conn.Close()
		if p.opts.Logger != nil {
			p.opts.Logger("driver/pool: evicted idle conn id=%s", pc.id)
		}
	}
}

// Stats returns a snapshot of pool state. Useful for ops dashboards
// and tests.
func (p *Pool) Stats() PoolStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := PoolStats{
		OpenConns: len(p.conns),
	}
	for _, pc := range p.conns {
		if pc.inUse.Load() > 0 {
			s.InUseConns++
		}
	}
	return s
}

// PoolStats is the result of Pool.Stats.
type PoolStats struct {
	OpenConns  int
	InUseConns int
}

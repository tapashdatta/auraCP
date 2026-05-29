package schema

import (
	"context"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// Cache wraps a Reader with TTL-based caching. The engine's HTTP layer
// calls Invalidate(schema) after any DDL that targets `schema`, so
// callers see fresh metadata on the next read.
//
// Concurrency: safe. Read-mostly internal map under a RWMutex.
//
// Capacity: capped at MaxEntries (LRU eviction). Each key counts as one
// entry regardless of the size of the cached value.
//
// Identifier validation: every method that takes an identifier runs
// ValidateIdentifier first and returns ErrInvalidIdentifier on failure
// without invoking the inner reader (and without polluting the cache).
//
// Load coalescing: concurrent uncached requests for the same key share
// a single backend load via golang.org/x/sync/singleflight. The
// shared load still respects Invalidate's generation counter — when an
// Invalidate races a slow load, the load completes and its result is
// returned to the in-flight callers, but it is NOT inserted into the
// cache. PR #4.5 (H6).
type Cache struct {
	inner Reader
	cfg   CacheConfig

	mu      sync.RWMutex
	entries map[string]*cacheEntry
	// lruOrder is sorted with most-recently-used at the front. Used
	// only on miss/insert when len(entries) >= MaxEntries.
	lruOrder []string
	// generation increments on every Invalidate. A load that started
	// before an Invalidate completed sees a stale generation and drops
	// its result rather than re-poisoning the cache.
	generation uint64

	// sf coalesces concurrent loads of the same key.
	sf singleflight.Group
}

type cacheEntry struct {
	value    any
	cachedAt time.Time
	// lastAccess tracks for LRU eviction. We update on read hits AND
	// on inserts; reads use atomic-style updates via a single
	// timestamp field.
	lastAccess atomic_time
}

// atomic_time wraps a time.Time for cheap atomic updates without sync/atomic.Value.
// We use a mutex-protected store; reads are rare enough that this is fine.
type atomic_time struct {
	mu sync.Mutex
	t  time.Time
}

func (a *atomic_time) Store(t time.Time) { a.mu.Lock(); a.t = t; a.mu.Unlock() }
func (a *atomic_time) Load() time.Time   { a.mu.Lock(); defer a.mu.Unlock(); return a.t }

// NewCache wraps a Reader with a configured cache. Defaults applied
// when cfg has zero values.
func NewCache(inner Reader, cfg CacheConfig) *Cache {
	if cfg.TTL <= 0 {
		cfg.TTL = 5 * time.Minute
	}
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = 1000
	}
	return &Cache{
		inner:   inner,
		cfg:     cfg,
		entries: make(map[string]*cacheEntry),
	}
}

// Engine reports the wrapped Reader's engine.
func (c *Cache) Engine() dbadmin.EngineKind { return c.inner.Engine() }

// Invalidate drops every cache entry whose key starts with prefix
// followed by "/" (the key delimiter), plus any key equal to prefix
// itself. So Invalidate("foo") drops "foo/@tables" but NOT "foobar/x".
// In addition, a schema-level invalidation always drops the global
// "@dbs" entry and any "@schemas:*" entries that may be stale.
// Pass empty prefix to invalidate everything.
//
// When the Cache was built with a Bucket, this method matches keys
// after stripping the bucket prefix so callers don't need to know it.
func (c *Cache) Invalidate(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	if prefix == "" {
		c.entries = make(map[string]*cacheEntry)
		c.lruOrder = nil
		return
	}
	bucketPrefix := ""
	if c.cfg.Bucket != "" {
		bucketPrefix = c.cfg.Bucket + "\x00"
	}
	prefixK := bucketPrefix + prefix
	for k := range c.entries {
		// Strip bucket discriminator for the user-visible prefix match.
		logical := k
		if bucketPrefix != "" && strings.HasPrefix(k, bucketPrefix) {
			logical = k[len(bucketPrefix):]
		}
		if k == prefixK || strings.HasPrefix(k, prefixK+"/") {
			delete(c.entries, k)
			continue
		}
		// DDL at the schema level may also affect the global database
		// list (e.g. CREATE SCHEMA) and schema lists (e.g. DROP
		// SCHEMA). Drop those entries too.
		if logical == "@dbs" || strings.HasPrefix(logical, "@schemas:") {
			delete(c.entries, k)
		}
	}
	// Rebuild lruOrder dropping evicted keys.
	survivors := c.lruOrder[:0]
	for _, k := range c.lruOrder {
		if _, ok := c.entries[k]; ok {
			survivors = append(survivors, k)
		}
	}
	c.lruOrder = survivors
}

// bkey applies the configured Bucket discriminator to a cache key so
// two cache instances sharing the same underlying map (or two cache
// users in the same bucket-aware key space) can't collide on
// schema/table only. Empty bucket = legacy unbucketed key space.
func (c *Cache) bkey(key string) string {
	if c.cfg.Bucket == "" {
		return key
	}
	return c.cfg.Bucket + "\x00" + key
}

// cacheFetch is the cache's read-through helper. It looks up `key`,
// returns the cached value if fresh, or calls `load` and caches the
// result.
//
// Concurrency:
//   - singleflight.Group coalesces N concurrent loads of the same key
//     into one backend call (PR #4.5 H6: thundering-herd avoidance).
//   - The singleflight slot captures the cache generation at slot
//     ACQUISITION time so the result-drop logic in the insert path
//     still works: if Invalidate ran while the load was in flight, we
//     still hand the value to all callers (load completed), but we do
//     NOT insert into the map (PR #4 H7: stale-result race protection).
//   - A failed load is NOT cached; the next call retries.
func cacheFetch[T any](c *Cache, key string, load func() (T, error)) (T, error) {
	var zero T
	bk := c.bkey(key)

	// Fast path: read lock.
	c.mu.RLock()
	e, ok := c.entries[bk]
	gen := c.generation
	c.mu.RUnlock()
	if ok && time.Since(e.cachedAt) < c.cfg.TTL {
		e.lastAccess.Store(time.Now())
		if v, ok := e.value.(T); ok {
			return v, nil
		}
		// Type mismatch — should never happen given the per-method
		// key naming, but if it does, fall through to refresh.
	}

	// Miss / stale: coalesce loads. singleflight returns the same
	// (value, error) to every concurrent caller that joined this slot.
	// We capture the generation BEFORE entering Do so leader and
	// followers observe the same race window against Invalidate.
	vAny, err, _ := c.sf.Do(bk, func() (any, error) {
		v, err := load()
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.generation != gen {
			// Concurrent Invalidate ran since we started; return value
			// to in-flight callers but do NOT insert.
			return v, nil
		}
		existing, hadEntry := c.entries[bk]
		c.entries[bk] = &cacheEntry{
			value:    v,
			cachedAt: time.Now(),
		}
		c.entries[bk].lastAccess.Store(time.Now())
		if !hadEntry || existing == nil {
			c.lruOrder = append(c.lruOrder, bk)
		}
		if len(c.entries) > c.cfg.MaxEntries {
			c.evictLRULocked()
		}
		return v, nil
	})
	if err != nil {
		return zero, err
	}
	v, ok := vAny.(T)
	if !ok {
		return zero, nil
	}
	return v, nil
}

// evictLRULocked drops the least-recently-used entry. Caller holds mu.
func (c *Cache) evictLRULocked() {
	if len(c.lruOrder) == 0 {
		return
	}
	// Find the entry whose lastAccess is oldest. Linear scan; with
	// MaxEntries=1000 this is fast enough.
	var oldestKey string
	var oldest time.Time
	for _, k := range c.lruOrder {
		e := c.entries[k]
		if e == nil {
			continue
		}
		t := e.lastAccess.Load()
		if oldestKey == "" || t.Before(oldest) {
			oldestKey = k
			oldest = t
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
		// Remove from lruOrder.
		out := c.lruOrder[:0]
		for _, k := range c.lruOrder {
			if k != oldestKey {
				out = append(out, k)
			}
		}
		c.lruOrder = out
	}
}

// ─── Reader interface methods ────────────────────────────────────────

func (c *Cache) ListDatabases(ctx context.Context) ([]string, error) {
	return cacheFetch(c, "@dbs", func() ([]string, error) {
		return c.inner.ListDatabases(ctx)
	})
}

func (c *Cache) ListSchemas(ctx context.Context, database string) ([]string, error) {
	if err := ValidateIdentifier(database); err != nil {
		return nil, err
	}
	return cacheFetch(c, "@schemas:"+database, func() ([]string, error) {
		return c.inner.ListSchemas(ctx, database)
	})
}

func (c *Cache) ListTables(ctx context.Context, schema string) ([]TableSummary, error) {
	if err := ValidateIdentifier(schema); err != nil {
		return nil, err
	}
	return cacheFetch(c, schema+"/@tables", func() ([]TableSummary, error) {
		return c.inner.ListTables(ctx, schema)
	})
}

func (c *Cache) GetTable(ctx context.Context, schema, table string) (*Table, error) {
	if err := ValidateIdentifier(schema); err != nil {
		return nil, err
	}
	if err := ValidateIdentifier(table); err != nil {
		return nil, err
	}
	tbl, err := cacheFetch(c, schema+"/"+table, func() (*Table, error) {
		return c.inner.GetTable(ctx, schema, table)
	})
	if err != nil {
		return nil, err
	}
	// Deep-copy on the way out so callers can't mutate the cached
	// value (and so two concurrent callers don't observe each other's
	// mutations).
	return cloneTable(tbl), nil
}

func (c *Cache) ListViews(ctx context.Context, schema string) ([]ViewSummary, error) {
	if err := ValidateIdentifier(schema); err != nil {
		return nil, err
	}
	return cacheFetch(c, schema+"/@views", func() ([]ViewSummary, error) {
		return c.inner.ListViews(ctx, schema)
	})
}

func (c *Cache) ListFunctions(ctx context.Context, schema string) ([]FunctionSummary, error) {
	if err := ValidateIdentifier(schema); err != nil {
		return nil, err
	}
	return cacheFetch(c, schema+"/@functions", func() ([]FunctionSummary, error) {
		return c.inner.ListFunctions(ctx, schema)
	})
}

func (c *Cache) ListProcedures(ctx context.Context, schema string) ([]ProcedureSummary, error) {
	if err := ValidateIdentifier(schema); err != nil {
		return nil, err
	}
	return cacheFetch(c, schema+"/@procedures", func() ([]ProcedureSummary, error) {
		return c.inner.ListProcedures(ctx, schema)
	})
}

func (c *Cache) ListTriggers(ctx context.Context, schema string) ([]TriggerSummary, error) {
	if err := ValidateIdentifier(schema); err != nil {
		return nil, err
	}
	return cacheFetch(c, schema+"/@triggers", func() ([]TriggerSummary, error) {
		return c.inner.ListTriggers(ctx, schema)
	})
}

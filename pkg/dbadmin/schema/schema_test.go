package schema_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/driver"
	"github.com/auracp/auracp/pkg/dbadmin/schema"
)

// ─── Identifier validation ───────────────────────────────────────────

func TestValidateIdentifier(t *testing.T) {
	good := []string{
		"users", "Users", "USERS", "_users", "user1", "users_v2",
		"u", "long_name_with_underscores_and_digits_42",
		"a$b$c", // $ allowed mid-name (MySQL legacy support)
		// 63 chars is the Postgres NAMEDATALEN ceiling.
		strings.Repeat("a", 63),
	}
	for _, n := range good {
		if err := schema.ValidateIdentifier(n); err != nil {
			t.Errorf("ValidateIdentifier(%q) = %v, want nil", n, err)
		}
	}

	bad := []string{
		"", "1user", "$user", "user-name", "user name", "user;DROP TABLE x",
		"users.table", "users\"x", "users'x", "users`x",
		"user\x00name",
		// 64 chars exceeds Postgres NAMEDATALEN; rejected.
		strings.Repeat("a", 64),
		strings.Repeat("a", 65),
		"user/comment", "../escape",
	}
	for _, n := range bad {
		if err := schema.ValidateIdentifier(n); err == nil {
			t.Errorf("ValidateIdentifier(%q) = nil, want ErrInvalidIdentifier", n)
		}
		if err := schema.ValidateIdentifier(n); !errors.Is(err, schema.ErrInvalidIdentifier) {
			t.Errorf("ValidateIdentifier(%q) didn't wrap ErrInvalidIdentifier: %v", n, err)
		}
	}
}

// ─── Stub Conn for unit tests ────────────────────────────────────────

// stubConn implements driver.Conn with a SQL → result map. Tests pre-
// load the expected rows for each query SQL.
type stubConn struct {
	mu        sync.Mutex
	responses map[string]stubResult
	queryLog  []string
}

type stubResult struct {
	rows [][]any
	cols []driver.ColumnInfo
	err  error
}

func newStubConn() *stubConn {
	return &stubConn{responses: make(map[string]stubResult)}
}

func (c *stubConn) on(sqlText string, result stubResult) *stubConn {
	c.mu.Lock()
	c.responses[sqlText] = result
	c.mu.Unlock()
	return c
}

func (c *stubConn) Query(ctx context.Context, _ driver.Limits, sqlText string, args ...any) (driver.Rows, error) {
	c.mu.Lock()
	c.queryLog = append(c.queryLog, sqlText)
	r, ok := c.responses[sqlText]
	c.mu.Unlock()
	if !ok {
		return nil, errors.New("stub: no response configured for query: " + summarize(sqlText))
	}
	if r.err != nil {
		return nil, r.err
	}
	return &stubRows{rows: r.rows, cols: r.cols}, nil
}

func (c *stubConn) Exec(ctx context.Context, _ driver.Limits, sqlText string, args ...any) (driver.Result, error) {
	return driver.Result{}, errors.New("stub: Exec not implemented")
}
func (c *stubConn) Ping(ctx context.Context) error                  { return nil }
func (c *stubConn) ServerVersion(ctx context.Context) (string, error) {
	return "stub-0.0.0", nil
}
func (c *stubConn) Close() error { return nil }

func summarize(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}

type stubRows struct {
	rows [][]any
	cols []driver.ColumnInfo
	idx  int
}

func (r *stubRows) Columns() []driver.ColumnInfo { return r.cols }
func (r *stubRows) Next(ctx context.Context) ([]any, error) {
	if r.idx >= len(r.rows) {
		return nil, driver.ErrEOF
	}
	row := r.rows[r.idx]
	r.idx++
	return row, nil
}
func (r *stubRows) Close() error { return nil }

// ─── MySQL reader tests ──────────────────────────────────────────────

func TestMySQL_ListDatabases(t *testing.T) {
	c := newStubConn()
	const expectedSQL = `
		SELECT schema_name
		FROM information_schema.schemata
		WHERE schema_name NOT IN ('information_schema', 'mysql', 'performance_schema', 'sys')
		ORDER BY schema_name`
	c.on(expectedSQL, stubResult{
		rows: [][]any{{"app_db"}, {"reporting"}},
	})

	r, err := schema.For(c, dbadmin.EngineMariaDB)
	if err != nil {
		t.Fatal(err)
	}
	dbs, err := r.ListDatabases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(dbs) != 2 || dbs[0] != "app_db" || dbs[1] != "reporting" {
		t.Errorf("databases = %v", dbs)
	}
}

func TestMySQL_GetTable_RejectsBadIdentifier(t *testing.T) {
	c := newStubConn()
	r, _ := schema.For(c, dbadmin.EngineMariaDB)
	_, err := r.GetTable(context.Background(), "'; DROP TABLE x; --", "users")
	if err == nil {
		t.Fatal("GetTable with bad identifier should return ErrInvalidIdentifier")
	}
	if !errors.Is(err, schema.ErrInvalidIdentifier) {
		t.Errorf("GetTable(bad schema): err = %v, want ErrInvalidIdentifier", err)
	}
	// Inner reader must NOT have been called.
	c.mu.Lock()
	queryCount := len(c.queryLog)
	c.mu.Unlock()
	if queryCount != 0 {
		t.Errorf("GetTable with bad identifier reached driver: %d queries", queryCount)
	}
}

// ─── Cache tests ─────────────────────────────────────────────────────

func TestCache_HitsAvoidUnderlyingCall(t *testing.T) {
	c := newStubConn().on(`
		SELECT schema_name
		FROM information_schema.schemata
		WHERE schema_name NOT IN ('information_schema', 'mysql', 'performance_schema', 'sys')
		ORDER BY schema_name`, stubResult{
		rows: [][]any{{"app_db"}},
	})
	r, _ := schema.For(c, dbadmin.EngineMariaDB)
	cache := schema.NewCache(r, schema.CacheConfig{TTL: 5 * time.Minute, MaxEntries: 100})

	for i := 0; i < 5; i++ {
		if _, err := cache.ListDatabases(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	// Underlying conn should have seen the query exactly once.
	c.mu.Lock()
	count := 0
	for _, q := range c.queryLog {
		if strings.Contains(q, "information_schema.schemata") {
			count++
		}
	}
	c.mu.Unlock()
	if count != 1 {
		t.Errorf("expected 1 underlying call, got %d", count)
	}
}

func TestCache_TTLExpires(t *testing.T) {
	c := newStubConn().on(`
		SELECT schema_name
		FROM information_schema.schemata
		WHERE schema_name NOT IN ('information_schema', 'mysql', 'performance_schema', 'sys')
		ORDER BY schema_name`, stubResult{
		rows: [][]any{{"app_db"}},
	})
	r, _ := schema.For(c, dbadmin.EngineMariaDB)
	cache := schema.NewCache(r, schema.CacheConfig{TTL: 5 * time.Millisecond, MaxEntries: 100})

	_, _ = cache.ListDatabases(context.Background())
	time.Sleep(15 * time.Millisecond)
	_, _ = cache.ListDatabases(context.Background())

	c.mu.Lock()
	count := 0
	for _, q := range c.queryLog {
		if strings.Contains(q, "information_schema.schemata") {
			count++
		}
	}
	c.mu.Unlock()
	if count != 2 {
		t.Errorf("expected 2 underlying calls (cache expired), got %d", count)
	}
}

func TestCache_InvalidatePrefix(t *testing.T) {
	c := newStubConn()
	// Pre-load responses for two schemas' table lists.
	const tablesSQL = `
		SELECT table_name, table_type, ifnull(table_comment, ''),
		       ifnull(table_rows, 0), ifnull(engine, '')
		FROM information_schema.tables
		WHERE table_schema = ?
		ORDER BY table_name`
	c.on(tablesSQL, stubResult{
		rows: [][]any{{"users", "BASE TABLE", "", int64(100), "InnoDB"}},
	})
	r, _ := schema.For(c, dbadmin.EngineMariaDB)
	cache := schema.NewCache(r, schema.CacheConfig{TTL: 5 * time.Minute, MaxEntries: 100})

	_, _ = cache.ListTables(context.Background(), "schemaA")
	_, _ = cache.ListTables(context.Background(), "schemaB")

	// Invalidate only schemaA's entries.
	cache.Invalidate("schemaA")

	// Re-read schemaA — should miss; schemaB — hit.
	c.mu.Lock()
	beforeCount := len(c.queryLog)
	c.mu.Unlock()

	_, _ = cache.ListTables(context.Background(), "schemaA")
	_, _ = cache.ListTables(context.Background(), "schemaB")

	c.mu.Lock()
	afterCount := len(c.queryLog)
	c.mu.Unlock()

	if afterCount-beforeCount != 1 {
		t.Errorf("expected 1 new underlying call after invalidate, got %d", afterCount-beforeCount)
	}
}

func TestCache_InvalidateAll(t *testing.T) {
	c := newStubConn().on(`
		SELECT schema_name
		FROM information_schema.schemata
		WHERE schema_name NOT IN ('information_schema', 'mysql', 'performance_schema', 'sys')
		ORDER BY schema_name`, stubResult{rows: [][]any{{"app_db"}}})
	r, _ := schema.For(c, dbadmin.EngineMariaDB)
	cache := schema.NewCache(r, schema.CacheConfig{})

	_, _ = cache.ListDatabases(context.Background())
	cache.Invalidate("")
	_, _ = cache.ListDatabases(context.Background())

	c.mu.Lock()
	count := 0
	for _, q := range c.queryLog {
		if strings.Contains(q, "information_schema.schemata") {
			count++
		}
	}
	c.mu.Unlock()
	if count != 2 {
		t.Errorf("expected 2 underlying calls (invalidated all), got %d", count)
	}
}

func TestCache_LRUEviction(t *testing.T) {
	c := newStubConn()
	const tablesSQL = `
		SELECT table_name, table_type, ifnull(table_comment, ''),
		       ifnull(table_rows, 0), ifnull(engine, '')
		FROM information_schema.tables
		WHERE table_schema = ?
		ORDER BY table_name`
	c.on(tablesSQL, stubResult{rows: [][]any{{"t", "BASE TABLE", "", int64(0), "InnoDB"}}})
	r, _ := schema.For(c, dbadmin.EngineMariaDB)
	cache := schema.NewCache(r, schema.CacheConfig{TTL: time.Hour, MaxEntries: 3})

	// Fill cache.
	for _, s := range []string{"a", "b", "c"} {
		if _, err := cache.ListTables(context.Background(), s); err != nil {
			t.Fatal(err)
		}
	}
	// Access "a" so it's not the LRU.
	_, _ = cache.ListTables(context.Background(), "a")

	// Insert "d" — should evict "b" (oldest unaccessed).
	_, _ = cache.ListTables(context.Background(), "d")

	c.mu.Lock()
	queryCountBefore := len(c.queryLog)
	c.mu.Unlock()

	// Re-read "a" — should be cached (3 calls so far: a, b, c, d (a re-hit)).
	_, _ = cache.ListTables(context.Background(), "a")
	c.mu.Lock()
	queryCountAfterA := len(c.queryLog)
	c.mu.Unlock()
	if queryCountAfterA != queryCountBefore {
		t.Errorf("expected 'a' to still be cached, but it was re-fetched")
	}

	// Re-read "b" — should miss.
	_, _ = cache.ListTables(context.Background(), "b")
	c.mu.Lock()
	queryCountAfterB := len(c.queryLog)
	c.mu.Unlock()
	if queryCountAfterB == queryCountAfterA {
		t.Error("expected 'b' to have been evicted")
	}
}

func TestCache_Engine(t *testing.T) {
	c := newStubConn()
	r, _ := schema.For(c, dbadmin.EngineMariaDB)
	cache := schema.NewCache(r, schema.CacheConfig{})
	if cache.Engine() != dbadmin.EngineMariaDB {
		t.Errorf("Cache.Engine = %v, want MariaDB", cache.Engine())
	}
}

// ─── Boundary / safety tests added for PR #4 review fixes ───────────

// TestCache_InvalidatePrefix_RespectsBoundary asserts that Invalidate
// uses "/" as a key boundary, so Invalidate("a") does NOT clear
// "aa/@tables". Regression test for H1.
func TestCache_InvalidatePrefix_RespectsBoundary(t *testing.T) {
	c := newStubConn()
	const tablesSQL = `
		SELECT table_name, table_type, ifnull(table_comment, ''),
		       ifnull(table_rows, 0), ifnull(engine, '')
		FROM information_schema.tables
		WHERE table_schema = ?
		ORDER BY table_name`
	c.on(tablesSQL, stubResult{
		rows: [][]any{{"users", "BASE TABLE", "", int64(1), "InnoDB"}},
	})
	r, _ := schema.For(c, dbadmin.EngineMariaDB)
	cache := schema.NewCache(r, schema.CacheConfig{TTL: 5 * time.Minute, MaxEntries: 100})

	// Prime caches for both schema "a" and "aa".
	if _, err := cache.ListTables(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.ListTables(context.Background(), "aa"); err != nil {
		t.Fatal(err)
	}

	// Invalidate "a" — must NOT touch "aa".
	cache.Invalidate("a")

	c.mu.Lock()
	beforeCount := len(c.queryLog)
	c.mu.Unlock()

	// Reading "aa" should hit the cache (no new query). Reading "a"
	// should miss (one new query).
	if _, err := cache.ListTables(context.Background(), "aa"); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.ListTables(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}

	c.mu.Lock()
	afterCount := len(c.queryLog)
	c.mu.Unlock()
	if afterCount-beforeCount != 1 {
		t.Errorf("expected exactly 1 underlying call after Invalidate(\"a\"); got %d (\"aa\" must remain cached)", afterCount-beforeCount)
	}
}

// TestCache_RejectsBadIdentifier verifies that every Cache method that
// takes identifiers rejects invalid input with ErrInvalidIdentifier
// BEFORE invoking the inner reader (so cache and driver stay clean).
// Regression test for H2.
func TestCache_RejectsBadIdentifier(t *testing.T) {
	c := newStubConn()
	r, _ := schema.For(c, dbadmin.EngineMariaDB)
	cache := schema.NewCache(r, schema.CacheConfig{})

	bad := "'; DROP TABLE x; --"
	ctx := context.Background()

	type call struct {
		name string
		fn   func() error
	}
	calls := []call{
		{"ListTables", func() error { _, err := cache.ListTables(ctx, bad); return err }},
		{"GetTable_badSchema", func() error { _, err := cache.GetTable(ctx, bad, "users"); return err }},
		{"GetTable_badTable", func() error { _, err := cache.GetTable(ctx, "good", bad); return err }},
		{"ListViews", func() error { _, err := cache.ListViews(ctx, bad); return err }},
		{"ListFunctions", func() error { _, err := cache.ListFunctions(ctx, bad); return err }},
		{"ListProcedures", func() error { _, err := cache.ListProcedures(ctx, bad); return err }},
		{"ListTriggers", func() error { _, err := cache.ListTriggers(ctx, bad); return err }},
		{"ListSchemas", func() error { _, err := cache.ListSchemas(ctx, bad); return err }},
	}
	for _, tc := range calls {
		err := tc.fn()
		if err == nil {
			t.Errorf("%s with bad identifier: want error, got nil", tc.name)
			continue
		}
		if !errors.Is(err, schema.ErrInvalidIdentifier) {
			t.Errorf("%s: err = %v, want ErrInvalidIdentifier", tc.name, err)
		}
	}

	// Inner reader must NOT have been called.
	c.mu.Lock()
	queryCount := len(c.queryLog)
	c.mu.Unlock()
	if queryCount != 0 {
		t.Errorf("bad-identifier calls reached driver: %d queries", queryCount)
	}
}

// TestCache_NoDuplicateLruEntriesOnTTLRefresh ensures that repeated
// reads of the same key after TTL expiry don't grow lruOrder beyond
// the number of distinct entries. Regression test for H4.
func TestCache_NoDuplicateLruEntriesOnTTLRefresh(t *testing.T) {
	c := newStubConn()
	const tablesSQL = `
		SELECT table_name, table_type, ifnull(table_comment, ''),
		       ifnull(table_rows, 0), ifnull(engine, '')
		FROM information_schema.tables
		WHERE table_schema = ?
		ORDER BY table_name`
	c.on(tablesSQL, stubResult{
		rows: [][]any{{"t", "BASE TABLE", "", int64(0), "InnoDB"}},
	})
	r, _ := schema.For(c, dbadmin.EngineMariaDB)
	cache := schema.NewCache(r, schema.CacheConfig{TTL: 10 * time.Millisecond, MaxEntries: 100})

	deadline := time.Now().Add(200 * time.Millisecond)
	calls := 0
	for time.Now().Before(deadline) && calls < 40 {
		if _, err := cache.ListTables(context.Background(), "alpha"); err != nil {
			t.Fatal(err)
		}
		calls++
		time.Sleep(12 * time.Millisecond)
	}
	if calls < 5 {
		t.Fatalf("expected at least 5 TTL-refresh cycles; got %d", calls)
	}

	entries, lru := schema.CacheSizes(cache)
	if entries != lru {
		t.Errorf("lruOrder duplicate-append leak: entries=%d, lruOrder=%d (want equal)", entries, lru)
	}
	if entries != 1 {
		t.Errorf("expected exactly 1 cached entry (one key, refreshed); got entries=%d", entries)
	}
}

// TestCache_InvalidateDuringLoadDropsStale forces an Invalidate to run
// while a slow load is in flight; the result of the slow load must NOT
// land in the cache (otherwise stale data overwrites the invalidation).
// Regression test for H7.
func TestCache_InvalidateDuringLoadDropsStale(t *testing.T) {
	slow := &slowReader{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
		table:   &schema.Table{Schema: "alpha", Name: "users"},
	}
	cache := schema.NewCache(slow, schema.CacheConfig{TTL: time.Hour, MaxEntries: 100})

	// Kick off the slow load.
	type res struct {
		t   *schema.Table
		err error
	}
	done := make(chan res, 1)
	go func() {
		t, err := cache.GetTable(context.Background(), "alpha", "users")
		done <- res{t, err}
	}()

	// Wait until the load is in flight, then invalidate.
	<-slow.started
	cache.Invalidate("alpha")
	// Now let the load complete.
	close(slow.release)

	r := <-done
	if r.err != nil {
		t.Fatalf("GetTable: %v", r.err)
	}
	// The caller still got their value back (load completed); the key
	// question is whether the cache absorbed it. It must NOT have.
	entries, _ := schema.CacheSizes(cache)
	if entries != 0 {
		t.Errorf("Invalidate during load left %d entries in cache; want 0 (stale value leaked)", entries)
	}
}

// slowReader is a minimal Reader stub whose GetTable blocks until
// release is closed. Used to create the Invalidate-vs-load race
// window in tests.
type slowReader struct {
	started chan struct{}
	release chan struct{}
	table   *schema.Table
}

func (s *slowReader) Engine() dbadmin.EngineKind { return dbadmin.EngineMariaDB }
func (s *slowReader) ListDatabases(ctx context.Context) ([]string, error) {
	return nil, nil
}
func (s *slowReader) ListSchemas(ctx context.Context, _ string) ([]string, error) {
	return nil, nil
}
func (s *slowReader) ListTables(ctx context.Context, _ string) ([]schema.TableSummary, error) {
	return nil, nil
}
func (s *slowReader) GetTable(ctx context.Context, _, _ string) (*schema.Table, error) {
	select {
	case s.started <- struct{}{}:
	default:
	}
	<-s.release
	return s.table, nil
}
func (s *slowReader) ListViews(ctx context.Context, _ string) ([]schema.ViewSummary, error) {
	return nil, nil
}
func (s *slowReader) ListFunctions(ctx context.Context, _ string) ([]schema.FunctionSummary, error) {
	return nil, nil
}
func (s *slowReader) ListProcedures(ctx context.Context, _ string) ([]schema.ProcedureSummary, error) {
	return nil, nil
}
func (s *slowReader) ListTriggers(ctx context.Context, _ string) ([]schema.TriggerSummary, error) {
	return nil, nil
}

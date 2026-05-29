package rows_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/driver"
	"github.com/auracp/auracp/pkg/dbadmin/rows"
	"github.com/auracp/auracp/pkg/dbadmin/schema"
)

// ─── Stub plumbing ───────────────────────────────────────────────────

// stubConn captures the SQL + args it's asked to execute, and returns
// pre-configured results.
type stubConn struct {
	mu        sync.Mutex
	queryLog  []recordedCall
	execLog   []recordedCall
	queryResp [][]any
	queryCols []driver.ColumnInfo
	queryErr  error
	execResp  driver.Result
	execErr   error

	// nextOverride, when non-nil, replaces the default stubRows.Next
	// behavior so tests can inject driver.ErrCapped / partial-result
	// scenarios (used by the H1 / PR #5.5 tests).
	nextOverride func(idx int) ([]any, error)
}

type recordedCall struct {
	sql  string
	args []any
}

func newStubConn() *stubConn { return &stubConn{} }

func (c *stubConn) Query(ctx context.Context, _ driver.Limits, sqlText string, args ...any) (driver.Rows, error) {
	c.mu.Lock()
	c.queryLog = append(c.queryLog, recordedCall{sql: sqlText, args: append([]any{}, args...)})
	c.mu.Unlock()
	if c.queryErr != nil {
		return nil, c.queryErr
	}
	return &stubRows{rows: c.queryResp, cols: c.queryCols, override: c.nextOverride}, nil
}

func (c *stubConn) Exec(ctx context.Context, _ driver.Limits, sqlText string, args ...any) (driver.Result, error) {
	c.mu.Lock()
	c.execLog = append(c.execLog, recordedCall{sql: sqlText, args: append([]any{}, args...)})
	c.mu.Unlock()
	if c.execErr != nil {
		return driver.Result{}, c.execErr
	}
	return c.execResp, nil
}

func (c *stubConn) Ping(ctx context.Context) error                       { return nil }
func (c *stubConn) ServerVersion(ctx context.Context) (string, error)    { return "stub", nil }
func (c *stubConn) Close() error                                         { return nil }

type stubRows struct {
	rows     [][]any
	cols     []driver.ColumnInfo
	idx      int
	override func(idx int) ([]any, error)
}

func (r *stubRows) Columns() []driver.ColumnInfo { return r.cols }
func (r *stubRows) Next(ctx context.Context) ([]any, error) {
	if r.override != nil {
		row, err := r.override(r.idx)
		r.idx++
		return row, err
	}
	if r.idx >= len(r.rows) {
		return nil, driver.ErrEOF
	}
	row := r.rows[r.idx]
	r.idx++
	return row, nil
}
func (r *stubRows) Close() error { return nil }

// stubSchema implements schema.Reader for tests.
type stubSchema struct {
	engine dbadmin.EngineKind
	tables map[string]*schema.Table
}

func newStubSchema(engine dbadmin.EngineKind) *stubSchema {
	return &stubSchema{engine: engine, tables: make(map[string]*schema.Table)}
}

func (s *stubSchema) withTable(name string, t *schema.Table) *stubSchema {
	s.tables[name] = t
	return s
}

func (s *stubSchema) Engine() dbadmin.EngineKind { return s.engine }
func (s *stubSchema) ListDatabases(ctx context.Context) ([]string, error) { return nil, nil }
func (s *stubSchema) ListSchemas(ctx context.Context, _ string) ([]string, error) {
	return nil, nil
}
func (s *stubSchema) ListTables(ctx context.Context, _ string) ([]schema.TableSummary, error) {
	return nil, nil
}
func (s *stubSchema) GetTable(ctx context.Context, schemaName, table string) (*schema.Table, error) {
	if t, ok := s.tables[schemaName+"."+table]; ok {
		return t, nil
	}
	return nil, schema.ErrTableNotFound
}
func (s *stubSchema) ListViews(ctx context.Context, _ string) ([]schema.ViewSummary, error) {
	return nil, nil
}
func (s *stubSchema) ListFunctions(ctx context.Context, _ string) ([]schema.FunctionSummary, error) {
	return nil, nil
}
func (s *stubSchema) ListProcedures(ctx context.Context, _ string) ([]schema.ProcedureSummary, error) {
	return nil, nil
}
func (s *stubSchema) ListTriggers(ctx context.Context, _ string) ([]schema.TriggerSummary, error) {
	return nil, nil
}

// ─── Read tests ──────────────────────────────────────────────────────

func TestRead_BasicMySQL(t *testing.T) {
	c := newStubConn()
	c.queryResp = [][]any{{int64(1), "alice"}, {int64(2), "bob"}}
	c.queryCols = []driver.ColumnInfo{{Name: "id"}, {Name: "name"}}

	s := newStubSchema(dbadmin.EngineMariaDB).withTable("app.users", &schema.Table{
		Schema: "app", Name: "users",
		Columns:    []schema.Column{{Name: "id"}, {Name: "name"}, {Name: "email"}},
		PrimaryKey: []string{"id"},
	})

	op, err := rows.New(c, s, rows.Options{
		Limits: driver.Limits{MaxRows: 100, Timeout: 5 * time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}

	r, err := op.Read(context.Background(), rows.ReadOpts{
		Schema:  "app",
		Table:   "users",
		Columns: []string{"id", "name"},
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Rows) != 2 {
		t.Errorf("got %d rows, want 2", len(r.Rows))
	}
	if len(c.queryLog) != 1 {
		t.Fatalf("expected 1 Query call, got %d", len(c.queryLog))
	}
	got := normalize(c.queryLog[0].sql)
	// H1: Read asks the backend for LIMIT+1 so it can detect overflow.
	want := "SELECT `id`, `name` FROM `app`.`users` LIMIT 11"
	if got != want {
		t.Errorf("SQL = %q,\n want %q", got, want)
	}
}

func TestRead_Postgres_FilterSortLimitOffset(t *testing.T) {
	c := newStubConn()
	s := newStubSchema(dbadmin.EnginePostgres).withTable("public.users", &schema.Table{
		Columns: []schema.Column{{Name: "id"}, {Name: "name"}},
	})
	op, _ := rows.New(c, s, rows.Options{
		Limits: driver.Limits{MaxRows: 1000, Timeout: 5 * time.Second},
	})
	_, err := op.Read(context.Background(), rows.ReadOpts{
		Schema:  "public",
		Table:   "users",
		Columns: []string{"id", "name"},
		Filter: []rows.Predicate{
			{Column: "name", Op: rows.OpILike, Value: "%a%"},
			{Column: "id", Op: rows.OpGt, Value: int64(0)},
		},
		Sort:   []rows.SortKey{{Column: "name", Descending: false}},
		Limit:  50,
		Offset: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := normalize(c.queryLog[0].sql)
	// H1: LIMIT+1.
	want := `SELECT "id", "name" FROM "public"."users" WHERE "name" ILIKE $1 AND "id" > $2 ORDER BY "name" ASC LIMIT 51 OFFSET 100`
	if got != want {
		t.Errorf("SQL = %q\n want %q", got, want)
	}
	if len(c.queryLog[0].args) != 2 {
		t.Errorf("args len = %d, want 2", len(c.queryLog[0].args))
	}
}

func TestRead_RejectsBadIdentifier(t *testing.T) {
	c := newStubConn()
	s := newStubSchema(dbadmin.EngineMariaDB)
	op, _ := rows.New(c, s, rows.Options{
		Limits: driver.Limits{MaxRows: 100},
	})
	cases := []rows.ReadOpts{
		{Schema: "users; DROP TABLE x", Table: "t"},
		{Schema: "app", Table: "users; DROP TABLE x"},
		{Schema: "app", Table: "users", Columns: []string{"id;"}},
		{Schema: "app", Table: "users", Filter: []rows.Predicate{{Column: "id;", Op: rows.OpEq, Value: 1}}},
		{Schema: "app", Table: "users", Sort: []rows.SortKey{{Column: "id;"}}},
	}
	for i, opts := range cases {
		_, err := op.Read(context.Background(), opts)
		if !errors.Is(err, schema.ErrInvalidIdentifier) {
			t.Errorf("case %d: err = %v, want ErrInvalidIdentifier", i, err)
		}
	}
	if len(c.queryLog) != 0 {
		t.Errorf("expected 0 queries for rejected inputs, got %d", len(c.queryLog))
	}
}

func TestRead_RejectsBadOp(t *testing.T) {
	c := newStubConn()
	s := newStubSchema(dbadmin.EngineMariaDB)
	op, _ := rows.New(c, s, rows.Options{
		Limits: driver.Limits{MaxRows: 100},
	})
	_, err := op.Read(context.Background(), rows.ReadOpts{
		Schema:  "app",
		Table:   "users",
		Columns: []string{"id"},
		Filter:  []rows.Predicate{{Column: "id", Op: "; DROP TABLE x; --", Value: 1}},
		Limit:   10,
	})
	if !errors.Is(err, rows.ErrInvalidPredicate) {
		t.Errorf("err = %v, want ErrInvalidPredicate", err)
	}
}

func TestRead_RejectsOversizeLimit(t *testing.T) {
	c := newStubConn()
	s := newStubSchema(dbadmin.EngineMariaDB)
	op, _ := rows.New(c, s, rows.Options{
		Limits: driver.Limits{MaxRows: 100},
	})
	_, err := op.Read(context.Background(), rows.ReadOpts{
		Schema:  "app",
		Table:   "users",
		Columns: []string{"id"},
		Limit:   10_000,
	})
	if !errors.Is(err, rows.ErrRowCapExceeded) {
		t.Errorf("err = %v, want ErrRowCapExceeded", err)
	}
}

func TestRead_IsNullAndIsNotNull(t *testing.T) {
	c := newStubConn()
	s := newStubSchema(dbadmin.EngineMariaDB).withTable("app.t", &schema.Table{
		Columns: []schema.Column{{Name: "x"}},
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	_, err := op.Read(context.Background(), rows.ReadOpts{
		Schema:  "app",
		Table:   "t",
		Columns: []string{"x"},
		Filter: []rows.Predicate{
			{Column: "x", Op: rows.OpIsNull},
			{Column: "x", Op: rows.OpIsNotNull},
		},
		Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := normalize(c.queryLog[0].sql)
	// H1: LIMIT+1.
	want := "SELECT `x` FROM `app`.`t` WHERE `x` IS NULL AND `x` IS NOT NULL LIMIT 11"
	if got != want {
		t.Errorf("SQL = %q\n want %q", got, want)
	}
	if len(c.queryLog[0].args) != 0 {
		t.Errorf("IS NULL clauses bind 0 args, got %d", len(c.queryLog[0].args))
	}
}

func TestRead_InWithSlice(t *testing.T) {
	c := newStubConn()
	s := newStubSchema(dbadmin.EnginePostgres).withTable("public.t", &schema.Table{
		Columns: []schema.Column{{Name: "id"}},
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	_, err := op.Read(context.Background(), rows.ReadOpts{
		Schema:  "public",
		Table:   "t",
		Columns: []string{"id"},
		Filter:  []rows.Predicate{{Column: "id", Op: rows.OpIn, Value: []int{1, 2, 3}}},
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := normalize(c.queryLog[0].sql)
	// H1: LIMIT+1.
	want := `SELECT "id" FROM "public"."t" WHERE "id" IN ($1, $2, $3) LIMIT 11`
	if got != want {
		t.Errorf("SQL = %q\n want %q", got, want)
	}
	if len(c.queryLog[0].args) != 3 {
		t.Errorf("args = %v, want 3", c.queryLog[0].args)
	}
}

func TestRead_EmptyIn(t *testing.T) {
	c := newStubConn()
	s := newStubSchema(dbadmin.EngineMariaDB).withTable("a.t", &schema.Table{Columns: []schema.Column{{Name: "x"}}})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	_, err := op.Read(context.Background(), rows.ReadOpts{
		Schema: "a", Table: "t", Columns: []string{"x"},
		Filter: []rows.Predicate{{Column: "x", Op: rows.OpIn, Value: []int{}}},
		Limit:  10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.queryLog[0].sql, "1=0") {
		t.Errorf("empty IN didn't emit 1=0: %q", c.queryLog[0].sql)
	}
}

// ─── Update tests ────────────────────────────────────────────────────

func TestUpdateByPK_HappyPath_MySQL(t *testing.T) {
	c := newStubConn()
	c.execResp = driver.Result{RowsAffected: 1}
	s := newStubSchema(dbadmin.EngineMariaDB).withTable("app.users", &schema.Table{
		Columns:    []schema.Column{{Name: "id"}, {Name: "name"}, {Name: "email"}},
		PrimaryKey: []string{"id"},
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})

	res, err := op.UpdateByPK(context.Background(), rows.UpdateByPKOpts{
		Schema: "app", Table: "users",
		PK:  map[string]any{"id": int64(42)},
		Set: map[string]any{"name": "alice", "email": "a@x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.RowsAffected != 1 {
		t.Errorf("RowsAffected = %d, want 1", res.RowsAffected)
	}
	got := normalize(c.execLog[0].sql)
	// SET keys are sorted alphabetically (email, name).
	want := "UPDATE `app`.`users` SET `email` = ?, `name` = ? WHERE `id` = ?"
	if got != want {
		t.Errorf("SQL = %q\n want %q", got, want)
	}
}

func TestUpdateByPK_CompositePK(t *testing.T) {
	c := newStubConn()
	c.execResp = driver.Result{RowsAffected: 1}
	s := newStubSchema(dbadmin.EnginePostgres).withTable("public.audit", &schema.Table{
		Columns:    []schema.Column{{Name: "tenant_id"}, {Name: "event_id"}, {Name: "status"}},
		PrimaryKey: []string{"tenant_id", "event_id"},
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})

	_, err := op.UpdateByPK(context.Background(), rows.UpdateByPKOpts{
		Schema: "public", Table: "audit",
		PK:  map[string]any{"tenant_id": int64(1), "event_id": int64(2)},
		Set: map[string]any{"status": "done"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := normalize(c.execLog[0].sql)
	// PK order from table's PrimaryKey slice: tenant_id, event_id.
	want := `UPDATE "public"."audit" SET "status" = $1 WHERE "tenant_id" = $2 AND "event_id" = $3`
	if got != want {
		t.Errorf("SQL = %q\n want %q", got, want)
	}
}

func TestUpdateByPK_NoPK_Refuses(t *testing.T) {
	c := newStubConn()
	s := newStubSchema(dbadmin.EngineMariaDB).withTable("a.t", &schema.Table{
		Columns: []schema.Column{{Name: "x"}},
		// No PrimaryKey set.
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	_, err := op.UpdateByPK(context.Background(), rows.UpdateByPKOpts{
		Schema: "a", Table: "t",
		PK:  map[string]any{"x": 1},
		Set: map[string]any{"x": 2},
	})
	if !errors.Is(err, rows.ErrNoPrimaryKey) {
		t.Errorf("err = %v, want ErrNoPrimaryKey", err)
	}
}

func TestUpdateByPK_EmptySet_Refuses(t *testing.T) {
	c := newStubConn()
	s := newStubSchema(dbadmin.EngineMariaDB).withTable("a.t", &schema.Table{
		Columns: []schema.Column{{Name: "id"}}, PrimaryKey: []string{"id"},
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	_, err := op.UpdateByPK(context.Background(), rows.UpdateByPKOpts{
		Schema: "a", Table: "t",
		PK:  map[string]any{"id": 1},
		Set: map[string]any{},
	})
	if !errors.Is(err, rows.ErrEmptyUpdate) {
		t.Errorf("err = %v, want ErrEmptyUpdate", err)
	}
}

func TestUpdateByPK_PKMismatch_Refuses(t *testing.T) {
	c := newStubConn()
	s := newStubSchema(dbadmin.EngineMariaDB).withTable("a.t", &schema.Table{
		Columns:    []schema.Column{{Name: "id"}},
		PrimaryKey: []string{"id"},
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})

	// Wrong column name.
	_, err := op.UpdateByPK(context.Background(), rows.UpdateByPKOpts{
		Schema: "a", Table: "t",
		PK:  map[string]any{"wrong_col": 1},
		Set: map[string]any{"id": 2},
	})
	if !errors.Is(err, rows.ErrPKMismatch) {
		t.Errorf("err = %v, want ErrPKMismatch", err)
	}

	// Too few columns for a composite PK.
	s2 := newStubSchema(dbadmin.EngineMariaDB).withTable("a.t", &schema.Table{
		Columns:    []schema.Column{{Name: "a"}, {Name: "b"}},
		PrimaryKey: []string{"a", "b"},
	})
	op2, _ := rows.New(c, s2, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	_, err = op2.UpdateByPK(context.Background(), rows.UpdateByPKOpts{
		Schema: "a", Table: "t",
		PK:  map[string]any{"a": 1},
		Set: map[string]any{"a": 2},
	})
	if !errors.Is(err, rows.ErrPKMismatch) {
		t.Errorf("err = %v, want ErrPKMismatch on composite-PK mismatch", err)
	}
}

// ─── Delete tests ────────────────────────────────────────────────────

func TestDeleteByPK_HappyPath(t *testing.T) {
	c := newStubConn()
	c.execResp = driver.Result{RowsAffected: 1}
	s := newStubSchema(dbadmin.EngineMariaDB).withTable("app.users", &schema.Table{
		Columns: []schema.Column{{Name: "id"}}, PrimaryKey: []string{"id"},
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})

	res, err := op.DeleteByPK(context.Background(), rows.DeleteByPKOpts{
		Schema: "app", Table: "users",
		PK: map[string]any{"id": int64(42)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.RowsAffected != 1 {
		t.Errorf("RowsAffected = %d, want 1", res.RowsAffected)
	}
	got := normalize(c.execLog[0].sql)
	want := "DELETE FROM `app`.`users` WHERE `id` = ?"
	if got != want {
		t.Errorf("SQL = %q\n want %q", got, want)
	}
}

func TestDeleteByPK_NoPK_Refuses(t *testing.T) {
	c := newStubConn()
	s := newStubSchema(dbadmin.EngineMariaDB).withTable("a.t", &schema.Table{
		Columns: []schema.Column{{Name: "x"}},
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	_, err := op.DeleteByPK(context.Background(), rows.DeleteByPKOpts{
		Schema: "a", Table: "t",
		PK: map[string]any{"x": 1},
	})
	if !errors.Is(err, rows.ErrNoPrimaryKey) {
		t.Errorf("err = %v, want ErrNoPrimaryKey", err)
	}
}

// ─── Insert tests ────────────────────────────────────────────────────

func TestInsert_HappyPath(t *testing.T) {
	c := newStubConn()
	c.execResp = driver.Result{RowsAffected: 1, LastInsertID: 99}
	s := newStubSchema(dbadmin.EngineMariaDB)
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})

	res, err := op.Insert(context.Background(), rows.InsertOpts{
		Schema: "app", Table: "users",
		Values: map[string]any{"name": "alice", "email": "a@x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.LastInsertID != 99 {
		t.Errorf("LastInsertID = %d, want 99", res.LastInsertID)
	}
	got := normalize(c.execLog[0].sql)
	// Keys sorted alphabetically: email, name.
	want := "INSERT INTO `app`.`users` (`email`, `name`) VALUES (?, ?)"
	if got != want {
		t.Errorf("SQL = %q\n want %q", got, want)
	}
}

func TestInsert_EmptyValues_Refuses(t *testing.T) {
	c := newStubConn()
	s := newStubSchema(dbadmin.EngineMariaDB)
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	_, err := op.Insert(context.Background(), rows.InsertOpts{
		Schema: "a", Table: "t", Values: map[string]any{},
	})
	if err == nil || !strings.Contains(err.Error(), "at least one") {
		t.Errorf("err = %v, want 'at least one column value'", err)
	}
}

// ─── Count test ──────────────────────────────────────────────────────

func TestCount_BasicMySQL(t *testing.T) {
	c := newStubConn()
	c.queryResp = [][]any{{int64(1234)}}
	c.queryCols = []driver.ColumnInfo{{Name: "count"}}
	s := newStubSchema(dbadmin.EngineMariaDB)
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})

	n, err := op.Count(context.Background(), rows.ReadOpts{
		Schema: "app", Table: "users",
		Filter: []rows.Predicate{{Column: "active", Op: rows.OpEq, Value: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1234 {
		t.Errorf("count = %d, want 1234", n)
	}
	got := normalize(c.queryLog[0].sql)
	want := "SELECT COUNT(*) FROM `app`.`users` WHERE `active` = ?"
	if got != want {
		t.Errorf("SQL = %q\n want %q", got, want)
	}
}

// ─── Operator construction ──────────────────────────────────────────

func TestNew_RejectsNilConn(t *testing.T) {
	s := newStubSchema(dbadmin.EngineMariaDB)
	_, err := rows.New(nil, s, rows.Options{})
	if err == nil {
		t.Error("expected error for nil Conn")
	}
}

func TestNew_RejectsNilSchema(t *testing.T) {
	c := newStubConn()
	_, err := rows.New(c, nil, rows.Options{})
	if err == nil {
		t.Error("expected error for nil Reader")
	}
}

func TestNew_RejectsUnknownEngine(t *testing.T) {
	c := newStubConn()
	// Reader reports an unknown engine; New must reject.
	s := newStubSchema(dbadmin.EngineKind(99))
	_, err := rows.New(c, s, rows.Options{})
	if err == nil {
		t.Error("expected error for unknown engine")
	}
}

// TestNew_DerivesEngineFromReader confirms a Postgres-engine reader
// produces an Operator that quotes with double-quotes in the SQL it
// generates (and not backticks). This is the contract for H7: the
// caller does not get to override the reader's engine.
func TestNew_DerivesEngineFromReader(t *testing.T) {
	c := newStubConn()
	c.queryCols = []driver.ColumnInfo{{Name: "id"}}
	s := newStubSchema(dbadmin.EnginePostgres).withTable("public.t", &schema.Table{
		Columns: []schema.Column{{Name: "id"}},
	})
	op, err := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = op.Read(context.Background(), rows.ReadOpts{
		Schema: "public", Table: "t", Columns: []string{"id"}, Limit: 5,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(c.queryLog) != 1 {
		t.Fatalf("expected 1 query, got %d", len(c.queryLog))
	}
	got := c.queryLog[0].sql
	if !strings.Contains(got, `"public"."t"`) {
		t.Errorf("expected Postgres double-quote quoting, got %q", got)
	}
	if strings.Contains(got, "`") {
		t.Errorf("expected NO backtick quoting in Postgres SQL, got %q", got)
	}
}

// TestRead_RejectsNegativeLimit asserts that Limit:-1 returns an
// error (not silent fallback to MaxRows). Required by H3.
func TestRead_RejectsNegativeLimit(t *testing.T) {
	c := newStubConn()
	s := newStubSchema(dbadmin.EngineMariaDB).withTable("a.t", &schema.Table{
		Columns: []schema.Column{{Name: "x"}},
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	_, err := op.Read(context.Background(), rows.ReadOpts{
		Schema: "a", Table: "t", Columns: []string{"x"},
		Limit: -1,
	})
	if err == nil {
		t.Fatal("expected error for negative Limit, got nil")
	}
	if !strings.Contains(err.Error(), "Limit must be >= 0") {
		t.Errorf("err = %v, want message about Limit >= 0", err)
	}
	if len(c.queryLog) != 0 {
		t.Errorf("expected no query for rejected Limit, got %d", len(c.queryLog))
	}
}

// TestCount_RejectsUnexpectedType uses a stub returning struct{}{} and
// asserts Count surfaces the toInt64 error rather than silently
// returning 0. Required by H4.
func TestCount_RejectsUnexpectedType(t *testing.T) {
	c := newStubConn()
	c.queryResp = [][]any{{struct{}{}}}
	c.queryCols = []driver.ColumnInfo{{Name: "count"}}
	s := newStubSchema(dbadmin.EngineMariaDB)
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})

	_, err := op.Count(context.Background(), rows.ReadOpts{
		Schema: "app", Table: "users",
	})
	if err == nil {
		t.Fatal("expected error for unexpected COUNT type, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected COUNT result type") {
		t.Errorf("err = %v, want 'unexpected COUNT result type'", err)
	}
}

// ─── Helpers ────────────────────────────────────────────────────────

// normalize collapses whitespace so SQL comparisons aren't fragile.
func normalize(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

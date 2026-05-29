package rows_test

// PR #5.5 — engine-parity & limits hardening.
//
// These tests cover the deferred PR #5 findings tracked in
// docs/aura-db/KNOWN-ISSUES.md (H1, H2, H5, H6, H8, M3, M4, M5, M6,
// M7, M10, L2, L7, L11, L13, L14, N1).

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/driver"
	"github.com/auracp/auracp/pkg/dbadmin/rows"
	"github.com/auracp/auracp/pkg/dbadmin/schema"
)

// ─── H1: Read does not silently swallow ErrCapped ────────────────────

// TestH1_Read_CappedFlagOnOverflow asserts that when the underlying
// SELECT would return more rows than Limit, Read trims to Limit and
// sets Capped=true. The stub returns Limit+1 rows; we expect Capped to
// flip and len(Rows) == Limit.
func TestH1_Read_CappedFlagOnOverflow(t *testing.T) {
	c := newStubConn()
	// Return 4 rows; Limit is 3 → Read asks for LIMIT 4 and should
	// detect the +1th row, trim, and set Capped=true.
	c.queryResp = [][]any{{int64(1)}, {int64(2)}, {int64(3)}, {int64(4)}}
	c.queryCols = []driver.ColumnInfo{{Name: "id"}}
	s := newStubSchema(dbadmin.EngineMariaDB).withTable("a.t", &schema.Table{
		Columns: []schema.Column{{Name: "id"}},
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	r, err := op.Read(context.Background(), rows.ReadOpts{
		Schema: "a", Table: "t", Columns: []string{"id"}, Limit: 3,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !r.Capped {
		t.Errorf("Capped = false, want true (got %d rows back from stub)", len(r.Rows))
	}
	if len(r.Rows) != 3 {
		t.Errorf("Rows trimmed to %d, want 3", len(r.Rows))
	}
	// Confirm the SQL asked the backend for LIMIT 4 (Limit + 1).
	if !strings.Contains(c.queryLog[0].sql, "LIMIT 4") {
		t.Errorf("SQL = %q, expected LIMIT 4 (Limit+1)", c.queryLog[0].sql)
	}
}

// TestH1_Read_NotCappedWhenAtLimitExactly asserts that when the table
// has EXACTLY Limit rows, Capped is false (no false positives).
func TestH1_Read_NotCappedWhenAtLimitExactly(t *testing.T) {
	c := newStubConn()
	c.queryResp = [][]any{{int64(1)}, {int64(2)}, {int64(3)}}
	c.queryCols = []driver.ColumnInfo{{Name: "id"}}
	s := newStubSchema(dbadmin.EngineMariaDB).withTable("a.t", &schema.Table{
		Columns: []schema.Column{{Name: "id"}},
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	r, err := op.Read(context.Background(), rows.ReadOpts{
		Schema: "a", Table: "t", Columns: []string{"id"}, Limit: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Capped {
		t.Errorf("Capped = true, want false (table happened to have exactly Limit rows)")
	}
	if len(r.Rows) != 3 {
		t.Errorf("Rows = %d, want 3", len(r.Rows))
	}
}

// TestH1_Read_HandlesDriverErrCapped asserts that if the driver
// surfaces ErrCapped (e.g. MaxBytes hit before LIMIT+1), Read treats
// it as a clean stop with Capped=true, NOT a bubbled error.
func TestH1_Read_HandlesDriverErrCapped(t *testing.T) {
	c := newStubConn()
	c.queryResp = [][]any{{int64(1)}, {int64(2)}}
	c.queryCols = []driver.ColumnInfo{{Name: "id"}}
	c.nextOverride = func(idx int) ([]any, error) {
		if idx >= 2 {
			return nil, driver.ErrCapped
		}
		return c.queryResp[idx], nil
	}
	s := newStubSchema(dbadmin.EngineMariaDB).withTable("a.t", &schema.Table{
		Columns: []schema.Column{{Name: "id"}},
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	r, err := op.Read(context.Background(), rows.ReadOpts{
		Schema: "a", Table: "t", Columns: []string{"id"}, Limit: 5,
	})
	if err != nil {
		t.Fatalf("ErrCapped should be a clean stop, got err: %v", err)
	}
	if !r.Capped {
		t.Errorf("Capped = false, want true (driver returned ErrCapped)")
	}
	if len(r.Rows) != 2 {
		t.Errorf("Rows = %d, want 2", len(r.Rows))
	}
}

// ─── H2: CountByOpts dedicated input type ────────────────────────────

func TestH2_CountByOpts_BasicMySQL(t *testing.T) {
	c := newStubConn()
	c.queryResp = [][]any{{int64(99)}}
	c.queryCols = []driver.ColumnInfo{{Name: "count"}}
	s := newStubSchema(dbadmin.EngineMariaDB)
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	n, err := op.CountByOpts(context.Background(), rows.CountOpts{
		Schema: "app", Table: "users",
		Filter: []rows.Predicate{{Column: "active", Op: rows.OpEq, Value: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 99 {
		t.Errorf("count = %d, want 99", n)
	}
	got := strings.Join(strings.Fields(c.queryLog[0].sql), " ")
	want := "SELECT COUNT(*) FROM `app`.`users` WHERE `active` = ?"
	if got != want {
		t.Errorf("SQL = %q,\n want %q", got, want)
	}
}

func TestH2_CountByOpts_RejectsBadIdentifier(t *testing.T) {
	c := newStubConn()
	s := newStubSchema(dbadmin.EngineMariaDB)
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	_, err := op.CountByOpts(context.Background(), rows.CountOpts{
		Schema: "a; DROP TABLE x", Table: "t",
	})
	if !errors.Is(err, schema.ErrInvalidIdentifier) {
		t.Errorf("err = %v, want schema.ErrInvalidIdentifier", err)
	}
	_, err = op.CountByOpts(context.Background(), rows.CountOpts{
		Schema: "a", Table: "t",
		Filter: []rows.Predicate{{Column: "bad;col", Op: rows.OpEq, Value: 1}},
	})
	if !errors.Is(err, schema.ErrInvalidIdentifier) {
		t.Errorf("err = %v, want schema.ErrInvalidIdentifier (bad filter column)", err)
	}
	_, err = op.CountByOpts(context.Background(), rows.CountOpts{
		Schema: "a", Table: "t",
		Filter: []rows.Predicate{{Column: "id", Op: "; DROP", Value: 1}},
	})
	if !errors.Is(err, rows.ErrInvalidPredicate) {
		t.Errorf("err = %v, want rows.ErrInvalidPredicate (bad op)", err)
	}
}

// TestH2_Count_BackcompatShim asserts the old Count(ReadOpts) still
// works and forwards correctly.
func TestH2_Count_BackcompatShim(t *testing.T) {
	c := newStubConn()
	c.queryResp = [][]any{{int64(7)}}
	c.queryCols = []driver.ColumnInfo{{Name: "count"}}
	s := newStubSchema(dbadmin.EngineMariaDB)
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	n, err := op.Count(context.Background(), rows.ReadOpts{
		Schema: "a", Table: "t",
		// These fields are ignored — but the shim must not bubble.
		Columns: []string{"id"},
		Sort:    []rows.SortKey{{Column: "id"}},
		Limit:   5, Offset: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 7 {
		t.Errorf("n = %d, want 7", n)
	}
}

// ─── H5: IN list capped at maxInListSize ─────────────────────────────

func TestH5_OversizeInList_Rejected(t *testing.T) {
	c := newStubConn()
	s := newStubSchema(dbadmin.EnginePostgres).withTable("a.t", &schema.Table{
		Columns: []schema.Column{{Name: "id"}},
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	huge := make([]int64, 1001)
	for i := range huge {
		huge[i] = int64(i)
	}
	_, err := op.Read(context.Background(), rows.ReadOpts{
		Schema: "a", Table: "t", Columns: []string{"id"},
		Filter: []rows.Predicate{{Column: "id", Op: rows.OpIn, Value: huge}},
		Limit:  10,
	})
	if !errors.Is(err, rows.ErrInvalidPredicate) {
		t.Errorf("err = %v, want ErrInvalidPredicate (oversize IN list)", err)
	}
	if len(c.queryLog) != 0 {
		t.Errorf("expected 0 queries for rejected IN list, got %d", len(c.queryLog))
	}
}

// TestH5_AtMaxInListSize_Accepted asserts the boundary case: exactly
// maxInListSize entries should be accepted.
func TestH5_AtMaxInListSize_Accepted(t *testing.T) {
	c := newStubConn()
	c.queryCols = []driver.ColumnInfo{{Name: "id"}}
	s := newStubSchema(dbadmin.EnginePostgres).withTable("a.t", &schema.Table{
		Columns: []schema.Column{{Name: "id"}},
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 1500}})
	atCap := make([]int64, 1000)
	for i := range atCap {
		atCap[i] = int64(i)
	}
	_, err := op.Read(context.Background(), rows.ReadOpts{
		Schema: "a", Table: "t", Columns: []string{"id"},
		Filter: []rows.Predicate{{Column: "id", Op: rows.OpIn, Value: atCap}},
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("Read at IN-list cap should succeed, got err: %v", err)
	}
}

// ─── H6: Postgres Insert RETURNING <pk> ──────────────────────────────

func TestH6_PostgresInsert_UsesReturning(t *testing.T) {
	c := newStubConn()
	// RETURNING goes through Query, not Exec; pre-seed Query response.
	c.queryResp = [][]any{{int64(101)}}
	c.queryCols = []driver.ColumnInfo{{Name: "id"}}
	s := newStubSchema(dbadmin.EnginePostgres).withTable("public.users", &schema.Table{
		Columns:    []schema.Column{{Name: "id"}, {Name: "name"}},
		PrimaryKey: []string{"id"},
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})

	res, err := op.Insert(context.Background(), rows.InsertOpts{
		Schema: "public", Table: "users",
		Values: map[string]any{"name": "alice"},
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if res.LastInsertID != 101 {
		t.Errorf("LastInsertID = %d, want 101 (from RETURNING)", res.LastInsertID)
	}
	if len(c.queryLog) != 1 {
		t.Fatalf("expected 1 Query call (RETURNING goes through Query), got %d", len(c.queryLog))
	}
	if len(c.execLog) != 0 {
		t.Errorf("expected 0 Exec calls (Postgres single-PK Insert uses Query), got %d", len(c.execLog))
	}
	got := strings.Join(strings.Fields(c.queryLog[0].sql), " ")
	want := `INSERT INTO "public"."users" ("name") VALUES ($1) RETURNING "id"`
	if got != want {
		t.Errorf("SQL = %q,\n want %q", got, want)
	}
}

// TestH6_PostgresInsert_NoPK_FallsBackToExec asserts that when the
// target table has no PK, Insert falls back to plain Exec (no
// RETURNING) and LastInsertID stays 0.
func TestH6_PostgresInsert_NoPK_FallsBackToExec(t *testing.T) {
	c := newStubConn()
	c.execResp = driver.Result{RowsAffected: 1}
	s := newStubSchema(dbadmin.EnginePostgres).withTable("public.audit", &schema.Table{
		Columns: []schema.Column{{Name: "msg"}},
		// No PrimaryKey.
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	res, err := op.Insert(context.Background(), rows.InsertOpts{
		Schema: "public", Table: "audit",
		Values: map[string]any{"msg": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.execLog) != 1 {
		t.Errorf("expected 1 Exec, got %d", len(c.execLog))
	}
	if res.LastInsertID != 0 {
		t.Errorf("LastInsertID = %d, want 0 (no PK to RETURNING)", res.LastInsertID)
	}
}

// TestH6_PostgresInsert_CompositePK_FallsBackToExec asserts that a
// table with a composite PK falls back to plain Exec (RETURNING would
// need to project multiple columns + the row API surface only has
// LastInsertID:int64).
func TestH6_PostgresInsert_CompositePK_FallsBackToExec(t *testing.T) {
	c := newStubConn()
	c.execResp = driver.Result{RowsAffected: 1}
	s := newStubSchema(dbadmin.EnginePostgres).withTable("public.t", &schema.Table{
		Columns:    []schema.Column{{Name: "a"}, {Name: "b"}, {Name: "v"}},
		PrimaryKey: []string{"a", "b"},
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	_, err := op.Insert(context.Background(), rows.InsertOpts{
		Schema: "public", Table: "t",
		Values: map[string]any{"v": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.execLog) != 1 {
		t.Errorf("expected 1 Exec, got %d", len(c.execLog))
	}
	if len(c.queryLog) != 0 {
		t.Errorf("expected 0 Query (composite PK skips RETURNING), got %d", len(c.queryLog))
	}
}

// TestH6_MariaDBInsert_StaysOnExec asserts MariaDB Insert keeps using
// Exec (LAST_INSERT_ID() is reliable for AUTO_INCREMENT).
func TestH6_MariaDBInsert_StaysOnExec(t *testing.T) {
	c := newStubConn()
	c.execResp = driver.Result{RowsAffected: 1, LastInsertID: 42}
	s := newStubSchema(dbadmin.EngineMariaDB).withTable("app.users", &schema.Table{
		Columns:    []schema.Column{{Name: "id"}, {Name: "name"}},
		PrimaryKey: []string{"id"},
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	res, err := op.Insert(context.Background(), rows.InsertOpts{
		Schema: "app", Table: "users",
		Values: map[string]any{"name": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.execLog) != 1 {
		t.Fatalf("expected 1 Exec, got %d", len(c.execLog))
	}
	if len(c.queryLog) != 0 {
		t.Errorf("expected 0 Query on MariaDB Insert, got %d", len(c.queryLog))
	}
	if res.LastInsertID != 42 {
		t.Errorf("LastInsertID = %d, want 42", res.LastInsertID)
	}
}

// ─── M3: empty NOT IN is rejected ────────────────────────────────────

func TestM3_EmptyNotIn_Rejected(t *testing.T) {
	c := newStubConn()
	s := newStubSchema(dbadmin.EngineMariaDB).withTable("a.t", &schema.Table{
		Columns: []schema.Column{{Name: "x"}},
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	_, err := op.Read(context.Background(), rows.ReadOpts{
		Schema: "a", Table: "t", Columns: []string{"x"},
		Filter: []rows.Predicate{{Column: "x", Op: rows.OpNotIn, Value: []int{}}},
		Limit:  10,
	})
	if !errors.Is(err, rows.ErrInvalidPredicate) {
		t.Errorf("err = %v, want ErrInvalidPredicate (empty NOT IN)", err)
	}
	if len(c.queryLog) != 0 {
		t.Errorf("expected 0 queries for rejected empty NOT IN, got %d", len(c.queryLog))
	}
}

// ─── M4: NaN/Inf in IN list rejected ─────────────────────────────────

func TestM4_NaNInF64InList_Rejected(t *testing.T) {
	c := newStubConn()
	s := newStubSchema(dbadmin.EnginePostgres).withTable("a.t", &schema.Table{
		Columns: []schema.Column{{Name: "x"}},
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})

	cases := []any{
		[]float64{math.NaN()},
		[]float64{math.Inf(1)},
		[]float64{math.Inf(-1)},
		[]float32{float32(math.NaN())},
		[]any{math.NaN()},
		[]any{math.Inf(1)},
	}
	for i, val := range cases {
		_, err := op.Read(context.Background(), rows.ReadOpts{
			Schema: "a", Table: "t", Columns: []string{"x"},
			Filter: []rows.Predicate{{Column: "x", Op: rows.OpIn, Value: val}},
			Limit:  10,
		})
		if !errors.Is(err, rows.ErrInvalidPredicate) {
			t.Errorf("case %d (%T): err = %v, want ErrInvalidPredicate", i, val, err)
		}
	}
}

// ─── M5: additional slice element types accepted ─────────────────────

func TestM5_AdditionalSliceTypes_Accepted(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		val  any
	}{
		{"[]int32", []int32{1, 2, 3}},
		{"[]int16", []int16{1, 2, 3}},
		{"[]int8", []int8{1, 2, 3}},
		{"[]uint64", []uint64{1, 2, 3}},
		{"[]uint32", []uint32{1, 2, 3}},
		{"[]uint16", []uint16{1, 2, 3}},
		{"[]uint", []uint{1, 2, 3}},
		{"[]bool", []bool{true, false}},
		{"[]float32", []float32{1.5, 2.5}},
		{"[]time.Time", []time.Time{now, now}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newStubConn()
			c.queryCols = []driver.ColumnInfo{{Name: "x"}}
			s := newStubSchema(dbadmin.EnginePostgres).withTable("a.t", &schema.Table{
				Columns: []schema.Column{{Name: "x"}},
			})
			op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
			_, err := op.Read(context.Background(), rows.ReadOpts{
				Schema: "a", Table: "t", Columns: []string{"x"},
				Filter: []rows.Predicate{{Column: "x", Op: rows.OpIn, Value: tc.val}},
				Limit:  10,
			})
			if err != nil {
				t.Errorf("Read with %s IN list failed: %v", tc.name, err)
			}
		})
	}
}

// ─── M6: assertPKMatch rejects nil PK values ─────────────────────────

func TestM6_NilPKValue_Rejected(t *testing.T) {
	c := newStubConn()
	s := newStubSchema(dbadmin.EngineMariaDB).withTable("a.t", &schema.Table{
		Columns:    []schema.Column{{Name: "id"}, {Name: "v"}},
		PrimaryKey: []string{"id"},
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	_, err := op.UpdateByPK(context.Background(), rows.UpdateByPKOpts{
		Schema: "a", Table: "t",
		PK:  map[string]any{"id": nil},
		Set: map[string]any{"v": 1},
	})
	if !errors.Is(err, rows.ErrPKMismatch) {
		t.Errorf("err = %v, want ErrPKMismatch on nil PK value", err)
	}
	_, err = op.DeleteByPK(context.Background(), rows.DeleteByPKOpts{
		Schema: "a", Table: "t",
		PK: map[string]any{"id": nil},
	})
	if !errors.Is(err, rows.ErrPKMismatch) {
		t.Errorf("err = %v, want ErrPKMismatch on nil PK value (Delete)", err)
	}
}

// ─── M7: UpdateByPK refuses to mutate PK columns ─────────────────────

func TestM7_UpdateByPK_RefusesPKMutation(t *testing.T) {
	c := newStubConn()
	s := newStubSchema(dbadmin.EngineMariaDB).withTable("a.t", &schema.Table{
		Columns:    []schema.Column{{Name: "id"}, {Name: "v"}},
		PrimaryKey: []string{"id"},
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	_, err := op.UpdateByPK(context.Background(), rows.UpdateByPKOpts{
		Schema: "a", Table: "t",
		PK:  map[string]any{"id": int64(1)},
		Set: map[string]any{"id": int64(2)},
	})
	if !errors.Is(err, rows.ErrPKMutation) {
		t.Errorf("err = %v, want ErrPKMutation", err)
	}
	if len(c.execLog) != 0 {
		t.Errorf("expected 0 Exec calls for rejected PK mutation, got %d", len(c.execLog))
	}
}

// ─── M10: per-value size cap ─────────────────────────────────────────

func TestM10_ValueSizeCap_Update(t *testing.T) {
	c := newStubConn()
	s := newStubSchema(dbadmin.EngineMariaDB).withTable("a.t", &schema.Table{
		Columns:    []schema.Column{{Name: "id"}, {Name: "blob"}},
		PrimaryKey: []string{"id"},
	})
	op, _ := rows.New(c, s, rows.Options{
		Limits:        driver.Limits{MaxRows: 100},
		MaxValueBytes: 16,
	})
	big := strings.Repeat("x", 17)
	_, err := op.UpdateByPK(context.Background(), rows.UpdateByPKOpts{
		Schema: "a", Table: "t",
		PK:  map[string]any{"id": int64(1)},
		Set: map[string]any{"blob": big},
	})
	if !errors.Is(err, rows.ErrValueTooLarge) {
		t.Errorf("err = %v, want ErrValueTooLarge", err)
	}
}

func TestM10_ValueSizeCap_Insert_Bytes(t *testing.T) {
	c := newStubConn()
	s := newStubSchema(dbadmin.EngineMariaDB)
	op, _ := rows.New(c, s, rows.Options{
		Limits:        driver.Limits{MaxRows: 100},
		MaxValueBytes: 8,
	})
	_, err := op.Insert(context.Background(), rows.InsertOpts{
		Schema: "a", Table: "t",
		Values: map[string]any{"blob": []byte("123456789")},
	})
	if !errors.Is(err, rows.ErrValueTooLarge) {
		t.Errorf("err = %v, want ErrValueTooLarge", err)
	}
}

// ─── L2: nested-slice rejection ──────────────────────────────────────

func TestL2_NestedSliceInInList_Rejected(t *testing.T) {
	c := newStubConn()
	s := newStubSchema(dbadmin.EngineMariaDB).withTable("a.t", &schema.Table{
		Columns: []schema.Column{{Name: "x"}},
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	_, err := op.Read(context.Background(), rows.ReadOpts{
		Schema: "a", Table: "t", Columns: []string{"x"},
		Filter: []rows.Predicate{
			{Column: "x", Op: rows.OpIn, Value: []any{[]int{1, 2}, []int{3, 4}}},
		},
		Limit: 10,
	})
	if !errors.Is(err, rows.ErrInvalidPredicate) {
		t.Errorf("err = %v, want ErrInvalidPredicate (nested slice)", err)
	}
}

func TestL2_ByteSliceInInList_Rejected(t *testing.T) {
	c := newStubConn()
	s := newStubSchema(dbadmin.EngineMariaDB).withTable("a.t", &schema.Table{
		Columns: []schema.Column{{Name: "x"}},
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	_, err := op.Read(context.Background(), rows.ReadOpts{
		Schema: "a", Table: "t", Columns: []string{"x"},
		Filter: []rows.Predicate{
			{Column: "x", Op: rows.OpIn, Value: []byte("abc")},
		},
		Limit: 10,
	})
	if !errors.Is(err, rows.ErrInvalidPredicate) {
		t.Errorf("err = %v, want ErrInvalidPredicate ([]byte ambiguous)", err)
	}
}

// ─── L7: New() rejects negative Limits ───────────────────────────────

func TestL7_New_RejectsNegativeLimits(t *testing.T) {
	c := newStubConn()
	s := newStubSchema(dbadmin.EngineMariaDB)
	_, err := rows.New(c, s, rows.Options{
		Limits: driver.Limits{MaxRows: -1},
	})
	if err == nil {
		t.Error("expected error for negative MaxRows")
	}
	_, err = rows.New(c, s, rows.Options{
		Limits: driver.Limits{Timeout: -time.Second},
	})
	if err == nil {
		t.Error("expected error for negative Timeout")
	}
	_, err = rows.New(c, s, rows.Options{
		Limits: driver.Limits{MaxBytes: -1},
	})
	if err == nil {
		t.Error("expected error for negative MaxBytes")
	}
	_, err = rows.New(c, s, rows.Options{MaxValueBytes: -1})
	if err == nil {
		t.Error("expected error for negative MaxValueBytes")
	}
}

// ─── L11: deep OFFSET rejected ───────────────────────────────────────

func TestL11_DeepOffset_Rejected(t *testing.T) {
	c := newStubConn()
	s := newStubSchema(dbadmin.EngineMariaDB).withTable("a.t", &schema.Table{
		Columns: []schema.Column{{Name: "x"}},
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	_, err := op.Read(context.Background(), rows.ReadOpts{
		Schema: "a", Table: "t", Columns: []string{"x"},
		Limit: 10, Offset: 1_000_001,
	})
	if err == nil {
		t.Fatal("expected error for offset > maxOffset")
	}
	if !strings.Contains(err.Error(), "offset") {
		t.Errorf("err = %v, want 'offset' message", err)
	}
}

// ─── L13: rows.ErrInvalidIdentifier alias ────────────────────────────

func TestL13_ErrInvalidIdentifierAlias(t *testing.T) {
	// Bad identifier should be discoverable via rows.ErrInvalidIdentifier.
	c := newStubConn()
	s := newStubSchema(dbadmin.EngineMariaDB)
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	_, err := op.Read(context.Background(), rows.ReadOpts{
		Schema: "bad;col", Table: "t", Columns: []string{"x"}, Limit: 10,
	})
	if !errors.Is(err, rows.ErrInvalidIdentifier) {
		t.Errorf("err = %v, want rows.ErrInvalidIdentifier", err)
	}
	if !errors.Is(err, schema.ErrInvalidIdentifier) {
		t.Errorf("err = %v, want schema.ErrInvalidIdentifier (alias chain)", err)
	}
}

// ─── L14: composite-PK WHERE-order regression ────────────────────────

// TestL14_CompositePK_WhereOrder confirms the UPDATE / DELETE WHERE
// clauses emit PK conditions in the table's declared PK order, not
// the user-supplied map's iteration order (Go maps are unordered).
func TestL14_CompositePK_WhereOrder(t *testing.T) {
	c := newStubConn()
	c.execResp = driver.Result{RowsAffected: 1}
	// Declared PK order: tenant_id, event_id (NOT alphabetical).
	s := newStubSchema(dbadmin.EnginePostgres).withTable("public.audit", &schema.Table{
		Columns:    []schema.Column{{Name: "tenant_id"}, {Name: "event_id"}, {Name: "v"}},
		PrimaryKey: []string{"tenant_id", "event_id"},
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})

	// Run multiple iterations — Go map ordering is randomized per
	// process but should be defeated by the package's explicit
	// pkCols ordering.
	for i := 0; i < 50; i++ {
		c.execLog = c.execLog[:0]
		_, err := op.UpdateByPK(context.Background(), rows.UpdateByPKOpts{
			Schema: "public", Table: "audit",
			PK:  map[string]any{"event_id": int64(2), "tenant_id": int64(1)},
			Set: map[string]any{"v": "x"},
		})
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(strings.Fields(c.execLog[0].sql), " ")
		want := `UPDATE "public"."audit" SET "v" = $1 WHERE "tenant_id" = $2 AND "event_id" = $3`
		if got != want {
			t.Fatalf("iter %d: SQL = %q,\n want %q", i, got, want)
		}
	}

	// And on DELETE.
	for i := 0; i < 50; i++ {
		c.execLog = c.execLog[:0]
		_, err := op.DeleteByPK(context.Background(), rows.DeleteByPKOpts{
			Schema: "public", Table: "audit",
			PK: map[string]any{"event_id": int64(2), "tenant_id": int64(1)},
		})
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(strings.Fields(c.execLog[0].sql), " ")
		want := `DELETE FROM "public"."audit" WHERE "tenant_id" = $1 AND "event_id" = $2`
		if got != want {
			t.Fatalf("iter %d: SQL = %q,\n want %q", i, got, want)
		}
	}
}

// ─── N1: Op enum exhaustiveness ──────────────────────────────────────

// TestN1_OpEnumExhaustive walks every defined Op constant and asserts
// the package treats it as valid. Catches the "added a new Op but
// forgot to wire it into validateOp" regression.
func TestN1_OpEnumExhaustive(t *testing.T) {
	allOps := []rows.Op{
		rows.OpEq, rows.OpNeq, rows.OpLt, rows.OpLte, rows.OpGt, rows.OpGte,
		rows.OpLike, rows.OpILike, rows.OpIsNull, rows.OpIsNotNull,
		rows.OpIn, rows.OpNotIn,
	}
	c := newStubConn()
	c.queryCols = []driver.ColumnInfo{{Name: "x"}}
	s := newStubSchema(dbadmin.EnginePostgres).withTable("a.t", &schema.Table{
		Columns: []schema.Column{{Name: "x"}},
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	for _, o := range allOps {
		p := rows.Predicate{Column: "x", Op: o, Value: "v"}
		if o == rows.OpIn || o == rows.OpNotIn {
			p.Value = []any{"a", "b"}
		}
		_, err := op.Read(context.Background(), rows.ReadOpts{
			Schema: "a", Table: "t", Columns: []string{"x"},
			Filter: []rows.Predicate{p},
			Limit:  5,
		})
		if errors.Is(err, rows.ErrInvalidPredicate) {
			t.Errorf("Op %q rejected by validateOp", o)
		}
	}
}

// TestN1_UnknownOp_Rejected complements the exhaustiveness check.
func TestN1_UnknownOp_Rejected(t *testing.T) {
	c := newStubConn()
	s := newStubSchema(dbadmin.EngineMariaDB).withTable("a.t", &schema.Table{
		Columns: []schema.Column{{Name: "x"}},
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	_, err := op.Read(context.Background(), rows.ReadOpts{
		Schema: "a", Table: "t", Columns: []string{"x"},
		Filter: []rows.Predicate{{Column: "x", Op: rows.Op("BETWEEN"), Value: 1}},
		Limit:  5,
	})
	if !errors.Is(err, rows.ErrInvalidPredicate) {
		t.Errorf("err = %v, want ErrInvalidPredicate (unknown Op)", err)
	}
}

// ─── H8: doc-only — sanity-check OpLike + OpILike still build ────────

// TestH8_OpLike_BuildsOnBothEngines pins down the OpLike SQL across
// engines. The case-sensitivity divergence is documented (see
// doc.go) but the engines do still both accept LIKE.
func TestH8_OpLike_BuildsOnBothEngines(t *testing.T) {
	for _, engine := range []dbadmin.EngineKind{dbadmin.EngineMariaDB, dbadmin.EnginePostgres} {
		c := newStubConn()
		c.queryCols = []driver.ColumnInfo{{Name: "name"}}
		s := newStubSchema(engine).withTable("a.t", &schema.Table{
			Columns: []schema.Column{{Name: "name"}},
		})
		op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
		_, err := op.Read(context.Background(), rows.ReadOpts{
			Schema: "a", Table: "t", Columns: []string{"name"},
			Filter: []rows.Predicate{{Column: "name", Op: rows.OpLike, Value: "%a%"}},
			Limit:  5,
		})
		if err != nil {
			t.Errorf("engine %v: OpLike failed: %v", engine, err)
		}
		if len(c.queryLog) == 0 {
			t.Errorf("engine %v: no query produced", engine)
			continue
		}
		if !strings.Contains(c.queryLog[0].sql, "LIKE") {
			t.Errorf("engine %v: SQL %q missing LIKE", engine, c.queryLog[0].sql)
		}
	}
}

// ─── ReadResult.Capped surfaces for callers ──────────────────────────

func TestH1_ReadResult_CappedDefaultsFalse(t *testing.T) {
	c := newStubConn()
	c.queryResp = [][]any{{int64(1)}}
	c.queryCols = []driver.ColumnInfo{{Name: "id"}}
	s := newStubSchema(dbadmin.EngineMariaDB).withTable("a.t", &schema.Table{
		Columns: []schema.Column{{Name: "id"}},
	})
	op, _ := rows.New(c, s, rows.Options{Limits: driver.Limits{MaxRows: 100}})
	r, err := op.Read(context.Background(), rows.ReadOpts{
		Schema: "a", Table: "t", Columns: []string{"id"}, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Capped {
		t.Error("Capped should default false when result fits under Limit")
	}
}

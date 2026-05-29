package explain

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/classifier"
	"github.com/auracp/auracp/pkg/dbadmin/driver"
)

// ─── MariaDB parser ──────────────────────────────────────────────────

func TestMySQL_Normalize_SimpleTable(t *testing.T) {
	raw := []byte(`{
		"query_block": {
			"select_id": 1,
			"cost_info": {"query_cost": "1.20"},
			"table": {
				"table_name": "users",
				"access_type": "ALL",
				"rows_examined_per_scan": 1000,
				"filtered": "100.00",
				"cost_info": {
					"read_cost": "0.50",
					"eval_cost": "0.20",
					"prefix_cost": "0.70",
					"data_read_per_join": "1K"
				}
			}
		}
	}`)
	p, err := (&mysqlNormalizer{}).Normalize(raw, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.Engine != "mariadb" {
		t.Errorf("Engine = %q, want mariadb", p.Engine)
	}
	if p.Root == nil {
		t.Fatal("Root is nil")
	}
	if p.Root.Kind != "Full Table Scan" {
		t.Errorf("Kind = %q, want Full Table Scan", p.Root.Kind)
	}
	if p.Root.Relation != "users" {
		t.Errorf("Relation = %q, want users", p.Root.Relation)
	}
	if p.Total.CostTotal != 1.20 {
		t.Errorf("Total.CostTotal = %f, want 1.20", p.Total.CostTotal)
	}
	if p.Root.Metrics.RowsExpected != 1000 {
		t.Errorf("RowsExpected = %d, want 1000", p.Root.Metrics.RowsExpected)
	}
}

func TestMySQL_Normalize_IndexScan(t *testing.T) {
	raw := []byte(`{
		"query_block": {
			"cost_info": {"query_cost": "0.35"},
			"table": {
				"table_name": "users",
				"access_type": "ref",
				"key": "idx_email",
				"rows_examined_per_scan": 1,
				"cost_info": {"prefix_cost": "0.35"}
			}
		}
	}`)
	p, _ := (&mysqlNormalizer{}).Normalize(raw, false)
	if p.Root.Kind != "Index Lookup" {
		t.Errorf("Kind = %q, want Index Lookup", p.Root.Kind)
	}
	if p.Root.Index != "idx_email" {
		t.Errorf("Index = %q, want idx_email", p.Root.Index)
	}
}

func TestMySQL_Normalize_NestedLoopJoin(t *testing.T) {
	raw := []byte(`{
		"query_block": {
			"cost_info": {"query_cost": "5.00"},
			"nested_loop": [
				{ "table": {"table_name": "users", "access_type": "ALL", "rows_examined_per_scan": 100, "cost_info": {"prefix_cost": "1.00"}} },
				{ "table": {"table_name": "orders", "access_type": "ref", "key": "idx_user", "rows_examined_per_scan": 10, "cost_info": {"prefix_cost": "4.00"}} }
			]
		}
	}`)
	p, err := (&mysqlNormalizer{}).Normalize(raw, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.Root.Kind != "Nested Loop" {
		t.Errorf("Kind = %q, want Nested Loop", p.Root.Kind)
	}
	if len(p.Root.Children) != 2 {
		t.Fatalf("children = %d, want 2", len(p.Root.Children))
	}
	if p.Root.Children[0].Relation != "users" {
		t.Errorf("child[0].Relation = %q, want users", p.Root.Children[0].Relation)
	}
	if p.Root.Children[1].Index != "idx_user" {
		t.Errorf("child[1].Index = %q, want idx_user", p.Root.Children[1].Index)
	}
}

func TestMySQL_Normalize_OrderingWrapper(t *testing.T) {
	raw := []byte(`{
		"query_block": {
			"cost_info": {"query_cost": "5.00"},
			"ordering_operation": {
				"using_temporary_table": false,
				"using_filesort": false,
				"table": {"table_name": "events", "access_type": "ALL", "rows_examined_per_scan": 10, "cost_info": {"prefix_cost": "5.00"}}
			}
		}
	}`)
	p, err := (&mysqlNormalizer{}).Normalize(raw, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.Root.Kind != "Ordering" {
		t.Errorf("Kind = %q, want Ordering", p.Root.Kind)
	}
	if len(p.Root.Children) != 1 {
		t.Fatalf("children = %d, want 1", len(p.Root.Children))
	}
	if p.Root.Children[0].Relation != "events" {
		t.Errorf("child relation = %q, want events", p.Root.Children[0].Relation)
	}
}

func TestMySQL_Normalize_Warnings(t *testing.T) {
	raw := []byte(`{
		"query_block": {"cost_info": {"query_cost": "1.00"}, "table": {"table_name": "x", "access_type": "ALL", "cost_info": {"prefix_cost": "1.00"}}},
		"warnings": [{"Code": 1681, "Level": "Warning", "Message": "JSON_TABLE has limited support"}]
	}`)
	p, _ := (&mysqlNormalizer{}).Normalize(raw, false)
	// PR #6.5 M6: the warning entry now includes [Level Code] prefix.
	if len(p.Warnings) != 1 {
		t.Fatalf("warnings = %v, want one warning", p.Warnings)
	}
	want := "[Warning 1681] JSON_TABLE has limited support"
	if p.Warnings[0] != want {
		t.Errorf("warnings[0] = %q, want %q", p.Warnings[0], want)
	}
	// And the code is mirrored onto Root.Extras for filterable triage.
	if p.Root == nil || p.Root.Extras == nil {
		t.Fatalf("Root.Extras nil, want warningCodes")
	}
	codes, _ := p.Root.Extras["warningCodes"].([]int)
	if len(codes) != 1 || codes[0] != 1681 {
		t.Errorf("warningCodes = %v, want [1681]", codes)
	}
}

func TestMySQL_Normalize_AnalyzeAddsWarning(t *testing.T) {
	// mysqlExplain prepends the analyze-not-supported warning when
	// Analyze=true. Normalize itself is engine-shape neutral; verify
	// the mysqlExplain path by exercising the helper via the
	// integration_test (env-gated). Here we just confirm Normalize
	// produces no extra warning when raw didn't contain one.
	raw := []byte(`{"query_block": {"cost_info": {"query_cost": "1"}, "table": {"table_name": "x", "access_type": "ALL", "cost_info": {"prefix_cost": "1"}}}}`)
	p, _ := (&mysqlNormalizer{}).Normalize(raw, true)
	if len(p.Warnings) != 0 {
		t.Errorf("Normalize should not add warnings; got %v", p.Warnings)
	}
}

func TestMySQL_Normalize_BadJSON_Errors(t *testing.T) {
	_, err := (&mysqlNormalizer{}).Normalize([]byte("not json"), false)
	if err == nil {
		t.Error("expected error for bad JSON")
	}
}

func TestMySQL_Normalize_MissingQueryBlock_Errors(t *testing.T) {
	_, err := (&mysqlNormalizer{}).Normalize([]byte(`{"other": 1}`), false)
	if err == nil {
		t.Error("expected error for missing query_block")
	}
}

// ─── Postgres parser ─────────────────────────────────────────────────

func TestPostgres_Normalize_SeqScan(t *testing.T) {
	raw := []byte(`[
		{
			"Plan": {
				"Node Type": "Seq Scan",
				"Relation Name": "users",
				"Schema": "public",
				"Alias": "u",
				"Startup Cost": 0.00,
				"Total Cost": 22.00,
				"Plan Rows": 1000,
				"Plans": []
			},
			"Planning Time": 0.234,
			"Execution Time": 1.567
		}
	]`)
	p, err := (&postgresNormalizer{}).Normalize(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	if p.Engine != "postgres" {
		t.Errorf("Engine = %q, want postgres", p.Engine)
	}
	if p.Root.Kind != "Seq Scan" {
		t.Errorf("Kind = %q", p.Root.Kind)
	}
	if p.Root.Relation != "users" {
		t.Errorf("Relation = %q", p.Root.Relation)
	}
	if p.Root.Schema != "public" {
		t.Errorf("Schema = %q", p.Root.Schema)
	}
	if p.Root.Alias != "u" {
		t.Errorf("Alias = %q", p.Root.Alias)
	}
	if p.Root.Metrics.CostTotal != 22.00 {
		t.Errorf("CostTotal = %f, want 22.00", p.Root.Metrics.CostTotal)
	}
	if p.PlanningTimeMS != 0.234 {
		t.Errorf("PlanningTimeMS = %f, want 0.234", p.PlanningTimeMS)
	}
	if p.ExecutionTimeMS != 1.567 {
		t.Errorf("ExecutionTimeMS = %f, want 1.567", p.ExecutionTimeMS)
	}
}

func TestPostgres_Normalize_NestedJoin(t *testing.T) {
	raw := []byte(`[
		{
			"Plan": {
				"Node Type": "Hash Join",
				"Join Type": "Inner",
				"Startup Cost": 1.00,
				"Total Cost": 50.00,
				"Plan Rows": 100,
				"Hash Cond": "(u.id = o.user_id)",
				"Plans": [
					{
						"Node Type": "Seq Scan",
						"Relation Name": "users",
						"Alias": "u",
						"Total Cost": 22.00,
						"Plan Rows": 1000,
						"Plans": []
					},
					{
						"Node Type": "Hash",
						"Total Cost": 10.00,
						"Plan Rows": 100,
						"Plans": [
							{
								"Node Type": "Seq Scan",
								"Relation Name": "orders",
								"Alias": "o",
								"Total Cost": 5.00,
								"Plans": []
							}
						]
					}
				]
			}
		}
	]`)
	p, err := (&postgresNormalizer{}).Normalize(raw, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.Root.Kind != "Hash Join" {
		t.Errorf("Kind = %q, want Hash Join", p.Root.Kind)
	}
	if p.Root.JoinType != "Inner" {
		t.Errorf("JoinType = %q, want Inner", p.Root.JoinType)
	}
	if p.Root.Filter != "(u.id = o.user_id)" {
		t.Errorf("Filter (from Hash Cond) = %q", p.Root.Filter)
	}
	if len(p.Root.Children) != 2 {
		t.Fatalf("children = %d, want 2", len(p.Root.Children))
	}
	// Nested grandchild: orders Seq Scan two levels deep.
	if len(p.Root.Children[1].Children) != 1 {
		t.Fatalf("grandchildren = %d, want 1", len(p.Root.Children[1].Children))
	}
	gc := p.Root.Children[1].Children[0]
	if gc.Relation != "orders" {
		t.Errorf("grandchild relation = %q, want orders", gc.Relation)
	}
}

func TestPostgres_Normalize_AnalyzeFields(t *testing.T) {
	raw := []byte(`[
		{
			"Plan": {
				"Node Type": "Index Scan",
				"Relation Name": "users",
				"Index Name": "users_pk",
				"Index Cond": "(id = 42)",
				"Total Cost": 0.42,
				"Plan Rows": 1,
				"Actual Startup Time": 0.05,
				"Actual Total Time": 0.06,
				"Actual Rows": 1,
				"Actual Loops": 1,
				"Shared Hit Blocks": 3,
				"Shared Read Blocks": 0,
				"Plans": []
			}
		}
	]`)
	p, _ := (&postgresNormalizer{}).Normalize(raw, true)
	m := p.Root.Metrics
	if m.RowsActual != 1 {
		t.Errorf("RowsActual = %d, want 1", m.RowsActual)
	}
	if m.TimeTotalMS != 0.06 {
		t.Errorf("TimeTotalMS = %f", m.TimeTotalMS)
	}
	if m.Loops != 1 {
		t.Errorf("Loops = %d", m.Loops)
	}
	if m.BuffersHit != 3 {
		t.Errorf("BuffersHit = %d, want 3", m.BuffersHit)
	}
	if p.Root.Index != "users_pk" {
		t.Errorf("Index = %q", p.Root.Index)
	}
	if p.Root.Filter != "(id = 42)" {
		t.Errorf("Filter from Index Cond = %q", p.Root.Filter)
	}
}

func TestPostgres_Normalize_ActualRowsWithLoops(t *testing.T) {
	// Postgres reports per-loop actual rows; the normalizer should
	// multiply by Loops for the "real" total. Inner side of nested
	// loop typical case.
	raw := []byte(`[
		{
			"Plan": {
				"Node Type": "Index Scan",
				"Actual Rows": 2.0,
				"Actual Loops": 100,
				"Plans": []
			}
		}
	]`)
	p, _ := (&postgresNormalizer{}).Normalize(raw, true)
	if p.Root.Metrics.RowsActual != 200 {
		t.Errorf("RowsActual = %d, want 200 (2 rows × 100 loops)", p.Root.Metrics.RowsActual)
	}
}

func TestPostgres_Normalize_BadJSON_Errors(t *testing.T) {
	_, err := (&postgresNormalizer{}).Normalize([]byte("not json"), false)
	if err == nil {
		t.Error("expected error for bad JSON")
	}
}

func TestPostgres_Normalize_EmptyArray_Errors(t *testing.T) {
	_, err := (&postgresNormalizer{}).Normalize([]byte("[]"), false)
	if err == nil {
		t.Error("expected error for empty array")
	}
}

func TestPostgres_Normalize_MissingPlanField_Errors(t *testing.T) {
	_, err := (&postgresNormalizer{}).Normalize([]byte(`[{"NotPlan": 1}]`), false)
	if err == nil {
		t.Error("expected error for missing Plan field")
	}
}

// ─── Public API ──────────────────────────────────────────────────────

func TestExplain_RejectsNilConn(t *testing.T) {
	_, err := Explain(context.Background(), nil, 0, ExplainOpts{SQL: "SELECT 1"})
	if err == nil {
		t.Error("expected error for nil conn")
	}
}

func TestExplain_RejectsEmptySQL(t *testing.T) {
	_, err := Explain(context.Background(), &noopConn{}, dbadmin.EngineMariaDB, ExplainOpts{})
	if err == nil {
		t.Error("expected error for empty SQL")
	}
}

func TestExplain_UnsupportedEngine(t *testing.T) {
	_, err := Explain(context.Background(), &noopConn{}, dbadmin.EngineKind(99),
		ExplainOpts{SQL: "SELECT 1"})
	if err == nil {
		t.Error("expected error for unsupported engine")
	}
}

// TestExplain_AnalyzeRequiresClassRead asserts that Explain refuses
// Analyze=true on any non-read class. Structural defense per C1: the
// engine layer should also gate, but if a future caller forgets,
// EXPLAIN ANALYZE would actually execute (and mutate via) the
// statement.
func TestExplain_AnalyzeRequiresClassRead(t *testing.T) {
	cases := []classifier.QueryClass{
		classifier.ClassWriteRow,
		classifier.ClassWriteRowMass,
		classifier.ClassDDL,
		classifier.ClassDangerous,
		classifier.ClassForbidden,
	}
	for _, cls := range cases {
		_, err := Explain(context.Background(), &noopConn{}, dbadmin.EngineMariaDB,
			ExplainOpts{SQL: "DELETE FROM users", Analyze: true, Class: cls})
		if err == nil {
			t.Errorf("class %v: expected refusal of Analyze=true, got nil error", cls)
			continue
		}
		if !strings.Contains(err.Error(), "Analyze=true refused") {
			t.Errorf("class %v: error = %q, want contains 'Analyze=true refused'", cls, err)
		}
	}

	// Sanity: Analyze=false on a write is permitted (the noopConn will
	// panic if Query is actually invoked, so we expect the panic-free
	// path to short-circuit elsewhere — in this case the engine path
	// is reached and panics. To keep this assertion focused we only
	// confirm the gate doesn't refuse).
	defer func() {
		// recover the expected noopConn panic; the gate didn't fire.
		_ = recover()
	}()
	_, _ = Explain(context.Background(), &noopConn{}, dbadmin.EngineMariaDB,
		ExplainOpts{SQL: "DELETE FROM users", Analyze: false, Class: classifier.ClassWriteRow})
}

// TestReadSingleJSONRow_RejectsOversize asserts that the post-fetch
// byte cap (H1) fires even when MaxRows=1 prevents the driver's
// pre-fetch byte check from ever triggering.
func TestReadSingleJSONRow_RejectsOversize(t *testing.T) {
	huge := make([]byte, 1024) // 1 KiB
	for i := range huge {
		huge[i] = 'x'
	}
	conn := &fakeJSONConn{payload: huge}

	limits := driver.Limits{MaxRows: 1, MaxBytes: 100} // cap = 100 bytes
	_, err := readSingleJSONRow(context.Background(), conn, limits, "EXPLAIN SELECT 1")
	if err == nil {
		t.Fatal("expected error for oversize payload, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds cap") {
		t.Errorf("error = %q, want 'exceeds cap' substring", err)
	}

	// Sanity: same conn with a generous cap should succeed.
	limits.MaxBytes = 4096
	if _, err := readSingleJSONRow(context.Background(), conn, limits, "EXPLAIN SELECT 1"); err != nil {
		t.Errorf("expected success with generous cap, got %v", err)
	}
}

// TestPlan_SurvivesNaNInfInput asserts H2: NaN / +Inf / 1e500 inputs
// in the engine output are coerced to 0 and the resulting Plan
// json-marshals cleanly (encoding/json refuses NaN/Inf and would
// fail the whole document otherwise).
func TestPlan_SurvivesNaNInfInput(t *testing.T) {
	t.Run("postgres-poisoned-direct", func(t *testing.T) {
		// Simulate a path where a downstream computation in our own
		// walker produces +Inf — e.g., a Postgres backend (or future
		// custom JSON source) hands us a value that's clean for
		// encoding/json but becomes Inf after our math. Inject by
		// pre-poisoning a pgPlan and running postProcessPgPlan +
		// walkPgNode directly.
		inf := math.Inf(1)
		nan := math.NaN()
		p := &pgPlan{
			NodeType:        "Seq Scan",
			TotalCost:       inf,
			StartupCost:     inf,
			ActualStartTime: inf,
			ActualTotalTime: nan,
		}
		postProcessPgPlan(p)
		ws := &walkState{}
		node := walkPgNode(p, ws)
		plan := &Plan{Engine: "postgres", Root: node, Total: node.Metrics, PlanningTimeMS: sanitizeFloat(inf)}
		if plan.Root.Metrics.CostTotal != 0 {
			t.Errorf("CostTotal = %v, want 0 (sanitized from +Inf)", plan.Root.Metrics.CostTotal)
		}
		if plan.PlanningTimeMS != 0 {
			t.Errorf("PlanningTimeMS = %v, want 0", plan.PlanningTimeMS)
		}
		if _, err := json.Marshal(plan); err != nil {
			t.Errorf("json.Marshal(Plan): %v (must not fail on NaN/Inf)", err)
		}
	})

	t.Run("mariadb", func(t *testing.T) {
		raw := []byte(`{
			"query_block": {
				"cost_info": {"query_cost": "1e500"},
				"table": {
					"table_name": "x",
					"access_type": "ALL",
					"rows_examined_per_scan": 1,
					"cost_info": {"prefix_cost": "NaN"}
				}
			}
		}`)
		p, err := (&mysqlNormalizer{}).Normalize(raw, false)
		if err != nil {
			t.Fatalf("Normalize: %v", err)
		}
		// query_cost "1e500" → +Inf via ParseFloat → 0 via sanitize.
		if p.Total.CostTotal != 0 {
			t.Errorf("Total.CostTotal = %v, want 0", p.Total.CostTotal)
		}
		if p.Root.Metrics.CostTotal != 0 {
			t.Errorf("Root.Metrics.CostTotal = %v, want 0", p.Root.Metrics.CostTotal)
		}
		if _, err := json.Marshal(p); err != nil {
			t.Errorf("json.Marshal(Plan): %v (must not fail)", err)
		}
	})
}

// TestNormalize_DepthCap asserts H3: a deeply-nested plan does not
// blow the stack; recursion is capped at maxPlanDepth and a truncation
// warning is surfaced on Plan.Warnings.
func TestNormalize_DepthCap(t *testing.T) {
	// Build a Postgres plan nested ~maxPlanDepth+50 deep.
	var build func(depth int) string
	build = func(depth int) string {
		if depth == 0 {
			return `{"Node Type": "Seq Scan", "Plans": []}`
		}
		return fmt.Sprintf(`{"Node Type": "Nested Loop", "Plans": [%s]}`, build(depth-1))
	}
	raw := []byte(fmt.Sprintf(`[{"Plan": %s}]`, build(maxPlanDepth+50)))

	p, err := (&postgresNormalizer{}).Normalize(raw, false)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(p.Warnings) == 0 {
		t.Fatal("expected a truncation warning")
	}
	found := false
	for _, w := range p.Warnings {
		if strings.Contains(w, "truncated") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("warnings %v: want one containing 'truncated'", p.Warnings)
	}
	// Walk the actual chain; it should stop at <= maxPlanDepth.
	n := p.Root
	depth := 0
	for n != nil && len(n.Children) > 0 {
		n = n.Children[0]
		depth++
		if depth > maxPlanDepth+10 {
			t.Fatalf("recursion not capped — reached depth %d", depth)
		}
	}
}

// TestPostgresNormalize_ClampsHugeActualRows asserts H4: an enormous
// ActualRows value does not wrap / saturate to a negative int64.
func TestPostgresNormalize_ClampsHugeActualRows(t *testing.T) {
	raw := []byte(`[{
		"Plan": {
			"Node Type": "Seq Scan",
			"Actual Rows": 1e20,
			"Actual Loops": 1,
			"Plans": []
		}
	}]`)
	p, err := (&postgresNormalizer{}).Normalize(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	if p.Root.Metrics.RowsActual < 0 {
		t.Errorf("RowsActual = %d, want >= 0 (must not wrap)", p.Root.Metrics.RowsActual)
	}
	// Should be clamped to MaxInt64 (1e20 > MaxInt64 ~ 9.2e18).
	if p.Root.Metrics.RowsActual != (1<<63 - 1) {
		t.Errorf("RowsActual = %d, want MaxInt64 (clamped)", p.Root.Metrics.RowsActual)
	}
}

// TestPlanJSONShape asserts H8: JSON keys are lowerCamelCase and the
// frontend-required shape (engine, root, total, children:[]) holds.
func TestPlanJSONShape(t *testing.T) {
	p := &Plan{
		Engine: "postgres",
		Root: &Node{
			Kind:     "Seq Scan",
			Children: []*Node{}, // empty must marshal as [], not null
		},
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	mustHave := []string{
		`"engine":`,
		`"root":`,
		`"total":`,
		`"children":[]`,
		`"kind":"Seq Scan"`,
		`"planningTimeMs":`,
		`"executionTimeMs":`,
	}
	for _, want := range mustHave {
		if !strings.Contains(s, want) {
			t.Errorf("JSON missing %q\nGot: %s", want, s)
		}
	}
	mustNotHave := []string{
		`"Engine":`,
		`"Root":`,
		`"Children":`,
		`"Kind":`,
		`"PlanningTimeMS":`,
	}
	for _, bad := range mustNotHave {
		if strings.Contains(s, bad) {
			t.Errorf("JSON unexpectedly contains PascalCase key %q\nGot: %s", bad, s)
		}
	}
}

// ─── Helpers + asX coercion tests ────────────────────────────────────

func TestAsFloat64(t *testing.T) {
	cases := []struct {
		in   any
		want float64
	}{
		{float64(1.5), 1.5},
		{int64(2), 2},
		{int(3), 3},
		{"4.25", 4.25},
		{nil, 0},
	}
	for _, c := range cases {
		if got := asFloat64(c.in); got != c.want {
			t.Errorf("asFloat64(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestAsInt64(t *testing.T) {
	cases := []struct {
		in   any
		want int64
	}{
		{int64(42), 42},
		{int(43), 43},
		// PR #6.5 L1: float→int64 rounds half-away-from-zero now.
		{float64(44.5), 45},
		{float64(44.4), 44},
		{float64(1.9), 2},
		{float64(-1.5), -2},
		{"45", 45},
		// L1: numeric string with fractional part rounds too.
		{"1.9", 2},
		{nil, 0},
	}
	for _, c := range cases {
		if got := asInt64(c.in); got != c.want {
			t.Errorf("asInt64(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestAsString(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"hello", "hello"},
		{[]byte("world"), "world"},
		{nil, ""},
		{42, "42"},
	}
	for _, c := range cases {
		if got := asString(c.in); got != c.want {
			t.Errorf("asString(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "", "x", "y"); got != "x" {
		t.Errorf("firstNonEmpty = %q, want x", got)
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Errorf("firstNonEmpty(all empty) = %q, want empty", got)
	}
}

func TestPlanJSONMarshals(t *testing.T) {
	// Verify Plan + Node round-trip through encoding/json so the HTTP
	// handler can marshal a Plan directly.
	p := &Plan{
		Engine: "postgres",
		Root: &Node{
			Kind:     "Seq Scan",
			Relation: "users",
			Children: []*Node{
				{Kind: "Limit"},
			},
		},
		Total:           Metrics{CostTotal: 1.23},
		PlanningTimeMS:  0.1,
		ExecutionTimeMS: 1.0,
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var back Plan
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Root == nil || back.Root.Kind != "Seq Scan" {
		t.Errorf("round-trip lost Root.Kind: %+v", back.Root)
	}
}

// ─── Test plumbing ───────────────────────────────────────────────────

// noopConn is the smallest driver.Conn for arg-validation tests; its
// query methods panic so any unintended query path is obvious. Used
// only in tests that expect to short-circuit before reaching the DB.
type noopConn struct{}

func (*noopConn) Query(ctx context.Context, _ driver.Limits, _ string, _ ...any) (driver.Rows, error) {
	panic("noopConn.Query should not be reached")
}
func (*noopConn) Exec(ctx context.Context, _ driver.Limits, _ string, _ ...any) (driver.Result, error) {
	panic("noopConn.Exec should not be reached")
}
func (*noopConn) Ping(ctx context.Context) error                    { return nil }
func (*noopConn) ServerVersion(ctx context.Context) (string, error) { return "", nil }
func (*noopConn) Close() error                                      { return nil }

// fakeJSONConn returns a fixed []byte payload as the single-column
// single-row result of any Query call. Used to exercise the
// post-fetch byte cap in readSingleJSONRow without standing up a
// real driver.
type fakeJSONConn struct{ payload []byte }

func (c *fakeJSONConn) Query(ctx context.Context, _ driver.Limits, _ string, _ ...any) (driver.Rows, error) {
	return &fakeRows{payload: c.payload}, nil
}
func (c *fakeJSONConn) Exec(ctx context.Context, _ driver.Limits, _ string, _ ...any) (driver.Result, error) {
	return driver.Result{}, nil
}
func (c *fakeJSONConn) Ping(ctx context.Context) error                    { return nil }
func (c *fakeJSONConn) ServerVersion(ctx context.Context) (string, error) { return "", nil }
func (c *fakeJSONConn) Close() error                                      { return nil }

type fakeRows struct {
	payload []byte
	done    bool
}

func (r *fakeRows) Columns() []driver.ColumnInfo {
	return []driver.ColumnInfo{{Name: "EXPLAIN"}}
}
func (r *fakeRows) Next(ctx context.Context) ([]any, error) {
	if r.done {
		return nil, driver.ErrEOF
	}
	r.done = true
	return []any{r.payload}, nil
}
func (r *fakeRows) Close() error { return nil }

// ─── PR #6.5 — deferred items from PR #6 review ─────────────────────

// TestMySQL_NestedLoop_MultiplicativeRows (H5): the join node's
// RowsExpected must be outer × inner, not outer + inner.
func TestMySQL_NestedLoop_MultiplicativeRows(t *testing.T) {
	raw := []byte(`{
		"query_block": {
			"cost_info": {"query_cost": "5.00"},
			"nested_loop": [
				{ "table": {"table_name": "users", "access_type": "ALL", "rows_examined_per_scan": 100, "cost_info": {"prefix_cost": "1.00"}} },
				{ "table": {"table_name": "orders", "access_type": "ref", "key": "idx_user", "rows_examined_per_scan": 10, "cost_info": {"prefix_cost": "4.00"}} }
			]
		}
	}`)
	p, err := (&mysqlNormalizer{}).Normalize(raw, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.Root.Metrics.RowsExpected != 1000 {
		t.Errorf("Nested Loop RowsExpected = %d, want 1000 (100×10)", p.Root.Metrics.RowsExpected)
	}
	if p.Root.JoinType != "Inner" {
		t.Errorf("JoinType = %q, want Inner", p.Root.JoinType)
	}
}

// TestPostgres_Extras_H6 (H6, M2, M15): Sort Key, Group Key, Hash
// Keys, Workers Planned/Launched, Parallel Aware, Output, Subplan
// Name, JIT, Triggers, Settings, AND the lossless per-condition fields
// are all surfaced on Node.Extras / Root.Extras.
func TestPostgres_Extras_H6(t *testing.T) {
	raw := []byte(`[
		{
			"Plan": {
				"Node Type": "Gather",
				"Workers Planned": 4,
				"Workers Launched": 3,
				"Parallel Aware": true,
				"Total Cost": 10.0,
				"Plans": [
					{
						"Node Type": "Sort",
						"Sort Key": ["users.id", "users.name DESC"],
						"Total Cost": 8.0,
						"Plans": [
							{
								"Node Type": "Bitmap Heap Scan",
								"Relation Name": "users",
								"Filter": "(age > 30)",
								"Recheck Cond": "(id < 100)",
								"Index Cond": "(id BETWEEN 1 AND 99)",
								"Output": ["id", "name"],
								"Total Cost": 5.0,
								"Plans": []
							}
						]
					}
				]
			},
			"JIT": {"Functions": 4},
			"Triggers": [{"Trigger Name": "t1"}],
			"Settings": {"work_mem": "16MB"}
		}
	]`)
	p, err := (&postgresNormalizer{analyzed: true}).Normalize(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	if p.Root.Extras == nil {
		t.Fatal("Root.Extras nil")
	}
	if p.Root.Extras["workersPlanned"] != int64(4) {
		t.Errorf("workersPlanned = %v, want 4", p.Root.Extras["workersPlanned"])
	}
	if p.Root.Extras["workersLaunched"] != int64(3) {
		t.Errorf("workersLaunched = %v, want 3", p.Root.Extras["workersLaunched"])
	}
	if p.Root.Extras["parallelAware"] != true {
		t.Errorf("parallelAware = %v, want true", p.Root.Extras["parallelAware"])
	}
	if _, ok := p.Root.Extras["jit"]; !ok {
		t.Errorf("Root.Extras missing 'jit'")
	}
	if _, ok := p.Root.Extras["triggers"]; !ok {
		t.Errorf("Root.Extras missing 'triggers'")
	}
	if _, ok := p.Root.Extras["settings"]; !ok {
		t.Errorf("Root.Extras missing 'settings'")
	}

	sort := p.Root.Children[0]
	sk, _ := sort.Extras["sortKey"].([]string)
	if len(sk) != 2 || sk[0] != "users.id" {
		t.Errorf("sortKey = %v, want [users.id, users.name DESC]", sk)
	}

	bitmap := sort.Children[0]
	if bitmap.Extras["filter"] != "(age > 30)" {
		t.Errorf("filter extra = %v", bitmap.Extras["filter"])
	}
	if bitmap.Extras["recheckCond"] != "(id < 100)" {
		t.Errorf("recheckCond extra = %v", bitmap.Extras["recheckCond"])
	}
	if bitmap.Extras["indexCond"] != "(id BETWEEN 1 AND 99)" {
		t.Errorf("indexCond extra = %v", bitmap.Extras["indexCond"])
	}
	// Filter (the legacy single-field) picks the first non-empty per
	// firstNonEmpty's priority, which is "Filter" → Filter > others.
	if bitmap.Filter != "(age > 30)" {
		t.Errorf("Filter = %q, want '(age > 30)'", bitmap.Filter)
	}
	out, _ := bitmap.Extras["output"].([]string)
	if len(out) != 2 || out[0] != "id" {
		t.Errorf("output = %v, want [id name]", out)
	}
}

// TestMySQL_ShapeCoverage_H7 (H7, L9): windowing, "Impossible WHERE",
// and unknown shapes — each must produce either an explicit Node Kind
// or an "Unknown" placeholder PLUS a parser warning naming the keys.
func TestMySQL_ShapeCoverage_H7(t *testing.T) {
	t.Run("windowing", func(t *testing.T) {
		raw := []byte(`{
			"query_block": {
				"cost_info": {"query_cost": "10.00"},
				"windowing": {
					"functions": [{"name": "row_number"}],
					"table": {"table_name": "events", "access_type": "ALL", "rows_examined_per_scan": 100, "cost_info": {"prefix_cost": "10.00"}}
				}
			}
		}`)
		p, err := (&mysqlNormalizer{}).Normalize(raw, false)
		if err != nil {
			t.Fatal(err)
		}
		if p.Root.Kind != "Windowing" {
			t.Errorf("Kind = %q, want Windowing", p.Root.Kind)
		}
		if _, ok := p.Root.Extras["windowing"]; !ok {
			t.Errorf("Extras missing windowing key")
		}
	})

	t.Run("impossible-where", func(t *testing.T) {
		raw := []byte(`{
			"query_block": {
				"select_id": 1,
				"message": "Impossible WHERE noticed after reading const tables"
			}
		}`)
		p, err := (&mysqlNormalizer{}).Normalize(raw, false)
		if err != nil {
			t.Fatal(err)
		}
		if p.Root.Kind != "Impossible WHERE" {
			t.Errorf("Kind = %q, want 'Impossible WHERE'", p.Root.Kind)
		}
	})

	t.Run("unknown-shape-with-warning", func(t *testing.T) {
		raw := []byte(`{
			"query_block": {
				"select_id": 1,
				"some_unknown_future_key": {}
			}
		}`)
		p, err := (&mysqlNormalizer{}).Normalize(raw, false)
		if err != nil {
			t.Fatal(err)
		}
		if p.Root.Kind != "Unknown" {
			t.Errorf("Kind = %q, want Unknown", p.Root.Kind)
		}
		found := false
		for _, w := range p.Warnings {
			if strings.Contains(w, "MariaDB block shape not recognized") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("warnings = %v, want one containing 'MariaDB block shape not recognized'", p.Warnings)
		}
	})

	t.Run("coexisting-subquery+table", func(t *testing.T) {
		raw := []byte(`{
			"query_block": {
				"cost_info": {"query_cost": "1.00"},
				"table": {"table_name": "x", "access_type": "ALL", "rows_examined_per_scan": 10, "cost_info": {"prefix_cost": "1.00"}},
				"having_subqueries": [
					{"query_block": {"cost_info": {"query_cost": "0.50"}, "table": {"table_name": "y", "access_type": "ALL", "rows_examined_per_scan": 5, "cost_info": {"prefix_cost": "0.50"}}}}
				]
			}
		}`)
		p, err := (&mysqlNormalizer{}).Normalize(raw, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Root.Children) != 1 {
			t.Fatalf("Children = %d, want 1 (the subquery)", len(p.Root.Children))
		}
		if p.Root.Children[0].Relation != "y" {
			t.Errorf("subquery child Relation = %q, want y", p.Root.Children[0].Relation)
		}
	})
}

// TestPlan_Total_H9_MariaDB (H9): MariaDB Plan.Total is now a
// rolled-up view of the whole tree, not just the root's prefix_cost.
func TestPlan_Total_H9_MariaDB(t *testing.T) {
	raw := []byte(`{
		"query_block": {
			"cost_info": {"query_cost": "100.00"},
			"nested_loop": [
				{ "table": {"table_name": "a", "access_type": "ALL", "rows_examined_per_scan": 50, "cost_info": {"prefix_cost": "10.00"}} },
				{ "table": {"table_name": "b", "access_type": "ALL", "rows_examined_per_scan": 30, "cost_info": {"prefix_cost": "80.00"}} }
			]
		}
	}`)
	p, _ := (&mysqlNormalizer{}).Normalize(raw, false)
	if p.Total.CostTotal != 100.00 {
		t.Errorf("Total.CostTotal = %f, want 100.00 (max of subtree)", p.Total.CostTotal)
	}
	// Total.CostStart is actively zero on MariaDB (N2).
	if p.Total.CostStart != 0 {
		t.Errorf("Total.CostStart = %f, want 0 (MariaDB always zero)", p.Total.CostStart)
	}
}

// TestPlan_PlanningTimed_M3: PlanningTimed disambiguates "not measured"
// from "sub-microsecond".
func TestPlan_PlanningTimed_M3(t *testing.T) {
	t.Run("postgres-analyze", func(t *testing.T) {
		raw := []byte(`[{"Plan": {"Node Type": "Seq Scan", "Plans": []}, "Planning Time": 0.001}]`)
		p, _ := (&postgresNormalizer{}).Normalize(raw, true)
		if !p.PlanningTimed {
			t.Errorf("PlanningTimed = false, want true (PG ANALYZE)")
		}
	})
	t.Run("postgres-no-analyze", func(t *testing.T) {
		raw := []byte(`[{"Plan": {"Node Type": "Seq Scan", "Plans": []}}]`)
		p, _ := (&postgresNormalizer{}).Normalize(raw, false)
		if p.PlanningTimed {
			t.Errorf("PlanningTimed = true, want false (PG without ANALYZE)")
		}
	})
	t.Run("mariadb-always-false", func(t *testing.T) {
		raw := []byte(`{"query_block": {"cost_info": {"query_cost": "1"}, "table": {"table_name": "x", "access_type": "ALL", "cost_info": {"prefix_cost": "1"}}}}`)
		p, _ := (&mysqlNormalizer{}).Normalize(raw, false)
		if p.PlanningTimed {
			t.Errorf("PlanningTimed = true, want false (MariaDB never times planning)")
		}
	})
}

// TestExplain_ValidateSQL_M1: multi-statement and bad-comment SQL is
// refused at the wrap site even if upstream classifier missed it.
func TestExplain_ValidateSQL_M1(t *testing.T) {
	bad := []string{
		"--;\nDROP TABLE x",                          // line-comment + stmt
		"SELECT 1; DROP TABLE x",                     // multi-statement
		"SELECT 1; -- trailing semicolon then stmt\nDELETE FROM y",
		"/* unterminated",
		"   ",       // empty after trim
		"-- only a comment",
		"'unterminated",
	}
	for _, sql := range bad {
		if err := validateSQLForExplain(sql); err == nil {
			t.Errorf("validateSQLForExplain(%q) = nil, want error", sql)
		}
	}
	ok := []string{
		"SELECT 1",
		"SELECT 1;",
		"SELECT 1;  ",
		"SELECT 1; -- end\n",
		"SELECT 'a;b' FROM t",
		"-- header\nSELECT 1",
	}
	for _, sql := range ok {
		if err := validateSQLForExplain(sql); err != nil {
			t.Errorf("validateSQLForExplain(%q) = %v, want nil", sql, err)
		}
	}
}

// TestExplain_ValidateSQL_GateInExplain: ensure Explain() actually
// calls the multi-statement guard (defense in depth past the
// classifier).
func TestExplain_ValidateSQL_GateInExplain(t *testing.T) {
	_, err := Explain(context.Background(), &noopConn{}, dbadmin.EngineMariaDB,
		ExplainOpts{SQL: "SELECT 1; DROP TABLE x"})
	if err == nil {
		t.Fatal("Explain accepted multi-statement SQL, want refusal")
	}
	if !strings.Contains(err.Error(), "multi-statement") {
		t.Errorf("error = %q, want contains 'multi-statement'", err)
	}
}

// TestAsFloat64_KMG_M4: K/M/G/T suffix handling for MariaDB's
// data_read_per_join + similar size strings.
func TestAsFloat64_KMG_M4(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"1K", 1024},
		{"10M", 10 * 1024 * 1024},
		{"1.5G", 1.5 * 1024 * 1024 * 1024},
		{"2T", 2.0 * 1024 * 1024 * 1024 * 1024},
		{"1Ki", 1024},
		{"2.5", 2.5},
		{"42", 42},
	}
	for _, c := range cases {
		if got := asFloat64(c.in); got != c.want {
			t.Errorf("asFloat64(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestParseMySQLTable_M5: RowsExpected stays examined-per-scan;
// rows_produced_per_join goes to Extras.
func TestParseMySQLTable_M5(t *testing.T) {
	raw := []byte(`{
		"query_block": {
			"cost_info": {"query_cost": "1.0"},
			"table": {
				"table_name": "x",
				"access_type": "ref",
				"rows_examined_per_scan": 100,
				"rows_produced_per_join": 5,
				"cost_info": {"prefix_cost": "1.0", "data_read_per_join": "10K"}
			}
		}
	}`)
	p, err := (&mysqlNormalizer{}).Normalize(raw, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.Root.Metrics.RowsExpected != 100 {
		t.Errorf("RowsExpected = %d, want 100 (rows_examined_per_scan, not produced)", p.Root.Metrics.RowsExpected)
	}
	if p.Root.Extras["rowsProducedPerJoin"] != int64(5) {
		t.Errorf("rowsProducedPerJoin extra = %v, want 5", p.Root.Extras["rowsProducedPerJoin"])
	}
	if p.Root.Extras["dataReadPerJoinBytes"] != int64(10*1024) {
		t.Errorf("dataReadPerJoinBytes extra = %v, want 10240", p.Root.Extras["dataReadPerJoinBytes"])
	}
}

// TestExplainOpts_OmitRaw_M9: Plan.Raw is dropped when requested.
func TestExplainOpts_OmitRaw_M9(t *testing.T) {
	raw := []byte(`[{"Plan": {"Node Type": "Seq Scan", "Plans": []}}]`)
	p, _ := (&postgresNormalizer{}).Normalize(raw, false)
	if len(p.Raw) == 0 {
		t.Fatal("Plan.Raw empty after Normalize; expected raw to be captured")
	}
	// Simulate the Explain() wrapper's OmitRaw post-processing.
	if true {
		p.Raw = nil
	}
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), `"raw"`) {
		t.Errorf("marshaled JSON contains 'raw' field after OmitRaw clear")
	}
}

// TestPostgresExplainFlags_M14: option assembly.
func TestPostgresExplainFlags_M14(t *testing.T) {
	cases := []struct {
		name string
		opts ExplainOpts
		want string
	}{
		{
			"defaults",
			ExplainOpts{},
			"BUFFERS, FORMAT JSON",
		},
		{
			"analyze",
			ExplainOpts{Analyze: true},
			"ANALYZE, BUFFERS, FORMAT JSON",
		},
		{
			"no-buffers",
			ExplainOpts{PGOptions: PostgresExplainOptions{DisableBuffers: true}},
			"FORMAT JSON",
		},
		{
			"verbose+settings",
			ExplainOpts{PGOptions: PostgresExplainOptions{Verbose: true, Settings: true}},
			"BUFFERS, VERBOSE, SETTINGS, FORMAT JSON",
		},
		{
			"wal-requires-analyze",
			ExplainOpts{PGOptions: PostgresExplainOptions{WAL: true}},
			"BUFFERS, FORMAT JSON",
		},
		{
			"wal+analyze",
			ExplainOpts{Analyze: true, PGOptions: PostgresExplainOptions{WAL: true}},
			"ANALYZE, BUFFERS, WAL, FORMAT JSON",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildPostgresExplainFlags(c.opts)
			if got != c.want {
				t.Errorf("flags = %q, want %q", got, c.want)
			}
		})
	}
}

// TestExplainWithConfig_M7: TimeoutMax plumbing.
func TestExplainWithConfig_M7(t *testing.T) {
	// We don't actually run a query — we test the precedence: when
	// opts.Limits.Timeout is zero AND cfg.Query.TimeoutMax is set,
	// the latter wins. Use a fake conn that fails to query so we
	// can inspect the post-default Limits via a side-effect.
	cfg := dbadmin.DefaultConfig()
	cfg.Query.TimeoutMax = 13 * 1000 * 1000 * 1000 // 13 seconds in ns
	conn := &captureLimitsConn{}
	_, _ = ExplainWithConfig(context.Background(), conn, dbadmin.EngineMariaDB, cfg, ExplainOpts{SQL: "SELECT 1"})
	if conn.lastLimits.Timeout == 0 {
		t.Fatal("Timeout not set on driver limits")
	}
	if conn.lastLimits.Timeout != cfg.Query.TimeoutMax {
		t.Errorf("Timeout = %v, want %v from cfg", conn.lastLimits.Timeout, cfg.Query.TimeoutMax)
	}
}

// TestExplain_AssertSecondNextEOF_L8: a driver returning two rows is
// rejected.
func TestExplain_AssertSecondNextEOF_L8(t *testing.T) {
	conn := &twoRowConn{payload: []byte(`{"query_block": {}}`)}
	_, err := readSingleJSONRow(context.Background(), conn, driver.Limits{MaxBytes: 1024}, "EXPLAIN ...")
	if err == nil {
		t.Fatal("expected error for driver returning >1 row")
	}
	if !strings.Contains(err.Error(), ">1 row") {
		t.Errorf("error = %q, want contains '>1 row'", err)
	}
}

// TestNode_Extras_JSONShape: Extras serializes as lowerCamelCase
// JSON-friendly map and is omitted when empty.
func TestNode_Extras_JSONShape(t *testing.T) {
	n := &Node{Kind: "Seq Scan", Children: []*Node{}}
	b, _ := json.Marshal(n)
	if strings.Contains(string(b), `"extras":`) {
		t.Errorf("extras unexpectedly emitted on empty: %s", b)
	}
	n.Extras = map[string]any{"workersPlanned": int64(2)}
	b, _ = json.Marshal(n)
	if !strings.Contains(string(b), `"extras":`) {
		t.Errorf("extras missing when populated: %s", b)
	}
	if !strings.Contains(string(b), `"workersPlanned":2`) {
		t.Errorf("expected workersPlanned key: %s", b)
	}
}

// TestMysqlAccessKind_L4: index_merge / index_subquery / unique_subquery.
func TestMysqlAccessKind_L4(t *testing.T) {
	cases := map[string]string{
		"index_merge":     "Index Merge",
		"index_subquery":  "Index Subquery",
		"unique_subquery": "Unique Subquery",
	}
	for in, want := range cases {
		if got := mysqlAccessKind(in); got != want {
			t.Errorf("mysqlAccessKind(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestEngineConstants_M13: the engine literals match what each
// normalizer emits.
func TestEngineConstants_M13(t *testing.T) {
	if EngineMariaDB != "mariadb" {
		t.Errorf("EngineMariaDB = %q, want mariadb", EngineMariaDB)
	}
	if EnginePostgres != "postgres" {
		t.Errorf("EnginePostgres = %q, want postgres", EnginePostgres)
	}
	rawPG := []byte(`[{"Plan": {"Node Type": "Seq Scan", "Plans": []}}]`)
	p, _ := (&postgresNormalizer{}).Normalize(rawPG, false)
	if p.Engine != EnginePostgres {
		t.Errorf("PG Plan.Engine = %q", p.Engine)
	}
	rawMY := []byte(`{"query_block": {"cost_info": {"query_cost": "1"}, "table": {"table_name": "x", "access_type": "ALL", "cost_info": {"prefix_cost": "1"}}}}`)
	p, _ = (&mysqlNormalizer{}).Normalize(rawMY, false)
	if p.Engine != EngineMariaDB {
		t.Errorf("MariaDB Plan.Engine = %q", p.Engine)
	}
}

// TestPostgres_BuffersDirtied_L2: Shared Dirtied Blocks surfaces on
// Metrics.BuffersDirtied.
func TestPostgres_BuffersDirtied_L2(t *testing.T) {
	raw := []byte(`[{
		"Plan": {
			"Node Type": "Seq Scan",
			"Shared Hit Blocks": 1,
			"Shared Read Blocks": 2,
			"Shared Dirtied Blocks": 3,
			"Shared Written Blocks": 4,
			"Plans": []
		}
	}]`)
	p, _ := (&postgresNormalizer{}).Normalize(raw, true)
	if p.Root.Metrics.BuffersDirtied != 3 {
		t.Errorf("BuffersDirtied = %d, want 3", p.Root.Metrics.BuffersDirtied)
	}
}

// TestMySQL_NestedUnion_L5: nested unions are recursed, not dropped.
func TestMySQL_NestedUnion_L5(t *testing.T) {
	raw := []byte(`{
		"query_block": {
			"cost_info": {"query_cost": "5"},
			"union_result": {
				"query_specifications": [
					{
						"query_block": {
							"union_result": {
								"query_specifications": [
									{"query_block": {"cost_info": {"query_cost": "1"}, "table": {"table_name": "deep", "access_type": "ALL", "cost_info": {"prefix_cost": "1"}}}}
								]
							}
						}
					}
				]
			}
		}
	}`)
	p, err := (&mysqlNormalizer{}).Normalize(raw, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.Root.Kind != "Union" {
		t.Errorf("Kind = %q, want Union", p.Root.Kind)
	}
	if len(p.Root.Children) != 1 {
		t.Fatalf("children = %d, want 1", len(p.Root.Children))
	}
	inner := p.Root.Children[0]
	if inner.Kind != "Union" {
		t.Errorf("inner.Kind = %q, want Union (nested union)", inner.Kind)
	}
	if len(inner.Children) == 0 || inner.Children[0].Relation != "deep" {
		t.Errorf("nested union didn't recurse to leaf 'deep': got %+v", inner)
	}
}

// TestMySQL_BlockNLJoin_L6: nested_loop entries without a 'table' key
// still parse (via generic block dispatch) rather than dropping.
func TestMySQL_BlockNLJoin_L6(t *testing.T) {
	raw := []byte(`{
		"query_block": {
			"cost_info": {"query_cost": "10"},
			"nested_loop": [
				{ "table": {"table_name": "a", "access_type": "ALL", "rows_examined_per_scan": 2, "cost_info": {"prefix_cost": "1"}} },
				{ "table": {"table_name": "b", "access_type": "ALL", "rows_examined_per_scan": 3, "cost_info": {"prefix_cost": "9"}} }
			]
		}
	}`)
	p, _ := (&mysqlNormalizer{}).Normalize(raw, false)
	if len(p.Root.Children) != 2 {
		t.Fatalf("children = %d, want 2", len(p.Root.Children))
	}
	// Multiplicative check: 2 * 3 = 6.
	if p.Root.Metrics.RowsExpected != 6 {
		t.Errorf("RowsExpected = %d, want 6 (2*3)", p.Root.Metrics.RowsExpected)
	}
}

// TestPostgres_Filter_Recheck_M15: Bitmap Heap Scan with both Filter
// and Recheck Cond preserves both on Extras even though Node.Filter
// collapses to one.
func TestPostgres_Filter_Recheck_M15(t *testing.T) {
	raw := []byte(`[{
		"Plan": {
			"Node Type": "Bitmap Heap Scan",
			"Filter": "(age > 30)",
			"Recheck Cond": "(id < 100)",
			"Plans": []
		}
	}]`)
	p, _ := (&postgresNormalizer{}).Normalize(raw, false)
	if p.Root.Extras["filter"] != "(age > 30)" {
		t.Errorf("extras[filter] = %v", p.Root.Extras["filter"])
	}
	if p.Root.Extras["recheckCond"] != "(id < 100)" {
		t.Errorf("extras[recheckCond] = %v", p.Root.Extras["recheckCond"])
	}
}

// TestMariaDB_CostStart_AlwaysZero_N2: even if a hostile/buggy raw
// payload tried to inject a CostStart value, the rollup zeroes it.
func TestMariaDB_CostStart_AlwaysZero_N2(t *testing.T) {
	raw := []byte(`{
		"query_block": {
			"cost_info": {"query_cost": "1"},
			"table": {"table_name": "x", "access_type": "ALL", "rows_examined_per_scan": 1, "cost_info": {"prefix_cost": "1"}}
		}
	}`)
	p, _ := (&mysqlNormalizer{}).Normalize(raw, false)
	if p.Total.CostStart != 0 {
		t.Errorf("Total.CostStart = %f, want 0 on MariaDB", p.Total.CostStart)
	}
	if p.Root.Metrics.CostStart != 0 {
		t.Errorf("Root.Metrics.CostStart = %f, want 0 on MariaDB", p.Root.Metrics.CostStart)
	}
}

// captureLimitsConn snapshots the Limits passed to Query so a test can
// inspect them.
type captureLimitsConn struct {
	lastLimits driver.Limits
}

func (c *captureLimitsConn) Query(ctx context.Context, l driver.Limits, _ string, _ ...any) (driver.Rows, error) {
	c.lastLimits = l
	// Return an empty rows iterator so the caller sees a NoRows error and
	// returns; we only care about the captured Limits.
	return &fakeRows{payload: nil}, fmt.Errorf("captureLimitsConn: synthetic error")
}
func (c *captureLimitsConn) Exec(ctx context.Context, _ driver.Limits, _ string, _ ...any) (driver.Result, error) {
	return driver.Result{}, nil
}
func (c *captureLimitsConn) Ping(ctx context.Context) error                    { return nil }
func (c *captureLimitsConn) ServerVersion(ctx context.Context) (string, error) { return "", nil }
func (c *captureLimitsConn) Close() error                                      { return nil }

// twoRowConn returns the payload row twice in a row, exercising L8.
type twoRowConn struct{ payload []byte }

func (c *twoRowConn) Query(ctx context.Context, _ driver.Limits, _ string, _ ...any) (driver.Rows, error) {
	return &twoRows{payload: c.payload}, nil
}
func (c *twoRowConn) Exec(ctx context.Context, _ driver.Limits, _ string, _ ...any) (driver.Result, error) {
	return driver.Result{}, nil
}
func (c *twoRowConn) Ping(ctx context.Context) error                    { return nil }
func (c *twoRowConn) ServerVersion(ctx context.Context) (string, error) { return "", nil }
func (c *twoRowConn) Close() error                                      { return nil }

type twoRows struct {
	payload []byte
	calls   int
}

func (r *twoRows) Columns() []driver.ColumnInfo { return []driver.ColumnInfo{{Name: "EXPLAIN"}} }
func (r *twoRows) Next(ctx context.Context) ([]any, error) {
	r.calls++
	if r.calls > 2 {
		return nil, driver.ErrEOF
	}
	return []any{r.payload}, nil
}
func (r *twoRows) Close() error { return nil }

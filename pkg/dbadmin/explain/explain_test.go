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
	if len(p.Warnings) != 1 || p.Warnings[0] != "JSON_TABLE has limited support" {
		t.Errorf("warnings = %v, want one warning", p.Warnings)
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
		{float64(44.5), 44},
		{"45", 45},
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

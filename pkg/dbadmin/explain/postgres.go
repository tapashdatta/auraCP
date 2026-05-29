package explain

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/auracp/auracp/pkg/dbadmin/driver"
)

// postgresExplain wraps the operator's SQL in EXPLAIN (...) FORMAT JSON
// and parses the result into a normalized Plan.
//
// Options assembled per opts:
//   - BUFFERS: always — operators want the cache-hit picture.
//   - ANALYZE: only when opts.Analyze=true. ANALYZE actually executes
//     the query; callers must gate on classifier output upstream.
//   - VERBOSE: false (extra columns are unused by the normalizer).
//   - FORMAT JSON: always.
func postgresExplain(ctx context.Context, conn driver.Conn, opts ExplainOpts, limits driver.Limits) (*Plan, error) {
	q := "EXPLAIN ("
	if opts.Analyze {
		q += "ANALYZE, BUFFERS, FORMAT JSON"
	} else {
		q += "BUFFERS, FORMAT JSON"
	}
	q += ") " + opts.SQL

	raw, err := readSingleJSONRow(ctx, conn, limits, q)
	if err != nil {
		return nil, err
	}
	return (&postgresNormalizer{}).Normalize(raw, opts.Analyze)
}

// postgresNormalizer implements Normalizer for PostgreSQL.
type postgresNormalizer struct{}

// Normalize parses a Postgres EXPLAIN ... FORMAT JSON payload.
//
// The shape:
//
//	[
//	  {
//	    "Plan": { ...recursive Plan nodes... },
//	    "Planning Time": 0.234,
//	    "Execution Time": 1.567   (only when ANALYZE)
//	  }
//	]
//
// Always a one-element array (Postgres EXPLAIN never returns multiple
// top-level plans for a single statement).
func (n *postgresNormalizer) Normalize(raw []byte, analyzed bool) (*Plan, error) {
	var arr []pgTop
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, fmt.Errorf("explain/postgres: parse top-level: %w", err)
	}
	if len(arr) == 0 {
		return nil, fmt.Errorf("explain/postgres: empty plan array")
	}
	top := arr[0]
	if top.Plan == nil {
		return nil, fmt.Errorf("explain/postgres: missing Plan field")
	}

	// Sanitize float fields that came directly off the struct's JSON
	// tags (encoding/json happily decodes NaN / +Inf out of strings
	// or via custom number sources). See sanitizeFloat in explain.go
	// and KNOWN-ISSUES H2.
	postProcessPgPlan(top.Plan)

	ws := &walkState{}
	root := walkPgNode(top.Plan, ws)
	plan := &Plan{
		Engine:          "postgres",
		Root:            root,
		Total:           root.Metrics,
		Raw:             raw,
		PlanningTimeMS:  sanitizeFloat(top.PlanningTime),
		ExecutionTimeMS: sanitizeFloat(top.ExecutionTime),
		Warnings:        ws.warnings,
	}
	_ = analyzed
	return plan, nil
}

// postProcessPgPlan walks the parsed pgPlan tree and clamps every
// float64 field to a JSON-marshalable value (no NaN / Inf). Applied
// post-Unmarshal because encoding/json refuses to emit those values
// later and the whole document would fail.
func postProcessPgPlan(p *pgPlan) {
	if p == nil {
		return
	}
	p.StartupCost = sanitizeFloat(p.StartupCost)
	p.TotalCost = sanitizeFloat(p.TotalCost)
	p.ActualStartTime = sanitizeFloat(p.ActualStartTime)
	p.ActualTotalTime = sanitizeFloat(p.ActualTotalTime)
	p.ActualRows = sanitizeFloat(p.ActualRows)
	for _, child := range p.Plans {
		postProcessPgPlan(child)
	}
}

// pgTop is the outer wrapper of a Postgres EXPLAIN JSON result.
type pgTop struct {
	Plan          *pgPlan `json:"Plan"`
	PlanningTime  float64 `json:"Planning Time"`
	ExecutionTime float64 `json:"Execution Time"`
}

// pgPlan is the recursive plan-node shape. Postgres emits dozens of
// fields per node; we capture the ones the flame-tree renderer + the
// inspector pane need, and leave the rest in Plan.Raw.
type pgPlan struct {
	NodeType         string    `json:"Node Type"`
	RelationName     string    `json:"Relation Name"`
	Schema           string    `json:"Schema"`
	Alias            string    `json:"Alias"`
	IndexName        string    `json:"Index Name"`
	JoinType         string    `json:"Join Type"`
	StartupCost      float64   `json:"Startup Cost"`
	TotalCost        float64   `json:"Total Cost"`
	PlanRows         int64     `json:"Plan Rows"`
	ActualRows       float64   `json:"Actual Rows"`
	ActualStartTime  float64   `json:"Actual Startup Time"`
	ActualTotalTime  float64   `json:"Actual Total Time"`
	ActualLoops      int64     `json:"Actual Loops"`
	SharedHitBlocks  int64     `json:"Shared Hit Blocks"`
	SharedReadBlocks int64     `json:"Shared Read Blocks"`
	SharedDirtBlocks int64     `json:"Shared Dirtied Blocks"`
	SharedWritBlocks int64     `json:"Shared Written Blocks"`
	IndexCond        string    `json:"Index Cond"`
	Filter           string    `json:"Filter"`
	HashCond         string    `json:"Hash Cond"`
	MergeCond        string    `json:"Merge Cond"`
	RecheckCond      string    `json:"Recheck Cond"`
	Plans            []*pgPlan `json:"Plans"`
}

// walkPgNode converts one pgPlan into our *Node. Recurses on Plans
// children. Caps recursion depth and total node count via walkState;
// see explain.go for the limits and KNOWN-ISSUES H3.
func walkPgNode(p *pgPlan, ws *walkState) *Node {
	if !ws.enter() {
		return nil
	}
	node := &Node{
		Kind:     p.NodeType,
		Relation: p.RelationName,
		Schema:   p.Schema,
		Alias:    p.Alias,
		Index:    p.IndexName,
		JoinType: p.JoinType,
		Filter:   firstNonEmpty(p.Filter, p.IndexCond, p.HashCond, p.MergeCond, p.RecheckCond),
		Metrics: Metrics{
			CostStart:      p.StartupCost,
			CostTotal:      p.TotalCost,
			RowsExpected:   p.PlanRows,
			RowsActual:     clampToInt64(p.ActualRows * float64(maxLoops(p.ActualLoops))),
			TimeStartMS:    p.ActualStartTime,
			TimeTotalMS:    p.ActualTotalTime,
			Loops:          p.ActualLoops,
			BuffersHit:     p.SharedHitBlocks,
			BuffersRead:    p.SharedReadBlocks,
			BuffersWritten: p.SharedWritBlocks,
		},
		Children: []*Node{},
	}
	ws.depth++
	for _, child := range p.Plans {
		c := walkPgNode(child, ws)
		if c == nil {
			// enter() already recorded the truncation warning;
			// drop this subtree and stop trying further siblings
			// once the cap fired (keeps output stable).
			break
		}
		node.Children = append(node.Children, c)
	}
	ws.depth--
	return node
}

// clampToInt64 converts a float64 row count into int64 with overflow
// + NaN safety. OLAP plans report ActualRows in the 1e10+ range; a
// naive cast saturates / wraps. NaN inputs (from a hostile or
// partially-analyzed plan) silently become 0 rather than 0x80000000.
func clampToInt64(f float64) int64 {
	if math.IsNaN(f) || f < 0 {
		return 0
	}
	if f > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(f)
}

// firstNonEmpty returns the first non-empty string from the list, used
// to collapse Postgres's several condition fields into one Filter slot.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// maxLoops returns Loops or 1 (Postgres reports per-loop actual rows,
// so the "real" row count is Actual Rows × Loops for the inner side of
// a join). For nodes with Loops=0 (not executed), avoid amplifying.
func maxLoops(l int64) int64 {
	if l < 1 {
		return 1
	}
	return l
}

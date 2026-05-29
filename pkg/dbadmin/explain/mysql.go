package explain

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/auracp/auracp/pkg/dbadmin/driver"
)

// mysqlExplain wraps the operator's SQL in EXPLAIN FORMAT=JSON, runs
// it, and parses the result into a normalized Plan.
//
// MariaDB and MySQL both support EXPLAIN FORMAT=JSON. The shape is a
// nested object rooted at "query_block" with optional sibling
// "warnings" arrays. We walk the tree, mapping each significant node
// (table access, joins, ordering operations) to one *Node in our
// Plan.
//
// MariaDB does NOT have an ANALYZE-equivalent that surfaces actual
// rows + timing in EXPLAIN JSON. Setting opts.Analyze true is therefore
// a no-op flag-wise — we still run EXPLAIN FORMAT=JSON and the
// resulting Plan has RowsActual=0 / TimeTotalMS=0 throughout. The
// engine documents this in Plan.Warnings.
func mysqlExplain(ctx context.Context, conn driver.Conn, opts ExplainOpts, limits driver.Limits) (*Plan, error) {
	q := "EXPLAIN FORMAT=JSON " + opts.SQL
	raw, err := readSingleJSONRow(ctx, conn, limits, q)
	if err != nil {
		return nil, err
	}

	p, err := (&mysqlNormalizer{}).Normalize(raw, opts.Analyze)
	if err != nil {
		return nil, err
	}
	if opts.Analyze {
		p.Warnings = append(p.Warnings,
			"MariaDB/MySQL EXPLAIN FORMAT=JSON does not produce ANALYZE-style actual rows / timing; numeric Actual* fields will be zero")
	}
	return p, nil
}

// mysqlNormalizer implements normalizer for MariaDB.
type mysqlNormalizer struct{}

// mysqlWarning preserves the engine's per-warning fields. PR #6.5 M6:
// triagers need Code and Level, not just Message.
type mysqlWarning struct {
	Code    int    `json:"Code"`
	Level   string `json:"Level"`
	Message string `json:"Message"`
}

// Normalize parses a MariaDB EXPLAIN FORMAT=JSON payload.
//
// The input shape:
//
//	{
//	  "query_block": {
//	    "select_id": 1,
//	    "cost_info": { "query_cost": "1.20" },
//	    "ordering_operation": { ... },
//	    "table": { ... },
//	    "nested_loop": [ { "table": { ... } }, ... ]
//	  },
//	  "warnings": [ {"Code": ..., "Level": "...", "Message": "..." } ]
//	}
func (n *mysqlNormalizer) Normalize(raw []byte, analyzed bool) (*Plan, error) {
	_ = analyzed
	var root struct {
		QueryBlock json.RawMessage `json:"query_block"`
		Warnings   []mysqlWarning  `json:"warnings"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("explain/mysql: parse top-level: %w", err)
	}
	if len(root.QueryBlock) == 0 {
		return nil, fmt.Errorf("explain/mysql: missing query_block")
	}

	ws := &walkState{}
	node, _, err := parseMySQLBlock(root.QueryBlock, ws)
	if err != nil {
		return nil, err
	}

	// PR #6.5 H9: Plan.Total is a rolled-up view of the whole subtree,
	// not just the root node's metrics. For MariaDB the recursive
	// merge already captured the deeper costs; we additionally walk
	// the resulting Node tree to sum metrics consistently so the
	// "total" badge mirrors what the operator sees in the inspector.
	total := rollupMariaDBTotals(node)

	p := &Plan{
		Engine:        EngineMariaDB,
		Root:          node,
		Total:         total,
		Raw:           raw,
		PlanningTimed: false, // PR #6.5 M3: MariaDB does not measure planning time.
	}
	codes := make([]int, 0, len(root.Warnings))
	for _, w := range root.Warnings {
		if w.Message == "" {
			continue
		}
		// PR #6.5 M6: include code + level so the operator can triage.
		switch {
		case w.Code != 0 && w.Level != "":
			p.Warnings = append(p.Warnings, fmt.Sprintf("[%s %d] %s", w.Level, w.Code, w.Message))
		case w.Code != 0:
			p.Warnings = append(p.Warnings, fmt.Sprintf("[%d] %s", w.Code, w.Message))
		case w.Level != "":
			p.Warnings = append(p.Warnings, fmt.Sprintf("[%s] %s", w.Level, w.Message))
		default:
			p.Warnings = append(p.Warnings, w.Message)
		}
		if w.Code != 0 {
			codes = append(codes, w.Code)
		}
	}
	if len(codes) > 0 && p.Root != nil {
		if p.Root.Extras == nil {
			p.Root.Extras = map[string]any{}
		}
		p.Root.Extras["warningCodes"] = codes
	}
	p.Warnings = append(p.Warnings, ws.warnings...)
	return p, nil
}

// rollupMariaDBTotals walks the normalized tree and produces a
// Plan.Total that mirrors the operator's intuition: max cost across
// the subtree, multiplicative row count down the deepest path's join
// chain (already reflected on join nodes thanks to H5), and additive
// buffer counters. (PR #6.5 H9.)
//
// We use the root node's already-merged metrics as the starting point
// because parseMySQLBlock has propagated wrapper cost_info up; this
// function then takes the max of root + any deeper node, which is
// stable even when child nodes were merged with the smaller-than-root
// strategy. The result is a Metrics with all fields populated where
// the underlying tree carried data.
func rollupMariaDBTotals(root *Node) Metrics {
	if root == nil {
		return Metrics{}
	}
	out := root.Metrics
	var walk func(*Node)
	walk = func(n *Node) {
		if n == nil {
			return
		}
		if n.Metrics.CostTotal > out.CostTotal {
			out.CostTotal = n.Metrics.CostTotal
		}
		if n.Metrics.RowsExpected > out.RowsExpected {
			out.RowsExpected = n.Metrics.RowsExpected
		}
		out.BuffersHit += n.Metrics.BuffersHit
		out.BuffersRead += n.Metrics.BuffersRead
		out.BuffersDirtied += n.Metrics.BuffersDirtied
		out.BuffersWritten += n.Metrics.BuffersWritten
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, c := range root.Children {
		walk(c)
	}
	// PR #6.5 N2: CostStart is never populated on the MariaDB path;
	// actively zero it so a future MariaDB version that exposes a
	// startup-cost-equivalent doesn't leak partial / stale values
	// until the parser explicitly opts in.
	out.CostStart = 0
	return out
}

// parseMySQLBlock parses a single query-block-or-sub-block. The same
// recursive structure appears at every level (query_block contains
// table OR nested_loop OR ordering_operation, each of which can
// contain its own children).
//
// Returns the root *Node for the block + the rolled-up Metrics
// (intended to mirror Plan.Total).
//
// Caps recursion depth and total node count via walkState; see
// explain.go for the limits and KNOWN-ISSUES H3.
func parseMySQLBlock(raw json.RawMessage, ws *walkState) (*Node, Metrics, error) {
	if !ws.enter() {
		return nil, Metrics{}, nil
	}
	var block map[string]json.RawMessage
	if err := json.Unmarshal(raw, &block); err != nil {
		return nil, Metrics{}, fmt.Errorf("parse block: %w", err)
	}

	// Top-level metrics: extract query_cost from cost_info if present.
	var totalMetrics Metrics
	if costRaw, ok := block["cost_info"]; ok {
		var cost struct {
			QueryCost any `json:"query_cost"`
		}
		_ = json.Unmarshal(costRaw, &cost)
		totalMetrics.CostTotal = asFloat64(cost.QueryCost)
	}

	ws.depth++
	defer func() { ws.depth-- }()

	// Dispatch on the inner shape. Order matters: ordering_operation
	// wraps nested_loop wraps table.
	//
	// PR #6.5 L7: wrapper cost_info is preserved by passing
	// totalMetrics as the SEED to parseMySQLOperation; the child
	// metrics are merged ON TOP rather than overwriting.
	switch {
	case len(block["ordering_operation"]) > 0:
		inner, m, err := parseMySQLOperation("Ordering", block["ordering_operation"], ws)
		if err != nil {
			return nil, totalMetrics, err
		}
		mergeMetrics(&totalMetrics, m)
		// Attach wrapper-level metrics to the inner node (cost_info on
		// the outer block belongs to the wrapper Kind).
		if inner != nil {
			inner.Metrics = combineWrapperMetrics(inner.Metrics, totalMetrics)
		}
		return inner, totalMetrics, nil
	case len(block["nested_loop"]) > 0:
		inner, m, err := parseMySQLNestedLoop(block["nested_loop"], ws)
		if err != nil {
			return nil, totalMetrics, err
		}
		mergeMetrics(&totalMetrics, m)
		if inner != nil {
			inner.Metrics = combineWrapperMetrics(inner.Metrics, totalMetrics)
		}
		return inner, totalMetrics, nil
	case len(block["grouping_operation"]) > 0:
		inner, m, err := parseMySQLOperation("Grouping", block["grouping_operation"], ws)
		if err != nil {
			return nil, totalMetrics, err
		}
		mergeMetrics(&totalMetrics, m)
		if inner != nil {
			inner.Metrics = combineWrapperMetrics(inner.Metrics, totalMetrics)
		}
		return inner, totalMetrics, nil
	case len(block["duplicates_removal"]) > 0:
		inner, m, err := parseMySQLOperation("Distinct", block["duplicates_removal"], ws)
		if err != nil {
			return nil, totalMetrics, err
		}
		mergeMetrics(&totalMetrics, m)
		if inner != nil {
			inner.Metrics = combineWrapperMetrics(inner.Metrics, totalMetrics)
		}
		return inner, totalMetrics, nil
	case len(block["windowing"]) > 0:
		// PR #6.5 H7 (windowing shape).
		inner, m, err := parseMySQLOperation("Windowing", block["windowing"], ws)
		if err != nil {
			return nil, totalMetrics, err
		}
		mergeMetrics(&totalMetrics, m)
		if inner != nil {
			inner.Metrics = combineWrapperMetrics(inner.Metrics, totalMetrics)
			if inner.Extras == nil {
				inner.Extras = map[string]any{}
			}
			inner.Extras["windowing"] = json.RawMessage(block["windowing"])
		}
		return inner, totalMetrics, nil
	case len(block["table"]) > 0:
		tbl, m, err := parseMySQLTable(block["table"], ws)
		if err != nil {
			return nil, totalMetrics, err
		}
		mergeMetrics(&totalMetrics, m)
		if tbl != nil {
			tbl.Metrics = combineWrapperMetrics(tbl.Metrics, totalMetrics)
		}
		// PR #6.5 H7 (coexisting subquery+table): a query_block may
		// have BOTH a table AND having_subqueries / select_list_subqueries
		// alongside it. Attach the subquery plans as additional
		// children rather than dropping them.
		attachMySQLSubqueries(tbl, block, ws)
		return tbl, totalMetrics, nil
	case len(block["union_result"]) > 0:
		inner, m, err := parseMySQLUnion(block["union_result"], ws)
		if err != nil {
			return nil, totalMetrics, err
		}
		mergeMetrics(&totalMetrics, m)
		if inner != nil {
			inner.Metrics = combineWrapperMetrics(inner.Metrics, totalMetrics)
		}
		return inner, totalMetrics, nil
	case isImpossibleWhereBlock(block):
		// PR #6.5 H7 ("Impossible WHERE").
		return &Node{
			Kind:     "Impossible WHERE",
			Children: []*Node{},
			Metrics:  totalMetrics,
		}, totalMetrics, nil
	}

	// PR #6.5 H7 + L9: unknown shape — emit a warning that names the
	// keys actually present, so operators (and future maintainers)
	// know what the engine emitted. The placeholder Node is kept so
	// the UI can still render *something*.
	keys := make([]string, 0, len(block))
	for k := range block {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ws.warn(fmt.Sprintf("MariaDB block shape not recognized: keys=[%s] — see Plan.Raw", strings.Join(keys, ",")))
	return &Node{
		Kind:     "Unknown",
		Children: []*Node{},
		Metrics:  totalMetrics,
	}, totalMetrics, nil
}

// isImpossibleWhereBlock detects MariaDB's "Impossible WHERE" optimizer
// shape — a query_block with a single "message" field whose value is
// "Impossible WHERE noticed after reading const tables" or similar.
func isImpossibleWhereBlock(block map[string]json.RawMessage) bool {
	msgRaw, ok := block["message"]
	if !ok {
		return false
	}
	var msg string
	if err := json.Unmarshal(msgRaw, &msg); err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(msg), "impossible where") ||
		strings.Contains(strings.ToLower(msg), "no matching")
}

// attachMySQLSubqueries appends having_subqueries / select_list_subqueries
// children when present (PR #6.5 H7).
func attachMySQLSubqueries(parent *Node, block map[string]json.RawMessage, ws *walkState) {
	if parent == nil {
		return
	}
	for _, key := range []string{"having_subqueries", "select_list_subqueries"} {
		raw, ok := block[key]
		if !ok || len(raw) == 0 {
			continue
		}
		var subs []json.RawMessage
		if err := json.Unmarshal(raw, &subs); err != nil {
			continue
		}
		for _, sub := range subs {
			var sb map[string]json.RawMessage
			if err := json.Unmarshal(sub, &sb); err != nil {
				continue
			}
			qb, ok := sb["query_block"]
			if !ok {
				continue
			}
			child, _, err := parseMySQLBlock(qb, ws)
			if err == nil && child != nil {
				if child.Extras == nil {
					child.Extras = map[string]any{}
				}
				child.Extras["subqueryRole"] = strings.TrimSuffix(key, "_subqueries")
				parent.Children = append(parent.Children, child)
			}
		}
	}
}

// combineWrapperMetrics layers the wrapper block's own cost on top of
// the inner node's metrics. Wrapper cost is preserved when greater
// (PR #6.5 L7); inner metrics dominate otherwise. Buffers / rows are
// taken from the inner node since the wrapper itself doesn't expose
// per-row counts.
func combineWrapperMetrics(inner, wrapper Metrics) Metrics {
	out := inner
	if wrapper.CostTotal > out.CostTotal {
		out.CostTotal = wrapper.CostTotal
	}
	return out
}

// parseMySQLOperation handles ordering / grouping / duplicates-removal
// / windowing wrappers. Each contains a child block which is itself a
// recursive structure.
func parseMySQLOperation(kind string, raw json.RawMessage, ws *walkState) (*Node, Metrics, error) {
	child, m, err := parseMySQLBlock(raw, ws)
	if err != nil {
		return nil, Metrics{}, err
	}
	node := &Node{
		Kind:     kind,
		Metrics:  m,
		Children: []*Node{},
	}
	if child != nil {
		node.Children = append(node.Children, child)
	}
	return node, m, nil
}

// parseMySQLNestedLoop handles the `nested_loop: [ {table: ...}, ... ]`
// shape — an array of join children. We model the join itself as one
// Node with each table as a child.
//
// PR #6.5 H5: Nested Loop cardinality is multiplicative, not additive.
// A Nested Loop with outer=100 inner=10 produces 1000 rows, not 110.
// We compute RowsExpected as the product of children's RowsExpected
// (skipping zeros), so the badge reflects engine reality.
//
// PR #6.5 L6: entries without a "table" key (e.g., when block-nl-join
// surfaces as its own nested object) are now traversed as generic
// sub-blocks rather than silently dropped.
func parseMySQLNestedLoop(raw json.RawMessage, ws *walkState) (*Node, Metrics, error) {
	var arr []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, Metrics{}, fmt.Errorf("parse nested_loop: %w", err)
	}
	node := &Node{
		Kind:     "Nested Loop",
		JoinType: "Inner", // MariaDB's plain nested_loop is INNER unless an outer join wrapper present.
		Children: []*Node{},
	}
	var total Metrics
	for _, entry := range arr {
		var (
			child *Node
			m     Metrics
			err   error
		)
		if tableRaw, ok := entry["table"]; ok {
			child, m, err = parseMySQLTable(tableRaw, ws)
		} else {
			// PR #6.5 L6: non-table entry (e.g., block-nl-join wrapper).
			// Re-marshal the entry and dispatch back through
			// parseMySQLBlock.
			b, mErr := json.Marshal(entry)
			if mErr != nil {
				continue
			}
			child, m, err = parseMySQLBlock(b, ws)
		}
		if err != nil {
			return nil, total, err
		}
		if child == nil {
			break
		}
		node.Children = append(node.Children, child)
		mergeMetrics(&total, m)
	}

	// PR #6.5 H5: replace additive RowsExpected with the multiplicative
	// product across child cardinalities.
	var rows int64 = 1
	any := false
	for _, c := range node.Children {
		r := c.Metrics.RowsExpected
		if r <= 0 {
			continue
		}
		// Overflow guard: cap at MaxInt64 / next factor before multiplying.
		if rows > 0 && r > 0 && rows > (1<<63-1)/r {
			rows = 1<<63 - 1
			any = true
			break
		}
		rows *= r
		any = true
	}
	if any {
		total.RowsExpected = rows
	} else {
		total.RowsExpected = 0
	}
	node.Metrics = total
	return node, total, nil
}

// parseMySQLTable parses a single table-access node.
//
// PR #6.5 M5: RowsExpected is now sourced from rows_examined_per_scan
// (the examined-per-scan count, which is the metric operators reach for
// when reasoning about scan cost). When rows_produced_per_join is also
// present, it is preserved on Extras["rowsProducedPerJoin"] so the
// inspector can show both. (Previous behavior overwrote one with the
// other, dropping context.)
func parseMySQLTable(raw json.RawMessage, ws *walkState) (*Node, Metrics, error) {
	var t struct {
		TableName      string `json:"table_name"`
		AccessType     string `json:"access_type"`
		Key            string `json:"key"`
		KeyLength      any    `json:"key_length"`
		RowsExaminedPS any    `json:"rows_examined_per_scan"`
		RowsProducedPJ any    `json:"rows_produced_per_join"`
		Filtered       any    `json:"filtered"`
		AttachedCond   string `json:"attached_condition"`
		CostInfo       struct {
			ReadCost        any `json:"read_cost"`
			EvalCost        any `json:"eval_cost"`
			PrefixCost      any `json:"prefix_cost"`
			DataReadPerJoin any `json:"data_read_per_join"`
		} `json:"cost_info"`
		// MaterializedFromSubquery + sibling nesting handled below.
		MaterializedFromSubquery json.RawMessage   `json:"materialized_from_subquery"`
		AttachedSubqueries       []json.RawMessage `json:"attached_subqueries"`
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, Metrics{}, fmt.Errorf("parse table: %w", err)
	}

	kind := mysqlAccessKind(t.AccessType)
	rowsExamined := asInt64(t.RowsExaminedPS)
	metrics := Metrics{
		CostTotal:    asFloat64(t.CostInfo.PrefixCost),
		RowsExpected: rowsExamined,
		// CostStart actively zeroed (PR #6.5 N2).
		CostStart: 0,
	}

	node := &Node{
		Kind:     kind,
		Relation: t.TableName,
		Index:    t.Key,
		Filter:   t.AttachedCond,
		Metrics:  metrics,
		Children: []*Node{},
		Extras:   map[string]any{},
	}
	if t.AccessType != "" {
		node.Extras["accessType"] = t.AccessType
	}
	if rowsExamined > 0 {
		node.Extras["rowsExaminedPerScan"] = rowsExamined
	}
	if r := asInt64(t.RowsProducedPJ); r > 0 {
		// PR #6.5 M5: preserve both, do not overwrite RowsExpected.
		node.Extras["rowsProducedPerJoin"] = r
	}
	if dr := asFloat64(t.CostInfo.DataReadPerJoin); dr > 0 {
		// PR #6.5 M4: "1K" / "10M" suffixes now parse correctly via
		// asFloat64; surface the bytes-per-join estimate to the
		// inspector.
		node.Extras["dataReadPerJoinBytes"] = int64(dr)
	}
	if len(node.Extras) == 0 {
		node.Extras = nil
	}

	// Materialized subquery: nest the sub-plan under this node.
	if len(t.MaterializedFromSubquery) > 0 {
		var mat map[string]json.RawMessage
		if err := json.Unmarshal(t.MaterializedFromSubquery, &mat); err == nil {
			if qb, ok := mat["query_block"]; ok {
				child, _, err := parseMySQLBlock(qb, ws)
				if err == nil && child != nil {
					node.Children = append(node.Children, child)
				}
			}
		}
	}

	// Attached subqueries: each is its own sub-plan.
	for _, sub := range t.AttachedSubqueries {
		var subBlock map[string]json.RawMessage
		if err := json.Unmarshal(sub, &subBlock); err == nil {
			if qb, ok := subBlock["query_block"]; ok {
				child, _, err := parseMySQLBlock(qb, ws)
				if err == nil && child != nil {
					node.Children = append(node.Children, child)
				}
			}
		}
	}

	return node, metrics, nil
}

// parseMySQLUnion handles the union_result wrapper.
//
// PR #6.5 L5: nested unions inside a union spec are now recursed
// through the generic parseMySQLBlock dispatcher (which itself
// re-enters parseMySQLUnion for nested union_result), so deeply nested
// UNIONs no longer drop on the floor.
func parseMySQLUnion(raw json.RawMessage, ws *walkState) (*Node, Metrics, error) {
	var u struct {
		QuerySpecs []map[string]json.RawMessage `json:"query_specifications"`
	}
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, Metrics{}, fmt.Errorf("parse union: %w", err)
	}
	node := &Node{
		Kind:     "Union",
		Children: []*Node{},
	}
	var total Metrics
	for _, spec := range u.QuerySpecs {
		var (
			child *Node
			m     Metrics
			err   error
		)
		switch {
		case len(spec["query_block"]) > 0:
			child, m, err = parseMySQLBlock(spec["query_block"], ws)
		default:
			// Nested union_result or other wrapper inside this spec —
			// re-marshal the spec body and recurse.
			b, mErr := json.Marshal(spec)
			if mErr != nil {
				continue
			}
			child, m, err = parseMySQLBlock(b, ws)
		}
		if err != nil {
			return nil, total, err
		}
		if child == nil {
			break
		}
		node.Children = append(node.Children, child)
		mergeMetrics(&total, m)
	}
	node.Metrics = total
	return node, total, nil
}

// mysqlAccessKind maps MariaDB's compact access_type code to the
// human-readable name the frontend renders.
//
// PR #6.5 L4: added index_merge / index_subquery / unique_subquery.
func mysqlAccessKind(access string) string {
	switch access {
	case "system":
		return "System Scan"
	case "const":
		return "Const Lookup"
	case "eq_ref":
		return "Unique Index Lookup"
	case "ref", "ref_or_null":
		return "Index Lookup"
	case "fulltext":
		return "Fulltext Index"
	case "range":
		return "Index Range Scan"
	case "index":
		return "Index Scan"
	case "index_merge":
		return "Index Merge"
	case "index_subquery":
		return "Index Subquery"
	case "unique_subquery":
		return "Unique Subquery"
	case "ALL":
		return "Full Table Scan"
	case "":
		return "Table Scan"
	}
	return "Table Scan (" + access + ")"
}

// mergeMetrics adds the relevant fields of `from` into `into`. Used
// for rolling sub-node costs up into a parent node.
//
// PR #6.5 H5: RowsExpected is NO LONGER summed here — join cardinality
// is now computed multiplicatively at the join node itself
// (parseMySQLNestedLoop). For non-join contexts mergeMetrics still
// needs to combine cardinalities; we take the max so we don't
// underestimate, which is a less wrong choice than the previous sum
// (which double-counted shared inputs) while we don't yet differentiate
// "child of join" from "child of wrapper". A future PR may refine this.
//
// PR #6.5 M10: previous additive RowsExpected behavior also double-
// counted via wrapper nesting (ordering wrapper's parent inherited
// child sums + the wrapper's own pass-through). The max-strategy makes
// the behavior monotonic instead of cumulative.
func mergeMetrics(into *Metrics, from Metrics) {
	if from.CostTotal > into.CostTotal {
		into.CostTotal = from.CostTotal
	}
	if from.RowsExpected > into.RowsExpected {
		into.RowsExpected = from.RowsExpected
	}
	if from.RowsActual > into.RowsActual {
		into.RowsActual = from.RowsActual
	}
	if from.TimeTotalMS > into.TimeTotalMS {
		into.TimeTotalMS = from.TimeTotalMS
	}
	into.BuffersHit += from.BuffersHit
	into.BuffersRead += from.BuffersRead
	into.BuffersDirtied += from.BuffersDirtied
	into.BuffersWritten += from.BuffersWritten
}

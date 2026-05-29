package explain

import (
	"context"
	"encoding/json"
	"fmt"

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

// mysqlNormalizer implements Normalizer for MariaDB.
type mysqlNormalizer struct{}

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
//	  "warnings": [ {"Code": ..., "Message": "..." } ]   (optional)
//	}
func (n *mysqlNormalizer) Normalize(raw []byte, analyzed bool) (*Plan, error) {
	_ = analyzed
	var root struct {
		QueryBlock json.RawMessage `json:"query_block"`
		Warnings   []struct {
			Message string `json:"Message"`
		} `json:"warnings"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("explain/mysql: parse top-level: %w", err)
	}
	if len(root.QueryBlock) == 0 {
		return nil, fmt.Errorf("explain/mysql: missing query_block")
	}

	ws := &walkState{}
	node, total, err := parseMySQLBlock(root.QueryBlock, ws)
	if err != nil {
		return nil, err
	}

	p := &Plan{
		Engine: "mariadb",
		Root:   node,
		Total:  total,
		Raw:    raw,
	}
	for _, w := range root.Warnings {
		if w.Message != "" {
			p.Warnings = append(p.Warnings, w.Message)
		}
	}
	p.Warnings = append(p.Warnings, ws.warnings...)
	return p, nil
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
	switch {
	case len(block["ordering_operation"]) > 0:
		inner, m, err := parseMySQLOperation("Ordering", block["ordering_operation"], ws)
		if err != nil {
			return nil, totalMetrics, err
		}
		mergeMetrics(&totalMetrics, m)
		return inner, totalMetrics, nil
	case len(block["nested_loop"]) > 0:
		inner, m, err := parseMySQLNestedLoop(block["nested_loop"], ws)
		if err != nil {
			return nil, totalMetrics, err
		}
		mergeMetrics(&totalMetrics, m)
		return inner, totalMetrics, nil
	case len(block["grouping_operation"]) > 0:
		inner, m, err := parseMySQLOperation("Grouping", block["grouping_operation"], ws)
		if err != nil {
			return nil, totalMetrics, err
		}
		mergeMetrics(&totalMetrics, m)
		return inner, totalMetrics, nil
	case len(block["duplicates_removal"]) > 0:
		inner, m, err := parseMySQLOperation("Distinct", block["duplicates_removal"], ws)
		if err != nil {
			return nil, totalMetrics, err
		}
		mergeMetrics(&totalMetrics, m)
		return inner, totalMetrics, nil
	case len(block["table"]) > 0:
		tbl, m, err := parseMySQLTable(block["table"], ws)
		if err != nil {
			return nil, totalMetrics, err
		}
		mergeMetrics(&totalMetrics, m)
		return tbl, totalMetrics, nil
	case len(block["union_result"]) > 0:
		inner, m, err := parseMySQLUnion(block["union_result"], ws)
		if err != nil {
			return nil, totalMetrics, err
		}
		mergeMetrics(&totalMetrics, m)
		return inner, totalMetrics, nil
	}

	// Unknown shape — return a placeholder Node so the UI can still
	// render *something* and the operator can switch to the Raw tab.
	return &Node{
		Kind:     "Unknown",
		Children: []*Node{},
	}, totalMetrics, nil
}

// parseMySQLOperation handles ordering / grouping / duplicates-removal
// wrappers. Each contains a child block which is itself a recursive
// structure.
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
func parseMySQLNestedLoop(raw json.RawMessage, ws *walkState) (*Node, Metrics, error) {
	var arr []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, Metrics{}, fmt.Errorf("parse nested_loop: %w", err)
	}
	node := &Node{
		Kind:     "Nested Loop",
		Children: []*Node{},
	}
	var total Metrics
	for _, entry := range arr {
		if tableRaw, ok := entry["table"]; ok {
			child, m, err := parseMySQLTable(tableRaw, ws)
			if err != nil {
				return nil, total, err
			}
			if child == nil {
				// Depth/node cap fired; stop accumulating siblings.
				break
			}
			node.Children = append(node.Children, child)
			mergeMetrics(&total, m)
		}
	}
	node.Metrics = total
	return node, total, nil
}

// parseMySQLTable parses a single table-access node.
func parseMySQLTable(raw json.RawMessage, ws *walkState) (*Node, Metrics, error) {
	var t struct {
		TableName        string `json:"table_name"`
		AccessType       string `json:"access_type"`
		Key              string `json:"key"`
		KeyLength        any    `json:"key_length"`
		RowsExaminedPS   any    `json:"rows_examined_per_scan"`
		RowsProducedPJ   any    `json:"rows_produced_per_join"`
		Filtered         any    `json:"filtered"`
		AttachedCond     string `json:"attached_condition"`
		CostInfo         struct {
			ReadCost  any `json:"read_cost"`
			EvalCost  any `json:"eval_cost"`
			PrefixCost any `json:"prefix_cost"`
			DataReadPerJoin any `json:"data_read_per_join"`
		} `json:"cost_info"`
		// MaterializedFromSubquery + sibling nesting handled below.
		MaterializedFromSubquery json.RawMessage `json:"materialized_from_subquery"`
		AttachedSubqueries       []json.RawMessage `json:"attached_subqueries"`
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, Metrics{}, fmt.Errorf("parse table: %w", err)
	}

	kind := mysqlAccessKind(t.AccessType)
	metrics := Metrics{
		CostTotal:    asFloat64(t.CostInfo.PrefixCost),
		RowsExpected: asInt64(t.RowsExaminedPS),
	}
	if r := asInt64(t.RowsProducedPJ); r > 0 {
		metrics.RowsExpected = r
	}

	node := &Node{
		Kind:     kind,
		Relation: t.TableName,
		Index:    t.Key,
		Filter:   t.AttachedCond,
		Metrics:  metrics,
		Children: []*Node{},
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
		if qb, ok := spec["query_block"]; ok {
			child, m, err := parseMySQLBlock(qb, ws)
			if err != nil {
				return nil, total, err
			}
			if child == nil {
				break
			}
			node.Children = append(node.Children, child)
			mergeMetrics(&total, m)
		}
	}
	node.Metrics = total
	return node, total, nil
}

// mysqlAccessKind maps MariaDB's compact access_type code to the
// human-readable name the frontend renders.
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
	case "ALL":
		return "Full Table Scan"
	case "":
		return "Table Scan"
	}
	return "Table Scan (" + access + ")"
}

// mergeMetrics adds the relevant fields of `from` into `into`. Used
// for rolling sub-node costs up into a parent node.
func mergeMetrics(into *Metrics, from Metrics) {
	if from.CostTotal > into.CostTotal {
		into.CostTotal = from.CostTotal
	}
	into.RowsExpected += from.RowsExpected
	into.RowsActual += from.RowsActual
	if from.TimeTotalMS > into.TimeTotalMS {
		into.TimeTotalMS = from.TimeTotalMS
	}
	into.BuffersHit += from.BuffersHit
	into.BuffersRead += from.BuffersRead
	into.BuffersWritten += from.BuffersWritten
}

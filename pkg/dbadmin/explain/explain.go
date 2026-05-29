package explain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/classifier"
	"github.com/auracp/auracp/pkg/dbadmin/driver"
)

// Plan is the engine-agnostic structured view of an EXPLAIN result.
// The frontend renders it as a tree (one Node per row in the flame
// view); every field is populated best-effort from whichever fields the
// engine exposes. Fields that don't apply to a given engine remain
// zero-valued.
type Plan struct {
	// Engine identifies which engine produced this plan. Used by the
	// frontend to label the result and choose engine-specific
	// tooltips.
	Engine string `json:"engine"`

	// Root is the top of the plan tree. Never nil for a valid plan;
	// a Plan returned with a non-nil Error and a nil Root indicates
	// the engine returned no plan node (rare; usually a parse error
	// that the driver layer caught first).
	Root *Node `json:"root"`

	// Total is the aggregate metrics for the top-level node. Mirrors
	// Root.Metrics for convenience — operators read Plan.Total for
	// the "total cost" / "total time" badge without traversing.
	Total Metrics `json:"total"`

	// Warnings carries engine-supplied warnings AND any parser-emitted
	// warnings (e.g. tree truncated due to depth cap, MariaDB shape
	// not yet recognized). For MariaDB this includes the engine's
	// "warnings" array. For Postgres, NOTICE messages are NOT
	// included in this list (pgx surfaces them via a separate channel
	// that the package does not currently subscribe to; see
	// KNOWN-ISSUES.md PR #6.5).
	Warnings []string `json:"warnings,omitempty"`

	// PlanningTimeMS is engine-reported planning time. Available on
	// Postgres with ANALYZE; MariaDB doesn't expose it separately
	// (we approximate by setting to 0).
	PlanningTimeMS float64 `json:"planningTimeMs"`

	// ExecutionTimeMS is the total wall-clock execution time. Only
	// populated when EXPLAIN ANALYZE ran; otherwise zero.
	ExecutionTimeMS float64 `json:"executionTimeMs"`

	// Raw preserves the verbatim engine output (the JSON bytes the
	// driver returned). Lets the UI's "show raw" tab work and lets
	// operators paste it into engine-native tools.
	Raw json.RawMessage `json:"raw,omitempty"`
}

// Node is one entry in the plan tree. Each row in the frontend's flame
// view corresponds to one Node.
type Node struct {
	// Kind is the engine's node-type name, surfaced verbatim:
	// MariaDB: "Index Scan" / "Range Scan" / "Block Nested Loop" / etc.
	// Postgres: "Seq Scan" / "Index Scan" / "Hash Join" / etc.
	// The frontend's color map keys on this string.
	Kind string `json:"kind"`

	// Relation is the table the node operates on, when applicable.
	// Empty for join nodes, sort nodes, aggregate nodes.
	Relation string `json:"relation"`

	// Schema is the schema-qualifier of Relation. Postgres surfaces
	// it explicitly; MariaDB's information is inferred from context.
	Schema string `json:"schema"`

	// Alias is the operator-specified alias for the relation, if any.
	Alias string `json:"alias"`

	// Index is the index used, when applicable.
	Index string `json:"index"`

	// JoinType is for join nodes ("Nested Loop", "Hash Join", "Merge
	// Join") — distinguishes inner / left / right / anti / semi.
	JoinType string `json:"joinType"`

	// Filter is the engine-reported filter predicate applied at this
	// node. The frontend tooltips this on hover.
	Filter string `json:"filter"`

	// Children are the inputs to this node. Leaf nodes have an empty
	// slice (not nil) so JSON-marshaling stays consistent.
	Children []*Node `json:"children"`

	// Metrics is the per-node cost / time / row estimates.
	Metrics Metrics `json:"metrics"`
}

// Metrics captures cost + timing + buffers for one node. All values are
// engine-normalized; missing values stay zero.
type Metrics struct {
	// CostStart is the cumulative cost before the first row is
	// produced. Postgres: "Startup Cost". MariaDB: closest available
	// equivalent (often 0).
	CostStart float64 `json:"costStart"`

	// CostTotal is the cumulative cost through the last row.
	// Postgres: "Total Cost". MariaDB: "cost_info.query_cost" at the
	// root + sub-node estimates.
	CostTotal float64 `json:"costTotal"`

	// RowsExpected is the planner's estimate of row count for this
	// node. Postgres: "Plan Rows". MariaDB: "rows_examined_per_scan"
	// or "rows_produced_per_join", whichever the node exposes.
	RowsExpected int64 `json:"rowsExpected"`

	// RowsActual is the observed row count after execution. Only
	// populated by EXPLAIN ANALYZE. Postgres: "Actual Rows".
	// MariaDB doesn't expose this; stays zero unless we run a
	// follow-up ANALYZE TABLE.
	RowsActual int64 `json:"rowsActual"`

	// TimeStartMS is the wall-clock time to first row (post-ANALYZE).
	TimeStartMS float64 `json:"timeStartMs"`

	// TimeTotalMS is the wall-clock time through last row.
	TimeTotalMS float64 `json:"timeTotalMs"`

	// Loops is the number of times this node was executed during
	// query run (e.g., the inner of a nested loop join). Postgres
	// reports this; MariaDB doesn't but the multiplier is implicit.
	Loops int64 `json:"loops"`

	// BuffersHit is shared-buffer hits (no disk I/O). Postgres only,
	// with BUFFERS option.
	BuffersHit int64 `json:"buffersHit"`

	// BuffersRead is shared-buffer reads (cold cache → disk I/O).
	// Postgres only.
	BuffersRead int64 `json:"buffersRead"`

	// BuffersWritten is shared-buffer writes (rare except for
	// CREATE INDEX, etc.). Postgres only.
	BuffersWritten int64 `json:"buffersWritten"`
}

// Normalizer parses engine-specific JSON output into a Plan tree.
// Implementations are stateless.
type Normalizer interface {
	// Normalize parses raw EXPLAIN JSON output and returns a Plan.
	// The raw bytes are also preserved on Plan.Raw.
	Normalize(raw []byte, analyzed bool) (*Plan, error)
}

// ExplainOpts configures Explain.
type ExplainOpts struct {
	// SQL is the operator's query to EXPLAIN. The classifier should
	// have already approved this as a read-class statement before
	// this package sees it.
	SQL string

	// Analyze controls whether EXPLAIN ANALYZE runs (Postgres) or
	// EXPLAIN with full metrics (MariaDB). When true, the query is
	// actually EXECUTED on the server — destructive for writes; the
	// engine layer must gate this on classifier.QueryClass ==
	// ClassRead and an explicit operator opt-in.
	Analyze bool

	// Class is the classifier's verdict on opts.SQL. When Analyze
	// is true, the package REFUSES to proceed unless Class is
	// explicitly classifier.ClassRead. Callers that haven't run
	// the classifier upstream can leave Class as zero-value
	// (ClassRead's zero, which is 0 = ClassRead by convention) but
	// they must then also leave Analyze false; the gate is
	// structural.
	//
	// (NOTE: classifier.ClassRead == 0; leaving Class unset and
	// Analyze unset is the safe default. The gate fires when
	// Analyze=true AND Class != ClassRead.)
	Class classifier.QueryClass

	// Limits applies to the EXPLAIN-wrapping query. Defaults to a
	// conservative 60s timeout (EXPLAIN ANALYZE can be slow).
	Limits driver.Limits
}

// Explain runs EXPLAIN against the conn and returns the normalized
// Plan. Engine is derived from the conn's reported engine (passed in
// explicitly because driver.Conn doesn't expose it directly).
func Explain(ctx context.Context, conn driver.Conn, engine dbadmin.EngineKind, opts ExplainOpts) (*Plan, error) {
	if conn == nil {
		return nil, errors.New("explain: nil driver.Conn")
	}
	if opts.SQL == "" {
		return nil, errors.New("explain: empty SQL")
	}
	// Structural gate (see ExplainOpts.Class): Analyze=true on a
	// non-read class actually EXECUTES the statement under
	// EXPLAIN ANALYZE, which would mutate data. Refuse here so a
	// missed upstream check doesn't lose data.
	if opts.Analyze && opts.Class != classifier.ClassRead {
		return nil, fmt.Errorf("explain: Analyze=true refused for class %v (must be ClassRead)", opts.Class)
	}
	limits := opts.Limits
	if limits.Timeout == 0 {
		limits.Timeout = defaultExplainTimeout
	}
	if limits.MaxRows == 0 {
		limits.MaxRows = 1 // EXPLAIN ... FORMAT JSON returns exactly one row
	}
	if limits.MaxBytes == 0 {
		limits.MaxBytes = defaultMaxBytes
	}

	switch engine {
	case dbadmin.EngineMariaDB:
		return mysqlExplain(ctx, conn, opts, limits)
	case dbadmin.EnginePostgres:
		return postgresExplain(ctx, conn, opts, limits)
	}
	return nil, fmt.Errorf("explain: unsupported engine %v", engine)
}

// defaultExplainTimeout caps the wrapping query at 60s. EXPLAIN
// ANALYZE on big queries genuinely takes that long; the driver layer's
// own 30s default would mask slow plans.
const defaultExplainTimeout = 60 * time.Second

// defaultMaxBytes caps the EXPLAIN JSON payload size. The largest
// engine plans we've observed are ~1 MB (deeply nested unions); 8 MB
// is generous headroom.
const defaultMaxBytes = 8 * 1024 * 1024

// Hard ceilings on plan structure. A real-world plan rarely
// exceeds depth 50 or 5,000 nodes; these caps protect against
// hostile / buggy backends.
const maxPlanDepth = 256
const maxPlanNodes = 10_000

// walkState carries depth + node-count + collected warnings through a
// recursive walk. Each walker increments nodes on entry and depth on
// descent; when either crosses its ceiling, the walker returns nil
// (truncating the subtree) and appends a warning. The caller then
// surfaces walkState.warnings onto Plan.Warnings.
type walkState struct {
	depth    int
	nodes    int
	warnings []string
}

// enter increments node count and tests both ceilings. Returns true
// when the walker may continue, false when the caller must stop
// recursing and (typically) return nil for the current child.
func (w *walkState) enter() bool {
	w.nodes++
	if w.nodes > maxPlanNodes {
		w.warn(fmt.Sprintf("plan tree truncated at node %d (limit %d) — see Plan.Raw for full data", w.nodes, maxPlanNodes))
		return false
	}
	if w.depth > maxPlanDepth {
		w.warn(fmt.Sprintf("plan tree truncated at depth %d (limit %d) — see Plan.Raw for full data", w.depth, maxPlanDepth))
		return false
	}
	return true
}

// warn appends a deduplicated warning. The dedup keeps the truncation
// message from being repeated once per node beyond the cap.
func (w *walkState) warn(msg string) {
	for _, m := range w.warnings {
		if m == msg {
			return
		}
	}
	w.warnings = append(w.warnings, msg)
}

// ─── Helpers shared between normalizers ──────────────────────────────

// readSingleJSONRow fetches the single-row JSON result that both
// engines produce for EXPLAIN ... FORMAT JSON, returning the raw
// bytes.
//
// Enforces limits.MaxBytes after the row is extracted. The
// driver.LimitedRows byte cap is pre-fetch — with MaxRows=1 it never
// fires before Next, so a hostile backend returning a multi-GB plan
// would OOM the process. This cap is the structural defense.
func readSingleJSONRow(ctx context.Context, conn driver.Conn, limits driver.Limits, sqlText string) ([]byte, error) {
	rs, err := conn.Query(ctx, limits, sqlText)
	if err != nil {
		return nil, err
	}
	defer rs.Close()

	vals, err := rs.Next(ctx)
	if errors.Is(err, driver.ErrEOF) {
		return nil, errors.New("explain: engine returned no rows")
	}
	if err != nil {
		return nil, err
	}
	if len(vals) == 0 {
		return nil, errors.New("explain: engine row has no columns")
	}

	var raw []byte
	switch x := vals[0].(type) {
	case []byte:
		raw = x
	case string:
		raw = []byte(x)
	case nil:
		return nil, errors.New("explain: engine returned NULL plan")
	default:
		// Both pgx and go-sql-driver return JSON as []byte or string.
		// Anything else is unexpected.
		return nil, fmt.Errorf("explain: unexpected plan column type %T", x)
	}

	if limits.MaxBytes > 0 && int64(len(raw)) > limits.MaxBytes {
		return nil, fmt.Errorf("explain: plan payload %d bytes exceeds cap %d", len(raw), limits.MaxBytes)
	}
	return raw, nil
}

// asFloat64 coerces a JSON number (which may decode as float64 or
// string depending on the engine + driver) into a float64.
//
// String parsing uses strconv (not fmt.Sscanf): we need to (a) reject
// NaN / +Inf / -Inf because encoding/json refuses to marshal them
// (the entire Plan would then fail to serialize) and (b) avoid
// Sscanf's silent acceptance of overflow values like "1e500" → +Inf.
// On any of those conditions we return 0; a quiet zero is preferable
// to losing the whole Plan downstream.
func asFloat64(v any) float64 {
	switch x := v.(type) {
	case float64:
		return sanitizeFloat(x)
	case float32:
		return sanitizeFloat(float64(x))
	case int64:
		return float64(x)
	case int:
		return float64(x)
	case string:
		f, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return 0
		}
		return sanitizeFloat(f)
	}
	return 0
}

// asInt64 coerces a JSON number into int64.
func asInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0
		}
		return int64(x)
	case string:
		n, err := strconv.ParseInt(x, 10, 64)
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}

// sanitizeFloat returns 0 for NaN / +Inf / -Inf, the input unchanged
// otherwise. Used wherever a float64 ends up on a struct that the
// HTTP handler will marshal with encoding/json — which refuses to
// emit "NaN" or "Inf" and fails the whole document.
func sanitizeFloat(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return f
}

// asString coerces a JSON string into Go string. Common for the
// MariaDB style where numbers are sometimes quoted.
func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	case nil:
		return ""
	}
	return fmt.Sprintf("%v", v)
}

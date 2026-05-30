package explain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/classifier"
	"github.com/auracp/auracp/pkg/dbadmin/driver"
)

// Engine name constants (PR #6.5 M13). The values match what the
// engine surfaces on Plan.Engine; frontend dispatch keys on these.
const (
	EngineMariaDB  = "mariadb"
	EnginePostgres = "postgres"
)

// Plan is the engine-agnostic structured view of an EXPLAIN result.
// The frontend renders it as a tree (one Node per row in the flame
// view); every field is populated best-effort from whichever fields the
// engine exposes. Fields that don't apply to a given engine remain
// zero-valued.
//
// Additive-stability (PR #6.5 N1): new fields may be appended to this
// struct in any release. Existing fields are never removed or
// repurposed in a non-backwards-compatible way. Frontends MUST ignore
// unknown JSON keys.
type Plan struct {
	// Engine identifies which engine produced this plan. One of
	// EngineMariaDB or EnginePostgres. Used by the frontend to label
	// the result and choose engine-specific tooltips.
	Engine string `json:"engine"`

	// Root is the top of the plan tree. Never nil for a valid plan;
	// a Plan returned with a non-nil Error and a nil Root indicates
	// the engine returned no plan node (rare; usually a parse error
	// that the driver layer caught first).
	Root *Node `json:"root"`

	// Total is the aggregate metrics for the plan. For Postgres this
	// mirrors Root.Metrics (the root node already aggregates the
	// subtree); for MariaDB this is a rolled-up sum from every node
	// in the tree (PR #6.5 H9) — operators read Plan.Total for the
	// "total cost" / "total time" badge without traversing.
	Total Metrics `json:"total"`

	// Warnings carries engine-supplied warnings AND any parser-emitted
	// warnings (e.g. tree truncated due to depth cap, MariaDB shape
	// not yet recognized). For MariaDB this includes the engine's
	// "warnings" array; each entry is prefixed with the code+level
	// (PR #6.5 M6). For Postgres, NOTICE messages are NOT included in
	// this list (pgx surfaces them via a separate channel that the
	// package does not currently subscribe to).
	Warnings []string `json:"warnings,omitempty"`

	// PlanningTimeMS is engine-reported planning time. Available on
	// Postgres with ANALYZE; MariaDB doesn't expose it separately.
	//
	// Zero is ambiguous: it can mean "not measured" (MariaDB always,
	// Postgres without ANALYZE) OR "sub-microsecond" on Postgres
	// ANALYZE. Use PlanningTimed (PR #6.5 M3) to disambiguate.
	PlanningTimeMS float64 `json:"planningTimeMs"`

	// PlanningTimed reports whether the engine measured planning time
	// at all. True when PlanningTimeMS came from a real engine
	// measurement (Postgres ANALYZE); false when planning time was
	// not produced (MariaDB always, Postgres without ANALYZE).
	// Distinguishes "0.0 ms — too fast to measure" from "no
	// measurement taken." (PR #6.5 M3.)
	PlanningTimed bool `json:"planningTimed"`

	// ExecutionTimeMS is the total wall-clock execution time. Only
	// populated when EXPLAIN ANALYZE ran; otherwise zero.
	ExecutionTimeMS float64 `json:"executionTimeMs"`

	// Raw preserves the verbatim engine output (the JSON bytes the
	// driver returned). Lets the UI's "show raw" tab work and lets
	// operators paste it into engine-native tools.
	//
	// Shape is engine-specific (see doc.go): a JSON array on Postgres,
	// a JSON object on MariaDB.
	//
	// Omitted from the response body when ExplainOpts.OmitRaw is set
	// (PR #6.5 M9), to keep size-constrained responses small.
	Raw json.RawMessage `json:"raw,omitempty"`
}

// Node is one entry in the plan tree. Each row in the frontend's flame
// view corresponds to one Node.
//
// Additive-stability (PR #6.5 N1): new fields may be appended.
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
	//
	// Postgres has several condition kinds (Filter, Index Cond, Hash
	// Cond, Merge Cond, Recheck Cond). Filter is the first non-empty
	// one in display priority; the FULL set is preserved on Extras
	// (PR #6.5 M15) under keys "filter", "indexCond", "hashCond",
	// "mergeCond", "recheckCond" so the inspector pane can show all.
	Filter string `json:"filter"`

	// Children are the inputs to this node. Leaf nodes have an empty
	// slice (not nil) so JSON-marshaling stays consistent.
	Children []*Node `json:"children"`

	// Metrics is the per-node cost / time / row estimates.
	Metrics Metrics `json:"metrics"`

	// Extras carries engine-specific per-node metadata that doesn't
	// fit the common shape. Keys are lowerCamelCase strings; values
	// are JSON-marshalable (string / number / bool / array / object).
	// Populated by the engine's normalizer when the source plan
	// contains the field. (PR #6.5 H6, M2, M15.)
	//
	// Stable keys (Postgres):
	//   - "sortKey":         []string  — Sort Key
	//   - "groupKey":        []string  — Group Key
	//   - "hashKeys":        []string  — Hash Keys
	//   - "output":          []string  — Output expressions
	//   - "subplanName":     string    — Subplan Name
	//   - "parentRelation":  string    — Parent Relationship
	//   - "workersPlanned": int        — Workers Planned
	//   - "workersLaunched": int       — Workers Launched (ANALYZE)
	//   - "parallelAware":   bool      — Parallel Aware
	//   - "jit":             object    — JIT info (top-level only)
	//   - "triggers":        array     — Triggers (top-level only)
	//   - "settings":        object    — Settings
	//   - "filter":          string    — full Filter
	//   - "indexCond":       string    — full Index Cond
	//   - "hashCond":        string    — full Hash Cond
	//   - "mergeCond":       string    — full Merge Cond
	//   - "recheckCond":     string    — full Recheck Cond
	//
	// Stable keys (MariaDB):
	//   - "accessType":  string  — raw access_type code
	//   - "rowsExaminedPerScan": int  — original RowsExpected basis
	//   - "rowsProducedPerJoin": int  — when present
	//   - "windowing":   object  — windowing block
	//   - "warningCodes": []int  — codes from engine warnings
	//
	// Frontends MUST tolerate unknown keys and missing well-known ones.
	Extras map[string]any `json:"extras,omitempty"`
}

// Metrics captures cost + timing + buffers for one node. All values are
// engine-normalized; missing values stay zero.
//
// Additive-stability (PR #6.5 N1): new fields may be appended.
type Metrics struct {
	// CostStart is the cumulative cost before the first row is
	// produced. Postgres: "Startup Cost". MariaDB: always 0 — the
	// JSON shape doesn't carry an equivalent and the field is
	// actively zeroed on the MariaDB path (PR #6.5 N2) so a future
	// MariaDB release that adds one won't leak junk before the
	// parser is updated.
	CostStart float64 `json:"costStart"`

	// CostTotal is the cumulative cost through the last row.
	// Postgres: "Total Cost". MariaDB: "cost_info.query_cost" at the
	// root + sub-node estimates.
	CostTotal float64 `json:"costTotal"`

	// RowsExpected is the planner's estimate of row count for this
	// node. Postgres: "Plan Rows". MariaDB: "rows_examined_per_scan"
	// or "rows_produced_per_join", whichever the node exposes.
	//
	// For MariaDB Nested Loop nodes (PR #6.5 H5): RowsExpected is
	// the multiplicative product of children (outer × inner …),
	// matching how the engine actually executes the join.
	RowsExpected int64 `json:"rowsExpected"`

	// RowsActual is the observed row count after execution. Only
	// populated by EXPLAIN ANALYZE. Postgres: "Actual Rows" × Loops.
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

	// BuffersDirtied is shared buffers dirtied (typically by
	// HOT updates / hint bit writes during a read). Postgres only.
	// (PR #6.5 L2.)
	BuffersDirtied int64 `json:"buffersDirtied"`

	// BuffersWritten is shared-buffer writes (rare except for
	// CREATE INDEX, etc.). Postgres only.
	BuffersWritten int64 `json:"buffersWritten"`
}

// normalizer parses engine-specific JSON output into a Plan tree.
// Implementations are stateless. Unexported (PR #6.5 M11): the package
// does not invite third-party normalizers — operators paste raw JSON
// into Plan.Raw for engine-native tools when the normalized shape
// misses something.
type normalizer interface {
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

	// OmitRaw, when true, drops the verbatim engine JSON from the
	// returned Plan to keep the response body small. Default false
	// (Raw is included). (PR #6.5 M9.)
	OmitRaw bool

	// PGOptions, when non-zero, replaces this package's default
	// Postgres EXPLAIN options. Default options are BUFFERS (+ ANALYZE
	// when opts.Analyze) + FORMAT JSON. ANALYZE and FORMAT JSON are
	// always added regardless of this struct; the remaining flags can
	// be toggled here. (PR #6.5 M14.)
	//
	// Ignored on non-Postgres engines.
	PGOptions PostgresExplainOptions
}

// PostgresExplainOptions toggles per-call Postgres EXPLAIN flags.
// Defaults (zero-value): BUFFERS=true, everything else false. ANALYZE is
// controlled by ExplainOpts.Analyze (and the structural ClassRead gate),
// FORMAT JSON is always set. (PR #6.5 M14.)
type PostgresExplainOptions struct {
	// DisableBuffers, when true, omits the default BUFFERS option.
	// Most operators want BUFFERS; this is here for completeness.
	DisableBuffers bool

	// Verbose adds VERBOSE. Surfaces extra Output / Schema columns.
	Verbose bool

	// Settings adds SETTINGS (Postgres 12+). Surfaces non-default
	// planner-relevant GUC settings on Plan.Raw + Plan.Extras.
	Settings bool

	// WAL adds WAL (Postgres 13+, only meaningful with ANALYZE).
	// Surfaces WAL records / bytes produced by the run.
	WAL bool
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
	// Belt-and-braces multi-statement / leading-comment guard
	// (PR #6.5 M1). The classifier upstream already enforces single-
	// statement-ness; this catches a missed upstream check before we
	// prepend the EXPLAIN keyword and send the payload to the engine.
	if err := validateSQLForExplain(opts.SQL); err != nil {
		return nil, err
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

	var p *Plan
	var err error
	switch engine {
	case dbadmin.EngineMariaDB:
		p, err = mysqlExplain(ctx, conn, opts, limits)
	case dbadmin.EnginePostgres:
		p, err = postgresExplain(ctx, conn, opts, limits)
	case dbadmin.EngineMongo:
		// MongoDB does have an .explain() surface but it is BSON-
		// command shaped, not SQL — and the classifier already
		// refuses raw SQL on Mongo connections, so this branch is
		// unreachable via the HTTP path. Return a clean refusal for
		// any direct caller. v0.3.2-F.
		return nil, fmt.Errorf("explain: EXPLAIN is not supported on MongoDB connections (raw SQL is refused upstream)")
	default:
		return nil, fmt.Errorf("explain: unsupported engine %v", engine)
	}
	if err != nil {
		return nil, err
	}
	if opts.OmitRaw {
		p.Raw = nil
	}
	return p, nil
}

// ExplainWithConfig is the host-friendly wrapper that derives the
// EXPLAIN-wrapping limits from a dbadmin.Config rather than requiring
// callers to plumb the timeout manually (PR #6.5 M7).
//
// Specifically, when opts.Limits.Timeout is zero, it is set to
// cfg.Query.TimeoutMax (capped to 5 min by the config's own validate);
// other limits zero values are filled from defaults inside Explain.
func ExplainWithConfig(ctx context.Context, conn driver.Conn, engine dbadmin.EngineKind, cfg dbadmin.Config, opts ExplainOpts) (*Plan, error) {
	if opts.Limits.Timeout == 0 && cfg.Query.TimeoutMax > 0 {
		opts.Limits.Timeout = cfg.Query.TimeoutMax
	}
	return Explain(ctx, conn, engine, opts)
}

// defaultExplainTimeout is the fallback used by Explain when neither
// opts.Limits.Timeout nor cfg.Query.TimeoutMax is set. EXPLAIN ANALYZE
// on big queries genuinely takes that long; the driver layer's own
// 30s default would mask slow plans. Prefer ExplainWithConfig in
// production (PR #6.5 M7).
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

// validateSQLForExplain catches obviously-multi-statement payloads
// before we prepend the EXPLAIN keyword (PR #6.5 M1). This is a
// belt-and-braces check; the upstream classifier already enforces
// single-statement-ness via a real lexer. Here we lex-lite:
//
//   - Reject leading line comments containing a semicolon (an attacker
//     can hide a statement after a "--" line comment that comments
//     out the prepended EXPLAIN keyword).
//   - Strip leading whitespace + benign comments.
//   - Reject empty remainder.
//   - Walk the remainder noting quote/identifier state and bare
//     semicolons; one trailing semicolon (and trailing whitespace +
//     comments) is allowed.
//
// On a multi-statement payload, we return a syntax-style error so the
// HTTP layer surfaces it as a 400.
func validateSQLForExplain(sql string) error {
	s := sql
	// Strip leading whitespace + comments (line + block). The classifier
	// has already done a more thorough job; this defends against the
	// path where Explain is called without it.
	for {
		s = strings.TrimLeftFunc(s, unicode.IsSpace)
		if len(s) == 0 {
			return errors.New("explain: SQL is empty after stripping comments")
		}
		if strings.HasPrefix(s, "--") {
			nl := strings.IndexByte(s, '\n')
			line := s
			if nl >= 0 {
				line = s[:nl]
			}
			// A leading line comment that contains a semicolon is
			// suspicious: prepending EXPLAIN puts the comment AFTER
			// EXPLAIN, which makes the engine interpret EXPLAIN + the
			// post-newline content as a separate statement.
			if strings.ContainsRune(line, ';') {
				return errors.New("explain: multi-statement SQL refused (leading line comment contains ';')")
			}
			if nl >= 0 {
				s = s[nl+1:]
				continue
			}
			return errors.New("explain: SQL is only comments")
		}
		if strings.HasPrefix(s, "/*") {
			if idx := strings.Index(s[2:], "*/"); idx >= 0 {
				s = s[2+idx+2:]
				continue
			}
			return errors.New("explain: SQL has unterminated block comment")
		}
		break
	}
	// Walk the body looking for an embedded semicolon outside of
	// quotes. A trailing semicolon at the very end (with optional
	// trailing whitespace + comments) is permitted.
	in := byte(0) // current quote state: 0 = none, '\'', '"', '`'
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch in {
		case 0:
			switch c {
			case '\'', '"', '`':
				in = c
			case '-':
				if i+1 < len(s) && s[i+1] == '-' {
					// line comment to EOL
					nl := strings.IndexByte(s[i:], '\n')
					if nl < 0 {
						return nil
					}
					i += nl
				}
			case '/':
				if i+1 < len(s) && s[i+1] == '*' {
					end := strings.Index(s[i+2:], "*/")
					if end < 0 {
						return errors.New("explain: SQL has unterminated block comment")
					}
					i += 2 + end + 1
				}
			case ';':
				// Permitted only at end-of-statement (trailing
				// whitespace + comments after).
				rest := strings.TrimLeftFunc(s[i+1:], unicode.IsSpace)
				rest = stripComments(rest)
				if rest != "" {
					return errors.New("explain: multi-statement SQL refused (extra content after ';')")
				}
				return nil
			}
		default:
			if c == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if c == in {
				in = 0
			}
		}
	}
	if in != 0 {
		return fmt.Errorf("explain: SQL has unterminated %c-quote", in)
	}
	return nil
}

// stripComments removes leading + trailing whitespace and any leading
// line/block comments. Used to test the "post-semicolon residue" tail.
func stripComments(s string) string {
	for {
		s = strings.TrimSpace(s)
		if strings.HasPrefix(s, "--") {
			if idx := strings.IndexByte(s, '\n'); idx >= 0 {
				s = s[idx+1:]
				continue
			}
			return ""
		}
		if strings.HasPrefix(s, "/*") {
			if idx := strings.Index(s[2:], "*/"); idx >= 0 {
				s = s[2+idx+2:]
				continue
			}
			return s
		}
		return s
	}
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
//
// Also asserts the iterator is exhausted (PR #6.5 L8): a malformed
// driver that returns a second row for EXPLAIN ... FORMAT JSON is a
// bug that we refuse to silently consume.
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

	// Assert exhaustion. A second row from EXPLAIN ... FORMAT JSON
	// would indicate a buggy or hostile driver; refuse silently
	// consuming it. ErrCapped is acceptable here — the MaxRows=1
	// cap is exactly the signal we want.
	if _, nerr := rs.Next(ctx); nerr == nil {
		return nil, errors.New("explain: engine returned >1 row for EXPLAIN ... FORMAT JSON")
	} else if !errors.Is(nerr, driver.ErrEOF) && !errors.Is(nerr, driver.ErrCapped) {
		return nil, fmt.Errorf("explain: unexpected error draining EXPLAIN rows: %w", nerr)
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
//
// Additionally recognizes MariaDB's K/M/G/T suffixed sizes (PR #6.5
// M4): "1K" → 1024, "10M" → 10485760, "1.5G" → 1610612736. The
// suffix is base-1024 (matches MariaDB's data_read_per_join), and we
// also accept "Ki"/"Mi" forms for forward-compat with engines that
// emit them.
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
		f, ok := parseFloatWithSuffix(x)
		if !ok {
			return 0
		}
		return sanitizeFloat(f)
	}
	return 0
}

// parseFloatWithSuffix parses a float that may carry a K/M/G/T/P
// suffix (base 1024). Returns ok=false on parse failure.
func parseFloatWithSuffix(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	// Strip optional trailing "i" (e.g. "Ki", "Mi") so we treat
	// "1K" and "1Ki" identically.
	unitEnd := len(s)
	mult := 1.0
	if unitEnd >= 2 && (s[unitEnd-1] == 'i' || s[unitEnd-1] == 'I') {
		unitEnd--
	}
	if unitEnd >= 1 {
		switch s[unitEnd-1] {
		case 'K', 'k':
			mult = 1024
			unitEnd--
		case 'M', 'm':
			mult = 1024 * 1024
			unitEnd--
		case 'G', 'g':
			mult = 1024 * 1024 * 1024
			unitEnd--
		case 'T', 't':
			mult = 1024 * 1024 * 1024 * 1024
			unitEnd--
		case 'P', 'p':
			mult = 1024 * 1024 * 1024 * 1024 * 1024
			unitEnd--
		}
	}
	if unitEnd <= 0 {
		// String was only a unit; treat as parse failure.
		if mult == 1 {
			return 0, false
		}
		return 0, false
	}
	num := strings.TrimSpace(s[:unitEnd])
	if num == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, false
	}
	return f * mult, true
}

// asInt64 coerces a JSON number into int64.
//
// PR #6.5 L1: float→int conversion now rounds half-away-from-zero
// instead of truncating. 1.9 → 2 (not 1), -1.5 → -2 (not -1). Row
// counts emitted as floats by Postgres are typically integers in
// disguise; rounding produces a less surprising result when they
// aren't.
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
		return int64(math.Round(x))
	case float32:
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return 0
		}
		return int64(math.Round(f))
	case string:
		// Try integer first; on failure, try float so we still pick
		// up "1.0" / "2.5" cleanly.
		if n, err := strconv.ParseInt(x, 10, 64); err == nil {
			return n
		}
		if f, ok := parseFloatWithSuffix(x); ok {
			f = sanitizeFloat(f)
			return int64(math.Round(f))
		}
		return 0
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

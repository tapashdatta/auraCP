// Package explain runs EXPLAIN against MariaDB / MySQL and PostgreSQL,
// then normalizes the engine-specific JSON output into a single Plan
// tree the frontend can render.
//
// The render target is a flame-tree (see ADR-001 §"One-of-a-kind angle"):
// horizontal bars sized by cost, color-coded by node kind, with the
// hottest path highlighted. The frontend cares about a small set of
// structured fields; it does NOT want to dispatch on engine-specific
// shapes. This package is the boundary.
//
// What this package does:
//
//   - Wraps the operator's SQL in `EXPLAIN FORMAT=JSON ...` (MariaDB)
//     or `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) ...` (Postgres),
//     using the driver layer to execute.
//   - Parses the engine-specific JSON into a Plan tree with Node +
//     Metrics types that are identical across engines.
//   - Preserves the verbatim engine output in Plan.Raw so the
//     operator can inspect it if the normalized shape misses
//     something interesting.
//
// What this package does NOT do:
//
//   - Render. That's a frontend concern; the API just ships JSON.
//   - Validate identifiers in the user's SQL. The classifier (PR #2)
//     already classified the SQL as ClassRead before this package is
//     reached; we accept whatever it accepted.
//   - Analyze write queries by default. EXPLAIN ANALYZE actually
//     EXECUTES the query — for write statements that's destructive.
//     The package enforces a STRUCTURAL gate: Explain() refuses any
//     call with ExplainOpts.Analyze=true unless ExplainOpts.Class is
//     classifier.ClassRead. The default zero value of Class is
//     ClassRead, so callers that haven't run the classifier upstream
//     are safe as long as they leave Analyze=false. The gate prevents
//     a missed upstream check from silently mutating data.
//
// Security:
//
//   - The user SQL is wrapped, not parsed. We rely on the classifier's
//     prior decision that it's ClassRead (or write, with Analyze=false).
//     PR #6.5 (M1) additionally rejects obvious multi-statement payloads
//     at the wrap site as a belt-and-braces defense — see
//     validateSQLForExplain.
//   - Postgres EXPLAIN ANALYZE on a SELECT inside a function-with-side-
//     effects can still mutate state — this is true of any DB and
//     documented as out of scope.
//   - JSON parsing uses encoding/json with explicit struct shapes;
//     unknown fields are tolerated for forward-compat with future
//     server versions but never cause silent loss of normalized
//     fields.
//
// Engine-parity field availability matrix (PR #6.5 H11):
//
// The Plan/Node/Metrics struct shape is engine-agnostic but not every
// field is populated by every engine. The frontend should treat zero
// values as "not measured" except where called out below.
//
//	Field                        Postgres            MariaDB
//	─────────────────────────── ─────────────────── ───────────────────
//	Plan.Engine                 "postgres"          "mariadb"
//	Plan.Root                   yes                 yes
//	Plan.Total                  mirrors Root        rolled-up tree (H9)
//	Plan.PlanningTimeMS         yes (ANALYZE only)  always 0 (M3 note)
//	Plan.ExecutionTimeMS        yes (ANALYZE only)  always 0
//	Plan.Warnings               yes                 yes (engine+parser)
//	Plan.Raw                    JSON array          JSON object (M12)
//	Node.Kind                   "Seq Scan", etc.    "Full Table Scan", etc.
//	Node.Relation               yes                 yes
//	Node.Schema                 yes                 ""  (inferred)
//	Node.Alias                  yes                 ""  (rare)
//	Node.Index                  yes                 yes
//	Node.JoinType               yes                 "Nested Loop" etc.
//	Node.Filter                 first non-empty of  attached_condition
//	                            5 fields; full set
//	                            available in Extras
//	Node.Extras                 PG-specific keys    MariaDB-specific keys
//	                            (Sort Key, Group    (warnings codes, etc.)
//	                            Key, Hash Keys,
//	                            Workers, JIT,
//	                            Triggers, Settings,
//	                            Output, etc.)
//	Metrics.CostStart           "Startup Cost"      always 0 (N2)
//	Metrics.CostTotal           "Total Cost"        prefix_cost / query_cost
//	Metrics.RowsExpected        "Plan Rows"         rows_examined_per_scan
//	                                                (join nodes:
//	                                                multiplicative — H5)
//	Metrics.RowsActual          "Actual Rows" ×Loops always 0
//	Metrics.TimeStartMS         yes (ANALYZE)       always 0
//	Metrics.TimeTotalMS         yes (ANALYZE)       always 0
//	Metrics.Loops               "Actual Loops"      always 0
//	Metrics.BuffersHit          "Shared Hit Blocks" always 0
//	Metrics.BuffersRead         "Shared Read"       always 0
//	Metrics.BuffersDirtied      "Shared Dirtied"    always 0
//	Metrics.BuffersWritten      "Shared Written"    always 0
//
// Plan.Raw shape (M12):
//
//   - Postgres: a JSON ARRAY of one element (`[ { "Plan": {...}, ... } ]`).
//   - MariaDB:  a JSON OBJECT (`{ "query_block": {...}, "warnings": [...] }`).
//
// The frontend's "raw" tab should not try to pre-parse Plan.Raw without
// branching on Plan.Engine.
//
// Forward-compat (N1):
//
// Plan, Node, and Metrics follow an additive-stability contract: new
// fields may be appended in any release, but existing fields are never
// removed or repurposed in a non-backwards-compatible way. The JSON
// shape uses lowerCamelCase keys; new keys are added in the same casing.
// Frontends should ignore unknown fields.
//
// Known gaps (tracked in docs/aura-db/KNOWN-ISSUES.md):
//
//   - Postgres NOTICE messages are not surfaced on Plan.Warnings; pgx
//     exposes them via a separate channel that this package does not
//     subscribe to. Engine-level warnings ARE surfaced via the
//     EXPLAIN output's own warnings array (MariaDB) — there is no
//     Postgres equivalent in the plan JSON.
package explain

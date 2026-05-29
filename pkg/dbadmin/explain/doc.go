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
//   - Postgres EXPLAIN ANALYZE on a SELECT inside a function-with-side-
//     effects can still mutate state — this is true of any DB and
//     documented as out of scope.
//   - JSON parsing uses encoding/json with explicit struct shapes;
//     unknown fields are tolerated for forward-compat with future
//     server versions but never cause silent loss of normalized
//     fields.
package explain

// Package rows provides safe, parameterized row operations for Aura DB:
// paginated reads with filter + sort, primary-key-anchored row updates
// and deletes, and inserts with optional column-set RETURNING.
//
// Why this exists separately from the driver and the classifier:
//
//   - The grid editor in the panel UI must mutate rows without exposing
//     raw SQL to the operator. Those mutations need to be safe by
//     construction. No operator string ever reaches the database as
//     part of the SQL. Operator-supplied integers (LIMIT, OFFSET) are
//     validated (Limit must be in [0, Operator.MaxRows], Offset must be
//     >= 0) and then formatted directly into the SQL text — this is
//     safe because Go's int type cannot encode SQL injection, but is
//     worth noting for auditors. The empty-IN-list sentinels (1=0 /
//     1=1) are static SQL fragments, not operator-derived.
//   - The classifier protects the SQL editor's freeform path. This
//     package protects the row-grid + cell-editor path. Both paths
//     run through the same engine but use entirely different shapes.
//   - Identifier handling is centralized here: every schema, table,
//     and column name is validated and engine-aware-quoted before
//     being concatenated into SQL. Values pass as bind parameters.
//
// Security posture:
//
//   - SCHEMA + TABLE + COLUMN names: validated via
//     schema.ValidateIdentifier (the package import), then quoted via
//     the engine-specific quoteIdent helper. Operators can NOT supply
//     an identifier that contains a quote character; the validator
//     rejects it.
//   - VALUES: passed as bind parameters through driver.Conn.Exec /
//     driver.Conn.Query. Never concatenated.
//   - PREDICATE OPERATORS: drawn from a fixed enum (see Op constants).
//     Operator strings like "; DROP TABLE x" do not match any constant
//     and are rejected at build time.
//   - PK ENFORCEMENT: Update / Delete refuse to run against a table
//     with no primary key. This eliminates the "I meant to update one
//     row but the WHERE was empty" class. Operators who actually need
//     mass DELETE / UPDATE use the SQL editor, where the classifier
//     flags it as write-row-mass + requires step-up.
//   - READ ROW CAP: Read accepts a Limit; the package refuses Limit
//     beyond Config.Query.ResultRowsMax (passed via Operator
//     construction). Hardcoded fallback: 10,000.
package rows

// Package schema reads database metadata — databases, schemas, tables,
// columns, indexes, foreign keys, triggers, views, functions — from a
// driver.Conn and returns a normalized model that's identical across
// engines.
//
// Why this exists separately from the driver:
//
//   - Schema metadata is read-only and cacheable; it shouldn't share the
//     driver's strict per-query resource limits.
//   - The query patterns are engine-specific (information_schema vs
//     pg_catalog), but the SHAPE of the answer must be the same so
//     downstream code (the HTTP handler, the frontend grid) only knows
//     one model.
//   - Operator input (schema name, table name) flows into these queries;
//     this package owns the validation that keeps them from becoming an
//     injection surface.
//
// Public surface:
//
//   - Reader interface: ListDatabases, ListSchemas, ListTables,
//     GetTable, ListViews, ListFunctions, ListProcedures, ListTriggers.
//   - Normalized types: TableSummary, Table, Column, Index, ForeignKey,
//     ViewSummary, FunctionSummary, ProcedureSummary, TriggerSummary,
//     TableKind. Databases and schemas are returned as []string.
//   - For(driver.Conn) → Reader: picks the right engine implementation.
//   - Cache: TTL-based wrapping reader with an invalidation hook the
//     engine calls after DDL.
//
// Security:
//
//   - All operator-supplied identifiers (schema, table, function names)
//     are validated against [a-zA-Z_][a-zA-Z0-9_$]{0,62} (max 63 chars
//     total, matching Postgres NAMEDATALEN) at the package entry.
//     Anything else returns ErrInvalidIdentifier.
//   - We never concatenate identifiers into SQL. All filter values pass
//     as parameterized arguments to information_schema / pg_catalog
//     queries.
//   - The reader holds no credentials and opens no connections itself.
//     Conn ownership stays with the pool.
package schema

// Package tableimport streams CSV / NDJSON payloads off an io.Reader as
// a sequence of {column → value} maps for the dbadmin import endpoint.
//
// The package is the symmetric counterpart of pkg/dbadmin/export: where
// export.Encoder pushes one row at a time onto a writer, tableimport.Decoder
// pulls one row at a time off a reader. The decoder is single-pass +
// streaming — it never buffers the full file in memory — so multi-MB
// payloads flow through with a single bufio.Reader's worth of working
// memory.
//
// Naming: the directory is `tableimport` rather than `import` because
// `import` is a Go reserved word and would block any caller from doing
// `import "github.com/auracp/auracp/pkg/dbadmin/import"`.
//
// # Format scope (v0.3.2-E)
//
// CSV (RFC 4180 + small extensions):
//   - Comma separator, CR-LF / LF line endings.
//   - First row is the header (column names). The header arity defines
//     the row arity for every subsequent row; mismatched rows return an
//     error rather than silently padding.
//   - Empty cell decodes to nil (NULL semantics on the rows.Insert side).
//   - Leading apostrophe is stripped when the cell would otherwise begin
//     with a formula-trigger character (=, +, -, @, \t, \r). This is the
//     inverse of export/csv.go's SEC-1 formula sanitiser — round-trip
//     identity for CSV cells written by the export package is preserved.
//
// NDJSON:
//   - One JSON object per line. Empty lines are skipped.
//   - Object keys define the column set for that row; the decoder does
//     NOT require every row to have identical key sets (sparse imports
//     are allowed; missing keys decode as nil on the wire).
//   - JSON numbers decode to either int64 (when the literal has no
//     decimal point and fits) or float64. The downstream rows.Insert
//     binds the value as a driver parameter; the driver coerces to the
//     column type.
//   - JSON null decodes to nil. true/false decode to bool. Arrays /
//     nested objects pass through as []any / map[string]any — the rows
//     package rejects these unless the driver accepts them as JSONB.
//
// SQL imports are NOT supported. The export endpoint emits a replay-able
// SQL dump; the inverse is "run this SQL through the SQL editor" rather
// than the import endpoint, which would otherwise become a back-door for
// arbitrary statement execution (the import endpoint authorises with
// ActionImport / RoleWriter, the SQL editor with ActionQueryWrite +
// per-statement classifier — these MUST NOT be conflated).
//
// # Hard caps
//
// The package itself does not enforce row caps, byte caps, or per-user
// concurrency — those concerns belong to the HTTP layer. The decoder
// does enforce a per-row cell-byte ceiling (maxCellBytes) so a single
// malformed CSV cell cannot trigger an O(file-size) allocation.
package tableimport

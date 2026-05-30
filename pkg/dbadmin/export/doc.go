// Package export streams query results to a writer in one of several
// serialization formats (CSV, NDJSON, engine-aware SQL INSERT).
//
// The package is the format-conversion engine behind the HTTP export
// endpoint (POST /connections/{id}/export). It exposes a tiny Encoder
// interface — WriteHeader / WriteRow / Close — and three implementations
// keyed by the Format constants. All encoders write incrementally to an
// io.Writer; the HTTP handler wraps the response writer + an
// http.Flusher and flushes between row batches so the browser sees
// chunked output and starts the file download as soon as headers arrive.
//
// Type mapping mirrors the driver layer (see driver/driver.go Rows
// docstring): the encoders accept the []any rows driver.Rows.Next
// returns, and render them per format. NULL is rendered as empty cell
// (CSV), null (NDJSON), or the SQL keyword NULL (SQL). Time.Time is
// always rendered in UTC RFC 3339 form. []byte is base64-encoded for
// CSV / NDJSON and rendered as engine-specific hex / bytea literal for
// SQL.
//
// The package does NOT enforce row caps, byte caps, or per-user
// concurrency: those concerns belong to the HTTP layer (see
// pkg/dbadmin/httpapi/export_limits.go).
//
// No external dependencies — only encoding/csv, encoding/json,
// encoding/base64, encoding/hex, strconv, strings.
//
// # Empty-result contracts (C12, PR #16.5)
//
// All three encoders return a well-defined empty-result output:
//
//   - CSV with IncludeHeader=true emits exactly one CRLF-terminated
//     header row (no data rows). With IncludeHeader=false the output
//     is the zero-byte stream.
//   - NDJSON emits a zero-byte stream — there is no header line and no
//     data lines. Consumers MUST tolerate an empty input (`jq -s '.'`
//     yields `[]`).
//   - SQL emits the header comment block + (engine-specific) pragma
//     followed by the trailing `-- end: 0 rows` marker. No INSERT
//     statements are emitted. The dump is replay-safe (a SCHEMA-only
//     refresh).
//
// # Float convention (C6, PR #16.5)
//
//   - NaN  → CSV empty cell, NDJSON null, SQL NULL.
//   - +Inf → CSV "Infinity",  NDJSON null, SQL NULL.
//   - -Inf → CSV "-Infinity", NDJSON null, SQL NULL.
//
// CSV uses the spelled-out "Infinity" / "-Infinity" so spreadsheet
// applications display a meaningful string rather than a missing cell.
// NDJSON + SQL coerce to null because the wire formats do not have
// non-finite-float literals.
//
// # Formats not (yet) supported (ux-12, PR #16.5)
//
// The BUILD-PLAN §PR #16 acceptance list mentioned a Markdown encoder
// as a stretch goal. Markdown is intentionally not shipped — the
// table-data export use case is dump-and-replay (NDJSON / SQL) or
// spreadsheet import (CSV); Markdown is a presentation format that
// already exists in the query-result viewer. The BUILD-PLAN reference
// has been struck to reflect the shipped scope.
//
// # Direct-streaming over signed-URL handoff (ux-13, PR #16.5)
//
// The SDK §7.3 sketch proposed a signed-URL handoff (POST → 202 +
// presigned GET) for export downloads. The shipped design instead
// streams the body directly from the export handler. Reasons:
//
//   - No object-store dependency in v0.3.0 (signed-URL handoff would
//     require S3 / GCS or a local presigned-path service).
//   - One round-trip vs two — the browser downloads as the rows are
//     produced, no spool-to-disk-then-pick-up step.
//   - Cancellation works out of the box: the AbortController on the
//     fetch tears down the streaming query immediately.
//
// Trade-off: the export holds the export-handler goroutine + per-user
// concurrency slot for the full transfer window. The 1 hour timeout
// and 1 GiB / 1M-row caps in pkg/dbadmin/httpapi/export_limits.go
// bound the worst-case dwell time. A signed-URL path is still on the
// roadmap for very-large-export workflows once the object-store
// dependency is on the menu.
package export

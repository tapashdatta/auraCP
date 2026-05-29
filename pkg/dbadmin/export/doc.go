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
package export

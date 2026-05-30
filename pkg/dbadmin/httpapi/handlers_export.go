package httpapi

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/driver"
	"github.com/auracp/auracp/pkg/dbadmin/export"
	"github.com/auracp/auracp/pkg/dbadmin/rows"
	"github.com/auracp/auracp/pkg/dbadmin/schema"
)

// rfc5987Encode percent-encodes a string for the filename* parameter
// of an RFC 5987 Content-Disposition header. Reserved + non-attr-char
// bytes are %HH-encoded; the ASCII attr-chars (token / mime-charset
// alphanumerics + a small punctuation set) pass through unchanged.
//
// SEC-3 / ux-4 (PR #16.5): the previous implementation interpolated
// the raw filename into filename*=UTF-8''<NAME> which violates the RFC
// when the filename contains percent / space / unicode bytes — strict
// parsers reject the header. SanitizeFilename strips the most dangerous
// characters but does not encode them.
func rfc5987Encode(s string) string {
	// Allowed attr-char set per RFC 5987 §3.2.1.
	const allow = "ABCDEFGHIJKLMNOPQRSTUVWXYZ" +
		"abcdefghijklmnopqrstuvwxyz" +
		"0123456789" +
		"!#$&+-.^_`|~"
	var b strings.Builder
	b.Grow(len(s))
	const hex = "0123456789ABCDEF"
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte(allow, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0F])
	}
	return b.String()
}

// asciiFilename returns s with non-ASCII bytes replaced by '_' for use
// in the plain `filename=` parameter. RFC 6266 advises shipping both
// a plain ASCII filename and an RFC 5987 filename* — old user-agents
// honour the plain form, modern ones honour filename* with the
// non-ASCII characters intact.
func asciiFilename(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == 0x7F || c >= 0x80 || c == '"' || c == '\\' {
			b.WriteByte('_')
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// exportLocks is the per-server in-flight export tracker. Lazy-init via
// sync.Once would be slightly cleaner; we pre-initialize a package-level
// instance because the server struct doesn't expose hooks. Concurrency
// is bounded to 1 export per user.
var exportLocks = newExportLockManager()

// exportTimeoutHard is the absolute deadline for one export request.
// Independent of the route's perRouteTimeout (export uses its own
// chain with no perRouteTimeout, see router.go).
const exportTimeoutHard = 1 * time.Hour

// handleExport streams a table export to the response writer. The
// endpoint never accepts raw SQL — the body is a structured
// {schema, table, columns?, filter?, sort?, format, limit?, ...} that
// the rows package builds into a parameterized SELECT.
//
// Lifecycle:
//  1. authorize() with ActionExport (RoleAnalyst, no step-up).
//  2. acquire per-user export slot (409 on contention).
//  3. decode + validate body; reject invalid identifiers + format.
//  4. emit START audit event.
//  5. open driver conn, run the streaming Query.
//  6. pump rows through the format encoder, flushing after every batch.
//  7. on completion / failure, emit OUTCOME audit event with row+byte
//     counts and elapsed ms.
//
// The handler suppresses the audit middleware's auto-emit (explicit
// audit records are emitted at start + end for richer outcome data).
//
// SEC-2 (PR #16): a deferred emitter installed at handler entry guarantees
// that EVERY early-return path (authz denial, validation, lock contention,
// driver open failure, ...) leaves an audit trail. The handler clears the
// deferred flag only after the START audit event has fired, at which
// point the explicit finish-emit path (emitExportFinish) takes over.
func handleExport(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		setAuditAction(r.Context(), dbadmin.ActionExport, dbadmin.Target{ConnectionID: connID})
		suppressAudit(r.Context()) // explicit emit below; avoid double-record.

		// C5 (PR #16.5): mint a per-export correlation ID at handler
		// entry. Used by BOTH the deferred denial emitter and the
		// explicit start/finish emitters so operators can pair denied
		// + start + finish records by a single token (the previous
		// design left the denial path with a fresh requestID and the
		// start/finish pair sharing a different requestID).
		jobID := newRequestID()
		w.Header().Set("X-Aura-Export-JobID", jobID)

		// SEC-2: deferred audit emitter. Fires a synthetic "export-denied"
		// outcome event if the START event was never recorded — i.e. when
		// the handler short-circuits during validation / authz / lock.
		// Captures the latest in-progress target (schema/table) when set
		// later in the flow. The deferred call uses context.Background()
		// so request-context cancellation cannot suppress the record.
		var (
			startEmitted bool
			denyTarget   = dbadmin.Target{ConnectionID: connID}
			denyErr      error
			denyStatus   string
		)
		denyStatus = "denied"
		defer func() {
			if startEmitted {
				return
			}
			if s == nil || s.engine == nil || s.engine.Audit() == nil {
				return
			}
			errStr := ""
			if denyErr != nil {
				_, code, _ := mapErr(denyErr)
				errStr = code
			}
			usr, _ := userFrom(r.Context())
			s.recordAudit(context.Background(), dbadmin.Event{
				EventID:        newRequestID(),
				Timestamp:      time.Now().UTC(),
				UserID:         usr.ID,
				UserRoleAtTime: usr.Roles[connID],
				SourceIP:       clientIP(r),
				UserAgentHash:  uaHash(r),
				Action:         dbadmin.ActionExport,
				Target:         denyTarget,
				Statement:      "export-" + denyStatus + ":" + jobID,
				Error:          errStr,
				ParametersRedacted: map[string]any{
					"phase": denyStatus,
					"jobId": jobID, // C5: correlation ID on the denial path.
				},
			})
		}()

		user, _ := userFrom(r.Context())
		if err := authorize(s, r.Context(), user, connID, dbadmin.ActionExport); err != nil {
			denyErr = err
			denyStatus = "denied-authz"
			writeMappedErr(w, r, err)
			return
		}

		// Per-user concurrency cap. Non-blocking acquire.
		// C11 / SEC-7 (PR #16.5): empty userID indicates an authn-wiring
		// gap upstream — the gate now returns an error rather than
		// silently letting the request through. We surface a 500 so the
		// failure is visible in monitoring instead of the previous "all
		// concurrent exports allowed" silent bypass.
		acquired, lockErr := exportLocks.tryAcquire(user.ID)
		if lockErr != nil {
			denyErr = lockErr
			denyStatus = "denied-empty-user"
			writeError(w, r, http.StatusInternalServerError, CodeInternal, "export: missing user identity")
			return
		}
		if !acquired {
			denyStatus = "denied-conflict"
			w.Header().Set("Retry-After", "5")
			writeError(w, r, http.StatusConflict, CodeConflict, "another export is already in progress for this user")
			return
		}
		defer exportLocks.release(user.ID)

		var in exportRequest
		if err := readJSON(w, r, &in, 1<<20); err != nil {
			denyErr = err
			denyStatus = "denied-badreq"
			writeMappedErr(w, r, err)
			return
		}
		if in.Schema == "" || in.Table == "" || in.Format == "" {
			denyStatus = "denied-badreq"
			writeError(w, r, http.StatusBadRequest, CodeInvalidInput, "schema, table, and format are required")
			return
		}
		// Update audit target with the requested schema/table even when
		// validation later rejects them — operators still want to see
		// what was attempted.
		denyTarget = dbadmin.Target{ConnectionID: connID, Schema: in.Schema, Object: in.Table}
		format := export.Format(strings.ToLower(in.Format))
		if !format.IsValid() {
			denyStatus = "denied-badreq"
			writeError(w, r, http.StatusBadRequest, CodeInvalidInput, "format must be csv, ndjson, or sql")
			return
		}
		if err := schema.ValidateIdentifier(in.Schema); err != nil {
			denyErr = err
			denyStatus = "denied-badident"
			writeMappedErr(w, r, err)
			return
		}
		if err := schema.ValidateIdentifier(in.Table); err != nil {
			denyErr = err
			denyStatus = "denied-badident"
			writeMappedErr(w, r, err)
			return
		}
		for _, c := range in.Columns {
			if err := schema.ValidateIdentifier(c); err != nil {
				denyErr = err
				denyStatus = "denied-badident"
				writeMappedErr(w, r, err)
				return
			}
		}

		// Translate + validate wire predicates / sort keys BEFORE
		// hitting the backend, so 400-class errors are surfaced
		// without a wasted driver dial.
		filter, err := exportPredicatesToRows(in.Filter)
		if err != nil {
			denyErr = err
			denyStatus = "denied-badpredicate"
			writeError(w, r, http.StatusBadRequest, CodeInvalidPredicate, err.Error())
			return
		}
		sortKeys := make([]rows.SortKey, len(in.Sort))
		for i, sk := range in.Sort {
			if err := schema.ValidateIdentifier(sk.Column); err != nil {
				denyErr = err
				denyStatus = "denied-badident"
				writeMappedErr(w, r, err)
				return
			}
			sortKeys[i] = rows.SortKey{Column: sk.Column, Descending: sk.Descending}
		}

		c, err := s.engine.Conns().Get(r.Context(), connID)
		if err != nil {
			denyErr = err
			denyStatus = "denied-conn-get"
			writeMappedErr(w, r, err)
			return
		}

		// Update audit target with full schema/table info.
		setAuditAction(r.Context(), dbadmin.ActionExport, dbadmin.Target{
			ConnectionID: connID, Schema: in.Schema, Object: in.Table,
		})

		// Resolve column list — if the caller did not pass one, fetch
		// the table's declared columns from the schema reader. We open
		// the backend conn once for both the schema lookup and the
		// streaming Query.
		conn, err := openConn(s, r.Context(), c)
		if err != nil {
			denyErr = err
			denyStatus = "denied-conn-open"
			writeMappedErr(w, r, err)
			return
		}
		defer conn.Close()
		rdr, err := schema.For(conn, c.Engine)
		if err != nil {
			denyErr = err
			denyStatus = "denied-schema-reader"
			writeMappedErr(w, r, err)
			return
		}

		cols := in.Columns
		if len(cols) == 0 {
			tbl, terr := rdr.GetTable(r.Context(), in.Schema, in.Table)
			if terr != nil {
				denyErr = terr
				denyStatus = "denied-schema-get"
				writeMappedErr(w, r, terr)
				return
			}
			cols = make([]string, 0, len(tbl.Columns))
			for _, tc := range tbl.Columns {
				cols = append(cols, tc.Name)
			}
		}
		if len(cols) == 0 {
			denyStatus = "denied-nocols"
			writeError(w, r, http.StatusUnprocessableEntity, CodeInvalidInput, "no columns to export")
			return
		}

		// Clamp limit. 0 / unset → cap.
		limit := in.Limit
		if limit <= 0 || limit > exportMaxRowsHardCap {
			limit = exportMaxRowsHardCap
		}

		sql, args, err := rows.BuildSelect(rows.BuildSelectOpts{
			Engine:  c.Engine,
			Schema:  in.Schema,
			Table:   in.Table,
			Columns: cols,
			Filter:  filter,
			Sort:    sortKeys,
			Limit:   limit,
			Offset:  0,
		})
		if err != nil {
			denyErr = err
			denyStatus = "denied-build"
			writeMappedErr(w, r, err)
			return
		}

		// Build encoder options.
		includeHeader := true
		if in.IncludeHeader != nil {
			includeHeader = *in.IncludeHeader
		}
		filename := export.SanitizeFilename(in.Filename)
		if filename == "export" {
			filename = fmt.Sprintf("%s-%s.%s",
				export.SanitizeFilename(in.Table),
				time.Now().UTC().Format("20060102T150405Z"),
				format.FileExt())
		} else if !strings.HasSuffix(strings.ToLower(filename), "."+format.FileExt()) {
			filename = filename + "." + format.FileExt()
		}

		// Streaming context. Independent timeout — the export route's
		// chain has no perRouteTimeout (router.go), so we install one
		// here scoped to exportTimeoutHard.
		streamCtx, cancel := context.WithTimeout(r.Context(), exportTimeoutHard)
		defer cancel()

		// Emit START audit event.
		// C5 (PR #16.5): jobID was minted at handler entry above so the
		// denial / start / finish records share a single correlation ID.
		startStmt := fmt.Sprintf("SELECT <%d cols> FROM %s.%s LIMIT %d",
			len(cols), in.Schema, in.Table, limit)
		s.recordAudit(streamCtx, dbadmin.Event{
			EventID:        jobID,
			Timestamp:      time.Now().UTC(),
			UserID:         user.ID,
			UserRoleAtTime: user.Roles[connID],
			SourceIP:       clientIP(r),
			UserAgentHash:  uaHash(r),
			Action:         dbadmin.ActionExport,
			Target:         dbadmin.Target{ConnectionID: connID, Schema: in.Schema, Object: in.Table},
			Statement:      startStmt,
		})
		// SEC-2: START fired — the deferred denial emitter must stand
		// down; the explicit finish-emit path (emitExportFinish) now
		// owns the OUTCOME record.
		startEmitted = true

		started := time.Now()

		// Run the streaming query. driver.Limits is generous — the
		// MaxRows guard is the request-level cap above; we also pass
		// MaxBytes to keep the row-byte counter active inside the
		// driver's LimitedRows.
		drvLimits := driver.Limits{
			Timeout:  exportTimeoutHard,
			MaxRows:  limit,
			MaxBytes: exportMaxBytesHardCap,
		}
		rs, err := conn.Query(streamCtx, drvLimits, sql, args...)
		if err != nil {
			emitExportFinish(s, streamCtx, user, connID, in, jobID, started, 0, 0, false, err)
			writeMappedErr(w, r, err)
			return
		}
		defer rs.Close()

		// Stream headers + open the encoder. From this point on errors
		// cannot use writeError (status already chosen). ux-3 surfaces
		// mid-stream errors via a Trailer header in addition to the
		// format-specific trailer row.
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", format.ContentType())
		// SEC-3 / ux-4 (PR #16.5): RFC 5987 percent-encoded filename*
		// (UTF-8 + lang-tag-elided) alongside an ASCII-safe filename
		// for legacy user agents per RFC 6266.
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`,
			asciiFilename(filename), rfc5987Encode(filename)))
		// C5: X-Aura-Export-JobID was set at handler entry; the row cap
		// is response-specific and stays here.
		w.Header().Set("X-Aura-Export-RowCap", fmt.Sprintf("%d", limit))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		// C2 / ux-3: declare the trailers we may set. Clients that read
		// trailers (fetch + response.headers in modern browsers expose
		// these as part of the response after the body is consumed) get
		// canonical truncation + error signals out of band of the body.
		//
		// ux-7 (PR #16.5): also surface the final row count via
		// X-Aura-Export-Rows so the UI can render "N rows" in the
		// status pill once the body finishes.
		w.Header().Set("Trailer", "X-Truncated, X-Export-Error, X-Aura-Export-Rows")
		w.WriteHeader(http.StatusOK)

		cw := newCountingWriter(w)
		encOpts := export.Options{
			IncludeHeader:  includeHeader,
			Engine:         c.Engine,
			SchemaName:     in.Schema,
			TableName:      in.Table,
			ConnectionName: c.Name,
		}
		enc, err := export.NewEncoder(cw, format, encOpts)
		if err != nil {
			emitExportFinish(s, streamCtx, user, connID, in, jobID, started, 0, 0, false, err)
			return
		}
		if err := enc.WriteHeader(cols); err != nil {
			emitExportFinish(s, streamCtx, user, connID, in, jobID, started, 0, cw.BytesWritten(), false, err)
			return
		}

		var (
			rowCount   int64
			truncated  bool
			finalErr   error
			lastFlush  = time.Now()
			flushEvery = 256
		)

	loop:
		for {
			select {
			case <-streamCtx.Done():
				truncated = true
				finalErr = streamCtx.Err()
				break loop
			default:
			}
			vals, err := rs.Next(streamCtx)
			if errors.Is(err, driver.ErrEOF) {
				break loop
			}
			if errors.Is(err, driver.ErrCapped) {
				truncated = true
				break loop
			}
			if err != nil {
				finalErr = err
				break loop
			}
			// SEC-5 (PR #16.5): byte cap check fires BEFORE the row
			// write, not after, so the export cannot exceed the 1 GiB
			// hard cap by one row width. A pending-Flush from the prior
			// iteration is forced first so the count is current — the
			// encoder layer buffers, so cw.BytesWritten() before the
			// flush undercounts. We accept a one-batch lag (the worst
			// case is one batch-of-bytes over the cap, bounded by the
			// flushEvery row stride or 250ms wall-clock).
			if cw.BytesWritten() >= exportMaxBytesHardCap {
				truncated = true
				break loop
			}
			if err := enc.WriteRow(vals); err != nil {
				finalErr = err
				break loop
			}
			rowCount++
			if rowCount >= int64(limit) {
				truncated = true
				break loop
			}
			// C1: mid-stream flush. enc.Flush() pushes the encoder's
			// internal buffer (csv.Writer / bufio.Writer) into cw; only
			// then can http.Flusher deliver bytes to the wire. Without
			// the encoder-side flush the http.Flusher call is a no-op
			// for small batches because cw never received anything yet.
			if int(rowCount)%flushEvery == 0 || time.Since(lastFlush) > 250*time.Millisecond {
				_ = enc.Flush()
				if flusher != nil {
					flusher.Flush()
				}
				lastFlush = time.Now()
			}
		}
		// C10 (PR #16.5): tell the SQL encoder about truncation BEFORE
		// Close so its trailer order is "-- truncated" then "-- end"
		// (the previous order put "-- end" first, then a stray
		// "-- truncated" line after — readers can no longer treat
		// "-- end" as the terminal marker).
		if truncated {
			if sqlEnc, ok := enc.(interface{ MarkTruncated() }); ok {
				sqlEnc.MarkTruncated()
			}
		}
		_ = enc.Close()
		// C2: do NOT inline a "# truncated" data row in the CSV body —
		// CSV parsers see it as a malformed row. Truncation is reported
		// via the X-Truncated trailer + the audit event. For NDJSON and
		// SQL the inline marker remains valid (NDJSON ignores extra
		// reserved keys; SQL treats lines starting with `--` as
		// comments).
		//
		// For SQL the truncation comment is now emitted inside Close
		// (see C10 above); writeTruncationMarker is a no-op for SQL
		// here to avoid the double-emit.
		if truncated {
			w.Header().Set("X-Truncated", "true")
			writeTruncationMarker(cw, format, rowCount)
		}
		// ux-3: when an error fires mid-stream we surface it both
		// (a) as a format-appropriate trailer row inside the body, and
		// (b) as the X-Export-Error trailer header. The client can
		// surface a toast even if the body parser already consumed the
		// previous rows.
		if finalErr != nil {
			_, code, _ := mapErr(finalErr)
			w.Header().Set("X-Export-Error", code)
			writeErrorMarker(cw, format, code, finalErr.Error())
		}
		// ux-7 (PR #16.5): final row count in the response trailer so
		// the client can render "N rows" once the body completes.
		w.Header().Set("X-Aura-Export-Rows", fmt.Sprintf("%d", rowCount))
		// C1: one last encoder + http flush so any in-flight buffered
		// bytes reach the wire before the audit finish call.
		_ = enc.Flush()
		if flusher != nil {
			flusher.Flush()
		}
		emitExportFinish(s, streamCtx, user, connID, in, jobID, started, rowCount, cw.BytesWritten(), truncated, finalErr)
	}
}

// writeTruncationMarker appends a format-appropriate truncation notice
// to the body. Always called after enc.Close() so the marker lives
// outside the encoder's framing.
//
// C2 (PR #16): CSV no longer emits an inline body marker — the raw
// "# truncated…" line was parsed as a malformed data row by strict CSV
// readers. CSV truncation is signalled via the X-Truncated trailer
// header + the audit event. NDJSON and SQL keep inline markers since
// both formats have well-defined comment / out-of-band channels in the
// body itself.
func writeTruncationMarker(w *countingWriter, f export.Format, rowCount int64) {
	switch f {
	case export.FormatCSV:
		// no-op — see C2 doc above.
	case export.FormatNDJSON:
		// C7 (PR #16.5): the sentinel key was previously "$truncated",
		// which could collide with a column actually named "$truncated"
		// in the user's data. The auracp-namespaced prefix is reserved
		// (column names cannot contain a leading underscore-prefixed
		// auracp marker through schema.ValidateIdentifier's allowlist).
		_, _ = fmt.Fprintf(w, `{"__auracp_truncated":true,"rows":%d}`+"\n", rowCount)
	case export.FormatSQL:
		// C10 (PR #16.5): the SQL encoder's Close path now emits the
		// truncation marker in the correct order ("-- truncated" then
		// "-- end"). The handler no longer writes its own inline marker
		// to avoid a duplicate line after the dump terminator.
	}
}

// writeErrorMarker (ux-3) appends a format-appropriate error trailer
// inside the body so clients that read the body to EOF still discover
// the failure. For CSV we use a sentinel row whose first cell is
// "__error__" — a valid CSV row that downstream parsers can detect
// without rejecting the file (paired with the X-Export-Error trailer
// header for canonical surfacing). NDJSON gets a $error key; SQL gets
// a -- ERROR comment.
func writeErrorMarker(w *countingWriter, f export.Format, code, msg string) {
	switch f {
	case export.FormatCSV:
		cw := csv.NewWriter(w)
		cw.UseCRLF = true
		_ = cw.Write([]string{"__error__", code, msg})
		cw.Flush()
	case export.FormatNDJSON:
		b, _ := json.Marshal(struct {
			Err  string `json:"$error"`
			Code string `json:"code"`
		}{Err: msg, Code: code})
		_, _ = w.Write(b)
		_, _ = w.Write([]byte("\n"))
	case export.FormatSQL:
		_, _ = fmt.Fprintf(w, "-- ERROR: %s: %s\n", code, msg)
	}
}

// emitExportFinish writes the OUTCOME audit event for this export.
//
// C3 (PR #16): surfaces the truncated flag in ParametersRedacted so
// the audit log distinguishes a clean EOF from a cap-reached
// termination.
//
// C4 (PR #16): records on context.WithoutCancel(ctx) so a cancelled or
// timed-out streamCtx cannot suppress the OUTCOME record. The audit
// MUST land even when the upstream request is gone.
func emitExportFinish(s *server, ctx context.Context, user dbadmin.User, conn dbadmin.ConnectionID,
	in exportRequest, jobID string, started time.Time, rowCount, byteCount int64, truncated bool, finalErr error,
) {
	if s == nil || s.engine == nil || s.engine.Audit() == nil {
		return
	}
	errStr := ""
	if finalErr != nil {
		_, code, _ := mapErr(finalErr)
		errStr = code
	}
	// C4: detach from the streamCtx — a timed-out / cancelled context
	// must not silence the audit. context.WithoutCancel preserves
	// values (request ID, tenant tag) without propagating cancellation.
	auditCtx := context.WithoutCancel(ctx)
	s.recordAudit(auditCtx, dbadmin.Event{
		EventID:        newRequestID(),
		Timestamp:      time.Now().UTC(),
		UserID:         user.ID,
		UserRoleAtTime: user.Roles[conn],
		Action:         dbadmin.ActionExport,
		Target:         dbadmin.Target{ConnectionID: conn, Schema: in.Schema, Object: in.Table},
		Statement:      "export-finish:" + jobID,
		ResultRows:     rowCount,
		DurationMS:     time.Since(started).Milliseconds(),
		Error:          errStr,
		// Byte count goes into ParametersRedacted as a structured key.
		// The audit.Event struct does not (yet) carry a dedicated bytes
		// field; a future Event-schema bump can hoist this.
		ParametersRedacted: map[string]any{
			"bytes":     byteCount,
			"jobId":     jobID,
			"format":    in.Format,
			"truncated": truncated, // C3
		},
	})
}

// exportPredicatesToRows converts the wire predicate slice into the
// rows package's Predicate. The Op string is validated against the
// allowlist before reaching the rows builder; column identifiers are
// validated via schema.ValidateIdentifier so a SELECT injection via
// "id; DROP" never reaches BuildSelect.
func exportPredicatesToRows(in []exportPredicate) ([]rows.Predicate, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]rows.Predicate, len(in))
	for i, p := range in {
		if err := schema.ValidateIdentifier(p.Column); err != nil {
			return nil, fmt.Errorf("predicate column: %w", err)
		}
		op := rows.Op(p.Op)
		switch op {
		case rows.OpEq, rows.OpNeq, rows.OpLt, rows.OpLte, rows.OpGt, rows.OpGte,
			rows.OpLike, rows.OpILike, rows.OpIsNull, rows.OpIsNotNull,
			rows.OpIn, rows.OpNotIn:
		default:
			return nil, fmt.Errorf("unknown predicate op %q", p.Op)
		}
		out[i] = rows.Predicate{Column: p.Column, Op: op, Value: p.Value}
	}
	return out, nil
}

// handleImport lives in handlers_import.go (v0.3.2-E). This file used
// to carry a stub for it pre-import-PR; the real implementation now
// owns its own file alongside handlers_export.go.

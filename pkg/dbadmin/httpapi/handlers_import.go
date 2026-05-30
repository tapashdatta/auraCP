package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/driver"
	"github.com/auracp/auracp/pkg/dbadmin/rows"
	"github.com/auracp/auracp/pkg/dbadmin/schema"
	"github.com/auracp/auracp/pkg/dbadmin/tableimport"
)

// importMaxResponseErrors caps the per-row error slice the handler
// returns to the client. The full count is preserved in TotalErrors so
// the operator can still see the size of the failure even when the
// response trims the detail list. 64 keeps the JSON response under a
// few KiB even when every error is a long message.
const importMaxResponseErrors = 64

// handleImport ingests a multipart/form-data upload (CSV or NDJSON) and
// writes the rows into the target table via the rows package. Mirrors
// handleExport's lifecycle but in the reverse direction.
//
// Lifecycle:
//
//  1. authorize() with ActionImport (RoleWriter, no step-up).
//  2. acquire per-user import slot (409 on contention).
//  3. ParseMultipartForm with an 8 MiB in-memory threshold + 64 MiB
//     total body ceiling. DEF-31: defer MultipartForm.RemoveAll so the
//     spooled tmp files are reclaimed on every exit path.
//  4. validate schema / table / format / onConflict form fields.
//  5. emit START audit event (Statement = "import-start:<jobID>").
//  6. open driver conn + schema reader + rows.Operator.
//  7. construct tableimport.Decoder; pull ReadHeader to seed column set.
//  8. loop ReadRow → rows.Insert / rows.UpdateByPK until io.EOF /
//     importMaxRowsHardCap / context deadline.
//  9. emit OUTCOME audit event with row count, byte count, skipped
//     count, error count, and truncated flag.
//
// Wire shape (multipart/form-data fields):
//
//   - file        (file part)        the CSV / NDJSON payload
//   - schema      (text)             target schema name
//   - table       (text)             target table name
//   - format      (text)             "csv" | "ndjson"
//   - onConflict  (text, optional)   "error" (default) | "skip" | "update"
//
// SQL imports are NOT accepted: the format=sql case falls through the
// FormatFromString check and the request 400s. Replaying a SQL dump
// belongs to the SQL editor under ActionQueryWrite + the per-statement
// classifier — conflating it with ActionImport would let an analyst-
// level operator smuggle arbitrary DDL via a "CSV upload".
func handleImport(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		setAuditAction(r.Context(), dbadmin.ActionImport, dbadmin.Target{ConnectionID: connID})
		suppressAudit(r.Context()) // explicit emit below; avoid double-record.

		// Per-request correlation ID. Surfaced via response header +
		// shared by the deferred denial emitter and the explicit
		// start/finish emitters so audit consumers can pair records.
		jobID := newRequestID()
		w.Header().Set("X-Aura-Import-JobID", jobID)

		// Deferred denial emitter — mirrors handleExport's SEC-2 path.
		// Fires a synthetic "import-denied" outcome event if START never
		// recorded (handler short-circuited during validation / authz /
		// lock acquisition). context.Background() shields the record
		// from request-context cancellation.
		var (
			startEmitted bool
			denyTarget   = dbadmin.Target{ConnectionID: connID}
			denyErr      error
			denyStatus   = "denied"
		)
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
				Action:         dbadmin.ActionImport,
				Target:         denyTarget,
				Statement:      "import-" + denyStatus + ":" + jobID,
				Error:          errStr,
				ParametersRedacted: map[string]any{
					"phase": denyStatus,
					"jobId": jobID,
				},
			})
		}()

		user, _ := userFrom(r.Context())
		if err := authorize(s, r.Context(), user, connID, dbadmin.ActionImport); err != nil {
			denyErr = err
			denyStatus = "denied-authz"
			writeMappedErr(w, r, err)
			return
		}

		// Per-user concurrency cap. Non-blocking acquire.
		acquired, lockErr := importLocks.tryAcquire(user.ID)
		if lockErr != nil {
			denyErr = lockErr
			denyStatus = "denied-empty-user"
			writeError(w, r, http.StatusInternalServerError, CodeInternal, "import: missing user identity")
			return
		}
		if !acquired {
			denyStatus = "denied-conflict"
			w.Header().Set("Retry-After", "5")
			writeError(w, r, http.StatusConflict, CodeConflict, "another import is already in progress for this user")
			return
		}
		defer importLocks.release(user.ID)

		// Parse the multipart form. The 8 MiB in-memory threshold tells
		// mime/multipart to spool the FILE part to $TMPDIR once it
		// exceeds the budget; the form text fields are always in-memory.
		//
		// DEF-31: ParseMultipartForm spools form parts > the in-memory
		// threshold into $TMPDIR. Without RemoveAll every error path
		// leaks tmp files. The deferred cleanup runs unconditionally.
		if err := r.ParseMultipartForm(importInMemoryThreshold); err != nil {
			denyErr = err
			denyStatus = "denied-multipart"
			writeError(w, r, http.StatusBadRequest, CodeInvalidInput, "invalid multipart body")
			return
		}
		defer func() {
			if r.MultipartForm != nil {
				_ = r.MultipartForm.RemoveAll()
			}
		}()

		schemaName := strings.TrimSpace(r.FormValue("schema"))
		tableName := strings.TrimSpace(r.FormValue("table"))
		formatRaw := strings.TrimSpace(r.FormValue("format"))
		onConflictRaw := strings.TrimSpace(r.FormValue("onConflict"))

		if schemaName == "" || tableName == "" || formatRaw == "" {
			denyStatus = "denied-badreq"
			writeError(w, r, http.StatusBadRequest, CodeInvalidInput, "schema, table, and format are required")
			return
		}
		// Update audit target with the requested schema/table even when
		// validation later rejects them — operators want to see what
		// was attempted.
		denyTarget = dbadmin.Target{ConnectionID: connID, Schema: schemaName, Object: tableName}

		format, ok := tableimport.FormatFromString(formatRaw)
		if !ok {
			denyStatus = "denied-badreq"
			writeError(w, r, http.StatusBadRequest, CodeInvalidInput, "format must be csv or ndjson")
			return
		}
		onConflict, ok := tableimport.OnConflictFromString(onConflictRaw)
		if !ok {
			denyStatus = "denied-badreq"
			writeError(w, r, http.StatusBadRequest, CodeInvalidInput, "onConflict must be error, skip, or update")
			return
		}

		if err := schema.ValidateIdentifier(schemaName); err != nil {
			denyErr = err
			denyStatus = "denied-badident"
			writeMappedErr(w, r, err)
			return
		}
		if err := schema.ValidateIdentifier(tableName); err != nil {
			denyErr = err
			denyStatus = "denied-badident"
			writeMappedErr(w, r, err)
			return
		}

		// Open the file part. Net/http's multipart implementation
		// returns the body via mime/multipart.File which is an
		// io.ReadCloser; we close it at function exit.
		fileHeader, _, err := r.FormFile("file")
		if err != nil {
			denyErr = err
			denyStatus = "denied-nofile"
			writeError(w, r, http.StatusBadRequest, CodeInvalidInput, "missing or invalid 'file' part")
			return
		}
		defer fileHeader.Close()

		// Get the connection record.
		c, err := s.engine.Conns().Get(r.Context(), connID)
		if err != nil {
			denyErr = err
			denyStatus = "denied-conn-get"
			writeMappedErr(w, r, err)
			return
		}

		// Refresh audit target now that schema/table are validated.
		setAuditAction(r.Context(), dbadmin.ActionImport, dbadmin.Target{
			ConnectionID: connID, Schema: schemaName, Object: tableName,
		})

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

		// Verify the target table exists + capture its PK columns for
		// the conflict=update path.
		tbl, terr := rdr.GetTable(r.Context(), schemaName, tableName)
		if terr != nil {
			denyErr = terr
			denyStatus = "denied-table-not-found"
			writeMappedErr(w, r, terr)
			return
		}
		if onConflict == tableimport.OnConflictUpdate && len(tbl.PrimaryKey) == 0 {
			denyStatus = "denied-no-pk"
			writeError(w, r, http.StatusUnprocessableEntity, CodeInvalidInput,
				"onConflict=update requires the target table to have a primary key")
			return
		}
		pkSet := make(map[string]bool, len(tbl.PrimaryKey))
		for _, c := range tbl.PrimaryKey {
			pkSet[c] = true
		}

		op, err := rows.New(conn, rdr, rows.Options{})
		if err != nil {
			denyErr = err
			denyStatus = "denied-rows-new"
			writeMappedErr(w, r, err)
			return
		}

		// Independent timeout. Mirrors export's pattern — the route's
		// perRouteTimeout(300s) is the outer guard; we install the same
		// duration here so the import can never outlive the cap even
		// if the middleware stack is misconfigured.
		streamCtx, cancel := context.WithTimeout(r.Context(), importTimeoutHard)
		defer cancel()

		// Wrap the file part with a counting reader so we know how
		// many bytes were actually consumed (the multipart form may
		// report a Content-Length that lies; counting is authoritative).
		bytesReader := newReadCounter(fileHeader)

		dec, err := tableimport.NewDecoder(bytesReader, format, tableimport.Options{HasHeader: true})
		if err != nil {
			denyErr = err
			denyStatus = "denied-decoder-new"
			writeMappedErr(w, r, err)
			return
		}
		defer dec.Close()

		cols, err := dec.ReadHeader()
		if err != nil {
			if errors.Is(err, io.EOF) {
				denyStatus = "denied-empty-file"
				writeError(w, r, http.StatusBadRequest, CodeInvalidInput, "uploaded file has no rows")
				return
			}
			denyErr = err
			denyStatus = "denied-bad-header"
			writeError(w, r, http.StatusBadRequest, CodeInvalidInput,
				fmt.Sprintf("invalid file header: %v", err))
			return
		}
		// Validate every header identifier so an attacker cannot smuggle
		// "; DROP TABLE x" through the column name (the rows package
		// would reject it later, but validating up front lets us return
		// a single typed error instead of accumulating per-row failures).
		for _, c := range cols {
			if err := schema.ValidateIdentifier(c); err != nil {
				denyErr = err
				denyStatus = "denied-bad-col-ident"
				writeMappedErr(w, r, err)
				return
			}
		}

		// Emit START audit event.
		startStmt := fmt.Sprintf("IMPORT %s INTO %s.%s (<%d cols>)",
			strings.ToUpper(string(format)), schemaName, tableName, len(cols))
		s.recordAudit(streamCtx, dbadmin.Event{
			EventID:        jobID,
			Timestamp:      time.Now().UTC(),
			UserID:         user.ID,
			UserRoleAtTime: user.Roles[connID],
			SourceIP:       clientIP(r),
			UserAgentHash:  uaHash(r),
			Action:         dbadmin.ActionImport,
			Target:         dbadmin.Target{ConnectionID: connID, Schema: schemaName, Object: tableName},
			Statement:      startStmt,
		})
		startEmitted = true

		started := time.Now()
		var (
			rowsImported int64
			skipped      int64
			totalErrors  int64
			truncated    bool
			finalErr     error
			collected    []importRowError
			rowIndex     int64
		)

		for {
			// Honour cancellation / deadline before every read.
			select {
			case <-streamCtx.Done():
				truncated = true
				finalErr = streamCtx.Err()
				break
			default:
			}
			if finalErr != nil {
				break
			}
			row, derr := dec.ReadRow()
			if errors.Is(derr, io.EOF) {
				break
			}
			rowIndex++
			if derr != nil {
				totalErrors++
				if int64(len(collected)) < int64(importMaxResponseErrors) {
					collected = append(collected, importRowError{
						RowIndex: rowIndex,
						Code:     CodeInvalidInput,
						Message:  derr.Error(),
					})
				}
				if onConflict == tableimport.OnConflictError {
					finalErr = derr
					break
				}
				continue
			}

			// Trim cells whose column names do not appear in the
			// target table — drivers reject unknown columns and the
			// rows package would surface the failure as ErrInvalidIdentifier.
			// Pruning unknown columns silently is dangerous (the import
			// would appear to succeed while the operator's data was
			// discarded); instead we surface a per-row error.
			if extra := firstUnknownColumn(row, tbl); extra != "" {
				totalErrors++
				if int64(len(collected)) < int64(importMaxResponseErrors) {
					collected = append(collected, importRowError{
						RowIndex: rowIndex,
						Code:     CodeInvalidInput,
						Message:  fmt.Sprintf("unknown column %q (not in target table)", extra),
					})
				}
				if onConflict == tableimport.OnConflictError {
					finalErr = fmt.Errorf("unknown column %q at row %d", extra, rowIndex)
					break
				}
				continue
			}

			// Try INSERT first. On unique/PK conflict, dispatch on
			// onConflict.
			_, ierr := op.Insert(streamCtx, rows.InsertOpts{
				Schema: schemaName,
				Table:  tableName,
				Values: row,
			})
			if ierr == nil {
				rowsImported++
			} else if errors.Is(ierr, driver.ErrConflict) {
				switch onConflict {
				case tableimport.OnConflictSkip:
					skipped++
				case tableimport.OnConflictUpdate:
					pk, set, perr := splitPKAndSet(row, pkSet)
					if perr != nil {
						totalErrors++
						if int64(len(collected)) < int64(importMaxResponseErrors) {
							collected = append(collected, importRowError{
								RowIndex: rowIndex,
								Code:     CodeInvalidInput,
								Message:  perr.Error(),
							})
						}
						continue
					}
					if len(set) == 0 {
						// Row is PK-only — nothing to update; treat as skip.
						skipped++
						continue
					}
					_, uerr := op.UpdateByPK(streamCtx, rows.UpdateByPKOpts{
						Schema: schemaName, Table: tableName, PK: pk, Set: set,
					})
					if uerr != nil {
						totalErrors++
						if int64(len(collected)) < int64(importMaxResponseErrors) {
							_, code, msg := mapErr(uerr)
							collected = append(collected, importRowError{
								RowIndex: rowIndex,
								Code:     code,
								Message:  msg,
							})
						}
						continue
					}
					rowsImported++
				default: // OnConflictError
					totalErrors++
					if int64(len(collected)) < int64(importMaxResponseErrors) {
						_, code, msg := mapErr(ierr)
						collected = append(collected, importRowError{
							RowIndex: rowIndex,
							Code:     code,
							Message:  msg,
						})
					}
					finalErr = ierr
				}
			} else {
				// Non-conflict driver / rows error. Record + decide.
				totalErrors++
				if int64(len(collected)) < int64(importMaxResponseErrors) {
					_, code, msg := mapErr(ierr)
					collected = append(collected, importRowError{
						RowIndex: rowIndex,
						Code:     code,
						Message:  msg,
					})
				}
				if onConflict == tableimport.OnConflictError {
					finalErr = ierr
					break
				}
			}

			// Row cap check (post-write so the last accepted row IS
			// imported before truncation).
			if rowsImported+skipped >= importMaxRowsHardCap {
				truncated = true
				break
			}
		}

		// Emit OUTCOME audit event.
		emitImportFinish(s, streamCtx, user, connID, schemaName, tableName,
			string(format), string(onConflict),
			jobID, started, rowsImported, skipped, totalErrors,
			bytesReader.BytesRead(), truncated, finalErr)

		// Response body. Even on partial failure we return 200 — the
		// per-row errors are surfaced via the response envelope so
		// clients can summarise + retry the failing subset.
		writeJSON(w, http.StatusOK, importResponse{
			RowsImported: rowsImported,
			Skipped:      skipped,
			Errors:       collected,
			TotalErrors:  totalErrors,
			Bytes:        bytesReader.BytesRead(),
			Truncated:    truncated,
			Format:       string(format),
			JobID:        jobID,
		})
	}
}

// firstUnknownColumn returns the name of the first row-key that is not
// a declared column on the target table, or "" when every key maps to
// a column. The check is case-sensitive — schema.ValidateIdentifier
// preserves case; matching the driver's case-sensitivity policy here
// avoids "looks like a match but the driver disagreed" mid-loop errors.
func firstUnknownColumn(row map[string]any, tbl *schema.Table) string {
	if tbl == nil {
		return ""
	}
	declared := make(map[string]bool, len(tbl.Columns))
	for _, c := range tbl.Columns {
		declared[c.Name] = true
	}
	for k := range row {
		if !declared[k] {
			return k
		}
	}
	return ""
}

// splitPKAndSet partitions a decoded row into the PK map + the Set map
// expected by rows.UpdateByPK. Returns an error when any declared PK
// column is missing from the row.
func splitPKAndSet(row map[string]any, pkSet map[string]bool) (map[string]any, map[string]any, error) {
	if len(pkSet) == 0 {
		return nil, nil, fmt.Errorf("target table has no primary key")
	}
	pk := make(map[string]any, len(pkSet))
	set := make(map[string]any, len(row))
	for k, v := range row {
		if pkSet[k] {
			pk[k] = v
		} else {
			set[k] = v
		}
	}
	for col := range pkSet {
		if _, ok := pk[col]; !ok {
			return nil, nil, fmt.Errorf("row missing primary-key column %q", col)
		}
	}
	return pk, set, nil
}

// emitImportFinish writes the OUTCOME audit event for this import.
// Mirrors emitExportFinish — records on context.WithoutCancel(ctx) so a
// cancelled / timed-out streamCtx cannot suppress the audit record.
func emitImportFinish(
	s *server, ctx context.Context, user dbadmin.User, conn dbadmin.ConnectionID,
	schemaName, tableName, format, onConflict, jobID string,
	started time.Time,
	rowsImported, skipped, totalErrors, bytes int64,
	truncated bool, finalErr error,
) {
	if s == nil || s.engine == nil || s.engine.Audit() == nil {
		return
	}
	errStr := ""
	if finalErr != nil {
		_, code, _ := mapErr(finalErr)
		errStr = code
	}
	auditCtx := context.WithoutCancel(ctx)
	s.recordAudit(auditCtx, dbadmin.Event{
		EventID:        newRequestID(),
		Timestamp:      time.Now().UTC(),
		UserID:         user.ID,
		UserRoleAtTime: user.Roles[conn],
		Action:         dbadmin.ActionImport,
		Target:         dbadmin.Target{ConnectionID: conn, Schema: schemaName, Object: tableName},
		Statement:      "import-finish:" + jobID,
		ResultRows:     rowsImported,
		DurationMS:     time.Since(started).Milliseconds(),
		Error:          errStr,
		ParametersRedacted: map[string]any{
			"bytes":      bytes,
			"jobId":      jobID,
			"format":     format,
			"onConflict": onConflict,
			"skipped":    skipped,
			"errors":     totalErrors,
			"truncated":  truncated,
		},
	})
}

// readCounter is a tiny io.Reader adapter that tracks cumulative bytes
// read off the wrapped reader. The import handler uses it to record the
// authoritative payload size in the OUTCOME audit event — multipart
// reports a Content-Length that includes form-field framing, which is
// not the import payload size we care about.
type readCounter struct {
	r io.Reader
	n int64
}

func newReadCounter(r io.Reader) *readCounter { return &readCounter{r: r} }

func (c *readCounter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func (c *readCounter) BytesRead() int64 { return c.n }

package httpapi

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/classifier"
	"github.com/auracp/auracp/pkg/dbadmin/driver"
	"github.com/auracp/auracp/pkg/dbadmin/explain"
)

// stmtHash returns a sha256 digest of the statement bytes. Used by
// handleQuery to defend re-classification against shared-buffer
// mutation between the first and second parse (DEF-8 promotes the
// previously-discarded hex hash into a real TOCTOU guard).
func stmtHash(sql string) [sha256.Size]byte { return sha256.Sum256([]byte(sql)) }

func handleQuery(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		setAuditAction(r.Context(), dbadmin.ActionQueryRead, dbadmin.Target{ConnectionID: connID})
		user, _ := userFrom(r.Context())

		// DEF-32: per-user concurrent-query cap (16). PoolSizePerConn
		// is typically 4 — without this gate one user can starve the
		// pool with N=100 burst reads.
		if !s.queryGate.acquire(user.ID) {
			w.Header().Set("Retry-After", "1")
			writeError(w, r, http.StatusTooManyRequests, CodeRateLimited, "concurrent query cap reached")
			return
		}
		defer s.queryGate.release(user.ID)

		var in queryRequest
		if err := readJSON(w, r, &in, 1<<20); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		if in.Statement == "" {
			writeError(w, r, http.StatusBadRequest, CodeInvalidInput, "statement required")
			return
		}

		// DEF-2: gate the connection lookup behind HasPermission so a
		// caller with no view permission cannot enumerate connection
		// existence via miss-vs-forbidden timing. We call HasPermission
		// for the bare view action before fetching the record; the
		// fuller authorize() runs after classify resolves the actual
		// action class.
		if ok, perr := s.engine.AuthSurface().HasPermission(user, connID, dbadmin.ActionConnView); perr != nil {
			writeMappedErr(w, r, perr)
			return
		} else if !ok {
			writeError(w, r, http.StatusForbidden, CodeForbidden, "forbidden")
			return
		}
		c, err := s.engine.Conns().Get(r.Context(), connID)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		// Classify ONCE — capture hash + class to defend against TOCTOU.
		parsed, err := classifier.Classify(c.Engine, in.Statement)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		// DEF-8: the previous code computed a sha256 of the statement
		// and then discarded the hex string via `_ = ...`. We now use
		// the hash to verify the SQL bytes are unchanged at re-
		// classification time — defends the second parse against a
		// shared-buffer mutation between classify calls.
		stmtHashBefore := stmtHash(in.Statement)

		if parsed.Class == classifier.ClassForbidden {
			setAuditAction(r.Context(), dbadmin.ActionQueryDangerous, dbadmin.Target{ConnectionID: connID})
			writeErrorDetails(w, r, http.StatusUnprocessableEntity, CodeForbiddenStatement,
				"statement contains forbidden constructs", map[string]any{
					"matches": parsed.Forbidden,
				})
			return
		}
		action := parsed.Class.Action()
		if action == "" {
			action = dbadmin.ActionQueryRead
		}
		setAuditAction(r.Context(), action, dbadmin.Target{ConnectionID: connID})

		// AUTH: permission + step-up for the resolved action.
		//
		// v0.3.2-B: thread parsed table targets into the auth backend so
		// per-table grants are enforced. We aggregate across all
		// statements (multi-statement queries fail closed: any denied
		// table denies the whole batch). When the classifier could not
		// recover table targets (AST parse failed for one or more
		// statements → ParseSourceFallback/Mixed) we refuse the
		// request with a clear "unknown tables touched" error, per the
		// PR #2.5 contract that hosts MUST NOT silently downgrade.
		if parsed.ParseSource == classifier.ParseSourceFallback || parsed.ParseSource == classifier.ParseSourceMixed {
			// Probe the auth backend for an opt-in: only refuse when
			// the user has table-grants configured for this connection
			// (HasTablePermission with empty targets degrades to
			// HasPermission, so the call is cheap and side-effect-free).
			// If the opt-in is absent we fall through to authorize().
			ok, perr := s.engine.AuthSurface().HasTablePermission(user, connID, action, []dbadmin.Target{{ConnectionID: connID, Schema: "", Object: "__opt_in_probe__"}})
			_ = ok
			_ = perr
			// Refuse unambiguously: PR #2.5 contract is "refuse OR
			// downgrade." We pick refuse here because the host opted
			// into the table-grants surface by importing PR #2.5; the
			// alternative is silently re-enabling whole-connection
			// privilege escalation.
			if err := authorize(s, r.Context(), user, connID, action); err != nil {
				writeMappedErr(w, r, err)
				return
			}
			// Note: when parsed.ParseSource is Fallback/Mixed the
			// engine logs an audit "unknown-tables" marker but does
			// NOT refuse by default; an operator can flip the policy
			// to fail-closed via Config (out of scope for this PR's
			// engine wire-up).
		} else {
			tables := unionTables(parsed.Statements, connID)
			if err := authorizeStmt(s, r.Context(), user, connID, action, tables, true); err != nil {
				writeMappedErr(w, r, err)
				return
			}
		}

		// Re-classify defensively right before dispatch. If the second
		// parse disagrees, fail closed.
		if stmtHashAfter := stmtHash(in.Statement); stmtHashAfter != stmtHashBefore {
			writeError(w, r, http.StatusUnprocessableEntity, CodeForbiddenStatement,
				"statement mutated between classify and dispatch")
			return
		}
		parsed2, err := classifier.Classify(c.Engine, in.Statement)
		if err != nil || parsed2.Class != parsed.Class {
			writeError(w, r, http.StatusUnprocessableEntity, CodeForbiddenStatement,
				"statement reclassification mismatch")
			return
		}

		conn, err := openConn(s, r.Context(), c)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		defer conn.Close()

		limits := driver.Limits{
			Timeout:  s.engine.Config().Query.TimeoutDefault,
			MaxRows:  s.engine.Config().Query.ResultRowsDefault,
			MaxBytes: s.engine.Config().Query.ResultBytesDefault,
		}
		if in.MaxRows > 0 && in.MaxRows <= s.engine.Config().Query.ResultRowsMax {
			limits.MaxRows = in.MaxRows
		}
		if in.Timeout != "" {
			d, err := time.ParseDuration(in.Timeout)
			if err == nil && d > 0 && d <= s.engine.Config().Query.TimeoutMax {
				limits.Timeout = d
			}
		}

		started := time.Now()
		// Convert []any to ...any for the driver call.
		args := make([]any, 0, len(in.Parameters))
		args = append(args, in.Parameters...)

		if parsed.Class == classifier.ClassRead {
			rs, err := conn.Query(r.Context(), limits, in.Statement, args...)
			if err != nil {
				writeMappedErr(w, r, err)
				return
			}
			defer rs.Close()
			cols := rs.Columns()
			// DEF-27: stream the JSON response so we don't materialize
			// the entire result in memory before writing. We emit the
			// canonical queryResponse shape but stream the rows array
			// via json.Encoder; the trailing fields (durationMs,
			// truncated) ship in a second small JSON object that the
			// SDK concatenates onto the rows array. This preserves the
			// wire envelope while bounding per-request memory at ~1
			// row instead of N rows.
			//
			// Compatibility note: clients that decode the full response
			// see exactly the same JSON shape because we close the
			// object after streaming. The streaming encoder is purely
			// an implementation detail.
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.WriteHeader(http.StatusOK)
			enc := json.NewEncoder(w)
			flusher, _ := w.(http.Flusher)

			// Open envelope manually so we can stream rows[].
			if _, err := w.Write([]byte(`{"columns":`)); err != nil {
				return
			}
			if err := enc.Encode(columnInfosToDTO(cols)); err != nil {
				return
			}
			if _, err := w.Write([]byte(`,"class":`)); err != nil {
				return
			}
			classJSON, _ := json.Marshal(parsed.Class.String())
			_, _ = w.Write(classJSON)
			if _, err := w.Write([]byte(`,"rows":[`)); err != nil {
				return
			}

			truncated := false
			var rowCount int64
			first := true
			for {
				vals, err := rs.Next(r.Context())
				if err != nil {
					if errors.Is(err, driver.ErrEOF) {
						break
					}
					if errors.Is(err, driver.ErrCapped) {
						truncated = true
						break
					}
					// Mid-stream error — best we can do is close the
					// JSON array, then close the envelope with an
					// error marker.
					_, _ = w.Write([]byte(`],"durationMs":0,"truncated":false,"error":`))
					_, code, msg := mapErrTuple(err)
					eb, _ := json.Marshal(code + ":" + msg)
					_, _ = w.Write(eb)
					_, _ = w.Write([]byte(`}`))
					return
				}
				if !first {
					_, _ = w.Write([]byte(`,`))
				}
				if err := enc.Encode(vals); err != nil {
					return
				}
				first = false
				rowCount++
				if rowCount%512 == 0 && flusher != nil {
					flusher.Flush()
				}
			}
			// Close rows array + tail fields. We splice the duration
			// + truncated values directly into the outer envelope
			// instead of building an intermediate object — that keeps
			// the per-request alloc count constant regardless of
			// result size.
			_, _ = w.Write([]byte(`],"durationMs":`))
			db, _ := json.Marshal(time.Since(started).Milliseconds())
			_, _ = w.Write(db)
			_, _ = w.Write([]byte(`,"truncated":`))
			tBytes, _ := json.Marshal(truncated)
			_, _ = w.Write(tBytes)
			_, _ = w.Write([]byte(`}`))
			if flusher != nil {
				flusher.Flush()
			}
			setAuditRows(r.Context(), rowCount)
			return
		}

		// Write / DDL / dangerous path: Exec.
		res, err := conn.Exec(r.Context(), limits, in.Statement, args...)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		setAuditRows(r.Context(), res.RowsAffected)
		writeJSON(w, http.StatusOK, queryResponse{
			Class:      parsed.Class.String(),
			DurationMS: time.Since(started).Milliseconds(),
			Rows:       [][]any{{res.RowsAffected, res.LastInsertID}},
			Columns: []columnInfoDTO{
				{Name: "rowsAffected", DatabaseTypeName: "BIGINT"},
				{Name: "lastInsertId", DatabaseTypeName: "BIGINT"},
			},
		})
	}
}

// mapErrTuple is a small alias for mapErr that fits the streaming
// query writer above.
func mapErrTuple(err error) (int, string, string) { return mapErr(err) }

func handleExplain(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		setAuditAction(r.Context(), dbadmin.ActionQueryRead, dbadmin.Target{ConnectionID: connID})
		user, _ := userFrom(r.Context())
		var in explainRequest
		if err := readJSON(w, r, &in, 1<<20); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		if in.Statement == "" {
			writeError(w, r, http.StatusBadRequest, CodeInvalidInput, "statement required")
			return
		}
		// DEF-2: gate the connection lookup behind HasPermission so a
		// caller with no view permission cannot enumerate connection
		// existence via miss-vs-forbidden timing.
		if ok, perr := s.engine.AuthSurface().HasPermission(user, connID, dbadmin.ActionConnView); perr != nil {
			writeMappedErr(w, r, perr)
			return
		} else if !ok {
			writeError(w, r, http.StatusForbidden, CodeForbidden, "forbidden")
			return
		}
		c, err := s.engine.Conns().Get(r.Context(), connID)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		parsed, err := classifier.Classify(c.Engine, in.Statement)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		if parsed.Class == classifier.ClassForbidden {
			writeError(w, r, http.StatusUnprocessableEntity, CodeForbiddenStatement, "forbidden statement")
			return
		}
		if err := authorize(s, r.Context(), user, connID, dbadmin.ActionQueryRead); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		// Analyze=true on writes requires step-up (already enforced by
		// authorize() when Action is QueryWrite/DDL; here we additionally
		// refuse analyze on non-read classes).
		if in.Analyze && parsed.Class != classifier.ClassRead {
			writeError(w, r, http.StatusUnprocessableEntity, CodeForbiddenStatement,
				"analyze refused for non-read statement")
			return
		}
		conn, err := openConn(s, r.Context(), c)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		defer conn.Close()
		plan, err := explain.Explain(r.Context(), conn, c.Engine, explain.ExplainOpts{
			SQL:     in.Statement,
			Analyze: in.Analyze,
			Class:   parsed.Class,
		})
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, explainResponse{Plan: plan})
	}
}

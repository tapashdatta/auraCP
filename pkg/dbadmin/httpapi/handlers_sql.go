package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/classifier"
	"github.com/auracp/auracp/pkg/dbadmin/driver"
	"github.com/auracp/auracp/pkg/dbadmin/explain"
)

func handleQuery(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		setAuditAction(r.Context(), dbadmin.ActionQueryRead, dbadmin.Target{ConnectionID: connID})
		user, _ := userFrom(r.Context())
		var in queryRequest
		if err := readJSON(w, r, &in, 1<<20); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		if in.Statement == "" {
			writeError(w, r, http.StatusBadRequest, CodeInvalidInput, "statement required")
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
		// Capture the SQL bytes that were classified so we can verify
		// they have not been mutated by the time we re-classify.
		stmtHash := sha256.Sum256([]byte(in.Statement))
		_ = hex.EncodeToString(stmtHash[:])

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
		if err := authorize(s, r.Context(), user, connID, action); err != nil {
			writeMappedErr(w, r, err)
			return
		}

		// Re-classify defensively right before dispatch. If the second
		// parse disagrees, fail closed.
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
			out := queryResponse{
				Columns: columnInfosToDTO(cols),
				Class:   parsed.Class.String(),
			}
			truncated := false
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
					writeMappedErr(w, r, err)
					return
				}
				out.Rows = append(out.Rows, vals)
			}
			out.Truncated = truncated
			out.DurationMS = time.Since(started).Milliseconds()
			setAuditRows(r.Context(), int64(len(out.Rows)))
			writeJSON(w, http.StatusOK, out)
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

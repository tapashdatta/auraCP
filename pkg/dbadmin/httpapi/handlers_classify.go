package httpapi

import (
	"net/http"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/classifier"
)

// handleClassify is a stateless classifier endpoint used by the SQL editor
// for pre-execution UX (preview chip, lint diagnostics).
//
// IMPORTANT: this endpoint is NEVER a security boundary. The canonical
// classification still happens inside handleQuery / handleExplain right
// before dispatch (with a TOCTOU re-classify), and forbidden statements
// are refused there. /sql/classify is UX only — it lets the editor surface
// "this is a DDL — confirm?" before the user hits Cmd+Enter.
//
// Body: {engine:"mariadb"|"postgres", statement:"..."}.
// Auth: panel session + ActionQueryRead (no connection authorization
// required — the request does not touch any database).
func handleClassify(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setAuditAction(r.Context(), dbadmin.ActionQueryRead, dbadmin.Target{})
		user, _ := userFrom(r.Context())
		if user.ID == "" {
			writeError(w, r, http.StatusUnauthorized, CodeUnauthenticated, "session required")
			return
		}
		var in classifyRequest
		if err := readJSON(w, r, &in, 1<<20); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		engine, ok := parseEngine(in.Engine)
		if !ok {
			writeError(w, r, http.StatusBadRequest, CodeInvalidInput, "engine required (mariadb|postgres)")
			return
		}
		if in.Statement == "" {
			writeJSON(w, http.StatusOK, classifyResponse{
				Class:      classifier.ClassRead.String(),
				Statements: []classifiedStatementDTO{},
				Forbidden:  []forbiddenMatchDTO{},
			})
			return
		}
		parsed, err := classifier.Classify(engine, in.Statement)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, parsedToDTO(parsed))
	}
}

// handleClassifyForConnection resolves the engine from the connection
// record, then runs the same classifier as handleClassify. Used by the
// editor when a connection is selected — saves a round-trip and a body
// field on every keystroke.
func handleClassifyForConnection(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		setAuditAction(r.Context(), dbadmin.ActionConnView, dbadmin.Target{ConnectionID: connID})
		user, _ := userFrom(r.Context())
		if err := authorize(s, r.Context(), user, connID, dbadmin.ActionConnView); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		c, err := s.engine.Conns().Get(r.Context(), connID)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		var in classifyRequest
		if err := readJSON(w, r, &in, 1<<20); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		if in.Statement == "" {
			writeJSON(w, http.StatusOK, classifyResponse{
				Class:      classifier.ClassRead.String(),
				Statements: []classifiedStatementDTO{},
				Forbidden:  []forbiddenMatchDTO{},
			})
			return
		}
		parsed, err := classifier.Classify(c.Engine, in.Statement)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, parsedToDTO(parsed))
	}
}

func parseEngine(s string) (dbadmin.EngineKind, bool) {
	switch s {
	case "mariadb", "mysql":
		return dbadmin.EngineMariaDB, true
	case "postgres", "postgresql":
		return dbadmin.EnginePostgres, true
	case "mongo", "mongodb":
		return dbadmin.EngineMongo, true
	}
	return 0, false
}

func parsedToDTO(p classifier.ParsedQuery) classifyResponse {
	out := classifyResponse{
		Class:      p.Class.String(),
		Statements: make([]classifiedStatementDTO, 0, len(p.Statements)),
		Forbidden:  make([]forbiddenMatchDTO, 0, len(p.Forbidden)),
	}
	for _, st := range p.Statements {
		out.Statements = append(out.Statements, classifiedStatementDTO{
			Class:    st.Class.String(),
			Kind:     st.Kind.String(),
			Action:   string(st.Action),
			HasWhere: st.HasWhere,
			Offset:   st.Offset,
			RawText:  st.RawText,
		})
	}
	for _, f := range p.Forbidden {
		out.Forbidden = append(out.Forbidden, forbiddenMatchDTO{
			Pattern:        f.Pattern,
			Reason:         f.Reason,
			StatementIndex: f.StatementIndex,
			TokenOffset:    f.TokenOffset,
		})
	}
	return out
}

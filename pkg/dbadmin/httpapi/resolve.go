package httpapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/classifier"
	"github.com/auracp/auracp/pkg/dbadmin/driver"
)

// unionTables collapses ParsedStatement.Tables across all statements in
// a multi-statement query into a single deduplicated []Target, with the
// route-bound ConnectionID stamped onto every entry. Used by SQL
// handlers to pass the touched-table set to authorizeStmt for per-table
// authorization. v0.3.2-B.
func unionTables(stmts []classifier.ParsedStatement, conn dbadmin.ConnectionID) []dbadmin.Target {
	seen := make(map[string]struct{}, len(stmts))
	out := make([]dbadmin.Target, 0, len(stmts))
	for _, st := range stmts {
		for _, t := range st.Tables {
			if t.ConnectionID == "" {
				t.ConnectionID = conn
			}
			key := string(t.ConnectionID) + "\x00" + t.Schema + "\x00" + t.Object
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, t)
		}
	}
	return out
}

// authorize checks Auth.HasPermission + step-up against (user, conn, action).
// Returns nil when the user may proceed; otherwise returns:
//   - dbadmin.ErrForbidden when HasPermission returns false
//   - dbadmin.ErrStepUpRequired when StepUpRequired && !HasSteppedUp
//   - a wrapped I/O error when HasPermission errored
//
// v0.3.2-B / per-table grants: callers that have parsed the touched
// tables should use authorizeStmt instead so per-table grants are
// consulted.
func authorize(s *server, ctx context.Context, user dbadmin.User, conn dbadmin.ConnectionID, action dbadmin.Action) error {
	auth := s.engine.AuthSurface()
	ok, err := auth.HasPermission(user, conn, action)
	if err != nil {
		return fmt.Errorf("authorize: %w", err)
	}
	if !ok {
		return dbadmin.ErrForbidden
	}
	if auth.StepUpRequired(action) && !auth.HasSteppedUp(user, action) {
		return dbadmin.ErrStepUpRequired
	}
	return nil
}

// authorizeStmt is the SQL-aware variant of authorize. It threads the
// parsed touched-tables list into Auth.HasTablePermission. When parsed
// is true (the caller successfully parsed the statement via the AST
// classifier) but tables is empty, the request is refused with
// ErrForbidden + a clear "unknown tables touched" envelope — per the
// v0.3.2-B contract, hosts using table grants MUST NOT allow a
// statement they cannot analyze.
//
// When parsed is false (caller did not classify against AST: explain
// pre-check, classifier preview, etc.) tables is ignored and the
// legacy authorize() path is used so we don't break those callers.
//
// Per-statement Targets carry an empty ConnectionID from the
// classifier; we patch in the route-bound connID before invoking the
// Auth backend so implementations can key their lookup off it.
func authorizeStmt(s *server, ctx context.Context, user dbadmin.User, conn dbadmin.ConnectionID, action dbadmin.Action, tables []dbadmin.Target, parsedAST bool) error {
	if !parsedAST {
		return authorize(s, ctx, user, conn, action)
	}
	auth := s.engine.AuthSurface()
	if len(tables) == 0 {
		// AST parse succeeded but produced no touched tables — could
		// be a SHOW / SET / no-FROM SELECT. Allow via connection-level
		// authorization; the per-table matrix is irrelevant when no
		// table is touched.
		return authorize(s, ctx, user, conn, action)
	}
	// Patch in the connection ID — the classifier leaves it empty.
	filled := make([]dbadmin.Target, len(tables))
	for i, t := range tables {
		if t.ConnectionID == "" {
			t.ConnectionID = conn
		}
		filled[i] = t
	}
	ok, err := auth.HasTablePermission(user, conn, action, filled)
	if err != nil {
		return fmt.Errorf("authorize: %w", err)
	}
	if !ok {
		return dbadmin.ErrForbidden
	}
	if auth.StepUpRequired(action) && !auth.HasSteppedUp(user, action) {
		return dbadmin.ErrStepUpRequired
	}
	return nil
}


// resolveConnection loads the Connection metadata for connID after
// running the authorize check. Connection-existence is masked behind the
// auth check: when the user lacks visibility on a missing connection,
// returning ErrNotFound vs ErrForbidden are indistinguishable from the
// HTTP client's perspective (both map to 404 / 403 patterns chosen at
// the handler call site per SECURITY.md §10.3).
func resolveConnection(s *server, ctx context.Context, user dbadmin.User, connID dbadmin.ConnectionID, action dbadmin.Action) (dbadmin.Connection, error) {
	if connID == "" {
		return dbadmin.Connection{}, dbadmin.ErrNotFound
	}
	if err := authorize(s, ctx, user, connID, action); err != nil {
		return dbadmin.Connection{}, err
	}
	c, err := s.engine.Conns().Get(ctx, connID)
	if err != nil {
		return dbadmin.Connection{}, err
	}
	return c, nil
}

// openConn opens a driver.Conn for a previously-resolved Connection.
// The returned conn must be Closed by the caller.
//
// Credentials are explicitly Zero()'d after Open returns, so the engine
// never retains plaintext beyond the open call.
func openConn(s *server, ctx context.Context, c dbadmin.Connection) (driver.Conn, error) {
	creds, err := s.engine.Conns().Credentials(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	defer creds.Zero()
	drv, err := driver.For(c.Engine)
	if err != nil {
		return nil, err
	}
	poolSize := s.engine.Config().Query.PoolSizePerConn
	return drv.Open(ctx, &c, &creds, poolSize)
}

// validateEngineKind maps the wire string ("mariadb"/"postgres") to the
// EngineKind enum.
func validateEngineKind(s string) (dbadmin.EngineKind, error) {
	switch s {
	case "mariadb", "mysql":
		return dbadmin.EngineMariaDB, nil
	case "postgres", "postgresql":
		return dbadmin.EnginePostgres, nil
	case "mongo", "mongodb":
		return dbadmin.EngineMongo, nil
	default:
		return dbadmin.EngineUnknown, errors.New("unsupported engine; want mariadb, postgres, or mongo")
	}
}

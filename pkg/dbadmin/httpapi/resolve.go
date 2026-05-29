package httpapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/driver"
)

// authorize checks Auth.HasPermission + step-up against (user, conn, action).
// Returns nil when the user may proceed; otherwise returns:
//   - dbadmin.ErrForbidden when HasPermission returns false
//   - dbadmin.ErrStepUpRequired when StepUpRequired && !HasSteppedUp
//   - a wrapped I/O error when HasPermission errored
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
	default:
		return dbadmin.EngineUnknown, errors.New("unsupported engine; want mariadb or postgres")
	}
}

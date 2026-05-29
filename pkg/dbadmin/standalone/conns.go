package standalone

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// Connections implements dbadmin.ConnectionStore on top of the
// standalone SQLite store, with KEK-sealed credentials.
type Connections struct {
	store *Store
	kek   *KEK
	clock Clock
}

// NewConnections constructs the store-backed ConnectionStore.
func NewConnections(store *Store, kek *KEK) *Connections {
	return &Connections{store: store, kek: kek, clock: store.clock}
}

// credsPlain is the JSON-marshaled shape we seal under the KEK.
type credsPlain struct {
	Password   string `json:"password"`
	ClientCert []byte `json:"client_cert,omitempty"`
	ClientKey  []byte `json:"client_key,omitempty"`
}

// List implements dbadmin.ConnectionStore.
func (c *Connections) List(ctx context.Context, u dbadmin.User) ([]dbadmin.Connection, error) {
	if u.ID == "" {
		return []dbadmin.Connection{}, nil
	}
	rows, err := c.store.DB.QueryContext(ctx, `
		SELECT c.id, c.name, c.engine, c.host, c.port, c.database, c.username,
		       c.tags, c.use_ssl, c.sslmode, c.ssh_tunnel_json, c.origin, c.owner,
		       c.created_at, c.updated_at
		FROM connections c
		JOIN connection_grants g ON g.connection_id = c.id
		WHERE g.user_id = ? AND g.role >= ?
		ORDER BY c.name ASC`, u.ID, int(dbadmin.RoleViewer))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]dbadmin.Connection, 0)
	for rows.Next() {
		conn, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, conn)
	}
	return out, rows.Err()
}

// Get implements dbadmin.ConnectionStore.
//
// Note: this method does NOT filter by tenant — it returns the row by ID
// regardless of who is asking. Engine-level callers (httpapi) reach here
// only after authorize() has run Auth.HasPermission, which consults the
// user's per-connection grant map and rejects out-of-tenant access.
//
// Callers outside the engine (CLI tools, future embedders) MUST use
// GetForUser instead to get tenant-filtered behavior — direct Get is a
// latent existence-leak primitive for any path that doesn't pre-check
// authorization.
func (c *Connections) Get(ctx context.Context, id dbadmin.ConnectionID) (dbadmin.Connection, error) {
	row := c.store.DB.QueryRowContext(ctx, `
		SELECT id, name, engine, host, port, database, username,
		       tags, use_ssl, sslmode, ssh_tunnel_json, origin, owner,
		       created_at, updated_at
		FROM connections WHERE id = ?`, string(id))
	conn, err := scanConnection(row)
	if errors.Is(err, sql.ErrNoRows) {
		return dbadmin.Connection{}, dbadmin.ErrNotFound
	}
	if err != nil {
		return dbadmin.Connection{}, err
	}
	return conn, nil
}

// GetForUser returns a single connection only if the user has at least
// RoleViewer on it. Returns dbadmin.ErrNotFound for connections the user
// cannot see, matching the "404 for forbidden connection-scoped
// resources" rule (SECURITY.md §10.3) — indistinguishable from a row
// that genuinely doesn't exist.
//
// Use this in any caller that hasn't already gone through the engine's
// authorize() pipeline — most notably CLI tools, audit replay, and
// future embedders.
func (c *Connections) GetForUser(ctx context.Context, u dbadmin.User, id dbadmin.ConnectionID) (dbadmin.Connection, error) {
	if u.ID == "" || id == "" {
		return dbadmin.Connection{}, dbadmin.ErrNotFound
	}
	row := c.store.DB.QueryRowContext(ctx, `
		SELECT c.id, c.name, c.engine, c.host, c.port, c.database, c.username,
		       c.tags, c.use_ssl, c.sslmode, c.ssh_tunnel_json, c.origin, c.owner,
		       c.created_at, c.updated_at
		FROM connections c
		JOIN connection_grants g ON g.connection_id = c.id
		WHERE c.id = ? AND g.user_id = ? AND g.role >= ?`,
		string(id), u.ID, int(dbadmin.RoleViewer))
	conn, err := scanConnection(row)
	if errors.Is(err, sql.ErrNoRows) {
		return dbadmin.Connection{}, dbadmin.ErrNotFound
	}
	if err != nil {
		return dbadmin.Connection{}, err
	}
	return conn, nil
}

// Credentials implements dbadmin.ConnectionStore.
func (c *Connections) Credentials(ctx context.Context, id dbadmin.ConnectionID) (dbadmin.Credentials, error) {
	row := c.store.DB.QueryRowContext(ctx, `SELECT creds_enc FROM connections WHERE id = ?`, string(id))
	var blob []byte
	if err := row.Scan(&blob); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return dbadmin.Credentials{}, dbadmin.ErrNotFound
		}
		return dbadmin.Credentials{}, err
	}
	pt, err := open(c.kek.Bytes(), blob, connAAD(string(id)))
	if err != nil {
		return dbadmin.Credentials{}, err
	}
	defer func() {
		for i := range pt {
			pt[i] = 0
		}
	}()
	var cp credsPlain
	if err := json.Unmarshal(pt, &cp); err != nil {
		return dbadmin.Credentials{}, err
	}
	// Defensive copy: the engine zeros bytes after use.
	out := dbadmin.Credentials{
		Password: cp.Password,
	}
	if len(cp.ClientCert) > 0 {
		out.ClientCert = append([]byte(nil), cp.ClientCert...)
	}
	if len(cp.ClientKey) > 0 {
		out.ClientKey = append([]byte(nil), cp.ClientKey...)
	}
	return out, nil
}

// Save implements dbadmin.ConnectionStore.
//
// Create-path is wrapped in a SQLite transaction so the INSERT into
// `connections` and the implicit owner-grant INSERT (when conn.Owner is
// set) either both succeed or both roll back. Previously a crash between
// the two left a connection row strand with no grant — invisible via
// List() but blocking the unique name (C2).
func (c *Connections) Save(ctx context.Context, conn dbadmin.Connection, creds dbadmin.Credentials) (dbadmin.ConnectionID, error) {
	if err := validateConnection(&conn); err != nil {
		return "", err
	}
	plain := credsPlain{
		Password:   creds.Password,
		ClientCert: creds.ClientCert,
		ClientKey:  creds.ClientKey,
	}
	raw, err := json.Marshal(plain)
	if err != nil {
		return "", err
	}

	tagsStr := serializeConnectionTags(conn.Tags)
	tunnelJSON := ""
	if conn.SSHTunnel != nil {
		b, jerr := json.Marshal(conn.SSHTunnel)
		if jerr != nil {
			// Zero the plaintext we no longer need.
			for i := range raw {
				raw[i] = 0
			}
			return "", jerr
		}
		tunnelJSON = string(b)
	}
	now := c.clock().UnixNano()

	if conn.ID == "" {
		conn.ID = dbadmin.ConnectionID(NewULID())
		// Seal AFTER the ID is minted so the AAD binds the ciphertext
		// to the row it lives in (SEC-04).
		enc, sealErr := seal(c.kek.Bytes(), raw, connAAD(string(conn.ID)))
		for i := range raw {
			raw[i] = 0
		}
		if sealErr != nil {
			return "", sealErr
		}
		origin := string(conn.Origin)
		if origin == "" {
			origin = string(dbadmin.OriginManual)
		}
		tx, terr := c.store.DB.BeginTx(ctx, nil)
		if terr != nil {
			return "", terr
		}
		defer func() {
			if err != nil {
				_ = tx.Rollback()
			}
		}()
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO connections (id, name, engine, host, port, database, username,
			                         creds_enc, tags, use_ssl, sslmode, ssh_tunnel_json,
			                         origin, owner, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			string(conn.ID), conn.Name, int(conn.Engine), conn.Host, conn.Port,
			conn.Database, conn.Username, enc, tagsStr,
			boolToInt(conn.UseSSL), conn.SSLMode, tunnelJSON,
			origin, conn.Owner, now, now); err != nil {
			if isUniqueViolation(err) {
				return "", dbadmin.ErrConflict
			}
			return "", err
		}
		// Atomic owner grant. If conn.Owner is empty we skip — caller
		// (e.g. system bootstrap) is responsible for granting manually.
		if conn.Owner != "" {
			if _, err = tx.ExecContext(ctx, `
				INSERT INTO connection_grants (user_id, connection_id, role, granted_by, granted_at)
				VALUES (?, ?, ?, ?, ?)
				ON CONFLICT(user_id, connection_id) DO UPDATE SET role=excluded.role, granted_by=excluded.granted_by, granted_at=excluded.granted_at`,
				conn.Owner, string(conn.ID), int(dbadmin.RoleOwner), conn.Owner, now); err != nil {
				return "", fmt.Errorf("standalone: save: owner grant: %w", err)
			}
		}
		if err = tx.Commit(); err != nil {
			return "", err
		}
		return conn.ID, nil
	}

	// Update path.
	enc, err := seal(c.kek.Bytes(), raw, connAAD(string(conn.ID)))
	for i := range raw {
		raw[i] = 0
	}
	if err != nil {
		return "", err
	}
	var existsOrigin string
	row := c.store.DB.QueryRowContext(ctx, `SELECT origin FROM connections WHERE id = ?`, string(conn.ID))
	if err := row.Scan(&existsOrigin); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", dbadmin.ErrNotFound
		}
		return "", err
	}
	if existsOrigin == string(dbadmin.OriginPanelSite) {
		return "", dbadmin.ErrConflict
	}
	res, err := c.store.DB.ExecContext(ctx, `
		UPDATE connections SET name=?, engine=?, host=?, port=?, database=?, username=?,
		                       creds_enc=?, tags=?, use_ssl=?, sslmode=?, ssh_tunnel_json=?, updated_at=?
		WHERE id = ?`,
		conn.Name, int(conn.Engine), conn.Host, conn.Port, conn.Database, conn.Username,
		enc, tagsStr, boolToInt(conn.UseSSL), conn.SSLMode, tunnelJSON, now,
		string(conn.ID))
	if err != nil {
		if isUniqueViolation(err) {
			return "", dbadmin.ErrConflict
		}
		return "", err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return "", dbadmin.ErrNotFound
	}
	return conn.ID, nil
}

// Delete implements dbadmin.ConnectionStore.
func (c *Connections) Delete(ctx context.Context, id dbadmin.ConnectionID) error {
	tx, err := c.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	var origin string
	if err = tx.QueryRowContext(ctx, `SELECT origin FROM connections WHERE id = ?`, string(id)).Scan(&origin); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return dbadmin.ErrNotFound
		}
		return err
	}
	if origin == string(dbadmin.OriginPanelSite) {
		err = dbadmin.ErrConflict
		return err
	}
	// CASCADE handles grants.
	if _, err = tx.ExecContext(ctx, `DELETE FROM connections WHERE id = ?`, string(id)); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

// Grant inserts or updates a (user, connection, role) row. granter is
// the user_id performing the action — used for audit.
//
// Returns dbadmin.ErrNotFound only when the user or connection genuinely
// doesn't exist (verified via pre-check inside the same tx). Other DB
// errors (SQLITE_BUSY, disk-full, ctx cancellation, schema mismatch)
// surface as wrapped errors so operators get the right remediation
// signal (C3).
func (c *Connections) Grant(ctx context.Context, granter, userID string, connID dbadmin.ConnectionID, role dbadmin.Role) error {
	if role == dbadmin.RoleNone {
		return c.Revoke(ctx, granter, userID, connID)
	}
	now := c.clock().UnixNano()
	tx, err := c.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("standalone: grant: begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var one int
	if err = tx.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id = ?`, userID).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = dbadmin.ErrNotFound
			return err
		}
		return fmt.Errorf("standalone: grant: lookup user: %w", err)
	}
	if err = tx.QueryRowContext(ctx, `SELECT 1 FROM connections WHERE id = ?`, string(connID)).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = dbadmin.ErrNotFound
			return err
		}
		return fmt.Errorf("standalone: grant: lookup connection: %w", err)
	}

	if _, err = tx.ExecContext(ctx, `
		INSERT INTO connection_grants (user_id, connection_id, role, granted_by, granted_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(user_id, connection_id) DO UPDATE SET role=excluded.role, granted_by=excluded.granted_by, granted_at=excluded.granted_at`,
		userID, string(connID), int(role), granter, now); err != nil {
		return fmt.Errorf("standalone: grant: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("standalone: grant: commit: %w", err)
	}
	return nil
}

// Revoke removes a (user, connection) grant. Returns ErrConflict if the
// removal would leave the connection without any RoleOwner.
func (c *Connections) Revoke(ctx context.Context, _ string, userID string, connID dbadmin.ConnectionID) error {
	tx, err := c.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var currentRole int
	row := tx.QueryRowContext(ctx, `SELECT role FROM connection_grants WHERE user_id = ? AND connection_id = ?`,
		userID, string(connID))
	if err = row.Scan(&currentRole); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return dbadmin.ErrNotFound
		}
		return err
	}
	if dbadmin.Role(currentRole) == dbadmin.RoleOwner {
		var ownerCount int
		if err = tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM connection_grants WHERE connection_id = ? AND role = ?`,
			string(connID), int(dbadmin.RoleOwner)).Scan(&ownerCount); err != nil {
			return err
		}
		if ownerCount <= 1 {
			err = dbadmin.ErrConflict
			return err
		}
	}
	if _, err = tx.ExecContext(ctx,
		`DELETE FROM connection_grants WHERE user_id = ? AND connection_id = ?`,
		userID, string(connID)); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

// GrantRow is the public shape returned by ListGrants.
type GrantRow struct {
	UserID       string
	ConnectionID dbadmin.ConnectionID
	Role         dbadmin.Role
	GrantedBy    string
	GrantedAt    time.Time
}

// ListGrants enumerates grants for a connection.
func (c *Connections) ListGrants(ctx context.Context, connID dbadmin.ConnectionID) ([]GrantRow, error) {
	rows, err := c.store.DB.QueryContext(ctx, `
		SELECT user_id, connection_id, role, granted_by, granted_at
		FROM connection_grants WHERE connection_id = ?`, string(connID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GrantRow{}
	for rows.Next() {
		var g GrantRow
		var connStr string
		var roleInt int
		var grantedAtNs int64
		if err := rows.Scan(&g.UserID, &connStr, &roleInt, &g.GrantedBy, &grantedAtNs); err != nil {
			return nil, err
		}
		g.ConnectionID = dbadmin.ConnectionID(connStr)
		g.Role = dbadmin.Role(roleInt)
		g.GrantedAt = time.Unix(0, grantedAtNs).UTC()
		out = append(out, g)
	}
	return out, rows.Err()
}

// ─── helpers ────────────────────────────────────────────────────────────

type connectionScanner interface {
	Scan(dest ...any) error
}

func scanConnection(row connectionScanner) (dbadmin.Connection, error) {
	var (
		conn        dbadmin.Connection
		idStr       string
		engineInt   int
		tagsStr     string
		useSSLInt   int
		tunnelJSON  string
		origin      string
		createdNs   int64
		updatedNs   int64
	)
	if err := row.Scan(&idStr, &conn.Name, &engineInt, &conn.Host, &conn.Port,
		&conn.Database, &conn.Username, &tagsStr, &useSSLInt, &conn.SSLMode,
		&tunnelJSON, &origin, &conn.Owner, &createdNs, &updatedNs); err != nil {
		return dbadmin.Connection{}, err
	}
	conn.ID = dbadmin.ConnectionID(idStr)
	conn.Engine = dbadmin.EngineKind(engineInt)
	conn.Tags = deserializeConnectionTags(tagsStr)
	conn.UseSSL = useSSLInt != 0
	conn.Origin = dbadmin.Origin(origin)
	conn.CreatedAt = time.Unix(0, createdNs).UTC()
	conn.UpdatedAt = time.Unix(0, updatedNs).UTC()
	if tunnelJSON != "" {
		var st dbadmin.SSHTunnel
		if err := json.Unmarshal([]byte(tunnelJSON), &st); err == nil {
			conn.SSHTunnel = &st
		}
	}
	return conn, nil
}

func validateConnection(c *dbadmin.Connection) error {
	if c.Name == "" {
		return fmt.Errorf("%w: connection name required", dbadmin.ErrInvalidInput)
	}
	if strings.Contains(c.Name, ",") {
		// Connection names live in audit logs and grants; a comma
		// would alias against our tag fencing scheme.
		return fmt.Errorf("%w: connection name must not contain ','", dbadmin.ErrInvalidInput)
	}
	if c.Engine != dbadmin.EngineMariaDB && c.Engine != dbadmin.EnginePostgres {
		return fmt.Errorf("%w: invalid engine %d", dbadmin.ErrInvalidInput, c.Engine)
	}
	if c.Host == "" {
		return fmt.Errorf("%w: host required", dbadmin.ErrInvalidInput)
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("%w: invalid port %d", dbadmin.ErrInvalidInput, c.Port)
	}
	return nil
}

func serializeConnectionTags(tags []dbadmin.Tag) string {
	if len(tags) == 0 {
		return ""
	}
	parts := make([]string, 0, len(tags))
	for _, t := range tags {
		s := strings.TrimSpace(string(t))
		if s == "" {
			continue
		}
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return ""
	}
	return "," + strings.Join(parts, ",") + ","
}

func deserializeConnectionTags(s string) []dbadmin.Tag {
	if s == "" {
		return nil
	}
	s = strings.TrimPrefix(s, ",")
	s = strings.TrimSuffix(s, ",")
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]dbadmin.Tag, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, dbadmin.Tag(p))
		}
	}
	return out
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "SQLITE_CONSTRAINT")
}

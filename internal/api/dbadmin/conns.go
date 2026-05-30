package dbadmin

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/auracp/auracp/internal/secret"
	"github.com/auracp/auracp/internal/store"
	"github.com/auracp/auracp/pkg/dbadmin"
)

// CredsAAD is the additional-authenticated-data label bound to every
// encrypted aura_db_connections.creds_enc blob. Sharing the panel KEK
// with other encrypted-at-rest tables (Cloudflare token, site DB
// passwords) is fine ONLY so long as a ciphertext minted under one
// label cannot be replayed against another. The AAD enforces that.
//
// PR #10.5 / FIX-PD-SEC-03: previously credBlob ciphertext was produced
// via box.Encrypt — same KEK as panel db.go's password encryption. If
// an attacker could write arbitrary bytes into aura_db_connections.creds_enc
// by replaying a value scraped from databases.password_enc, the dbadmin
// adapter would happily decrypt the stolen password and surface it to
// any RoleOwner on that connection. Binding the AAD to the table+column
// stops that cross-table replay.
const CredsAAD = "dbadmin:creds:v1"

// panelConns implements dbadmin.ConnectionStore against the panel's
// SQLite + secret.Box. Connections + grants live in aura_db_* tables
// (see migrate.go); credentials are encrypted with the panel's NaCl
// secretbox key.
type panelConns struct {
	st  *store.Store
	box *secret.Box
}

func newPanelConns(st *store.Store, box *secret.Box) *panelConns {
	return &panelConns{st: st, box: box}
}

// credBlob is the JSON shape stored (encrypted) in aura_db_connections.creds_enc.
type credBlob struct {
	Password   string `json:"password,omitempty"`
	ClientCert []byte `json:"client_cert,omitempty"`
	ClientKey  []byte `json:"client_key,omitempty"`
}

// RolesFor returns the user's per-connection role map. ROLE_ADMIN gets
// an implicit RoleOwner on every connection without needing a row in
// aura_db_grants.
//
// PR #10.5 / FIX-INT-14 (also addresses SDK-6 duplicate): ROLE_ADMIN
// previously ran allIDs() — a full SELECT id FROM aura_db_connections
// scan — on every request just to mint per-id RoleOwner entries that
// HasPermission would have short-circuited anyway. With N connections
// and M admin requests/s the cost was N*M scans per second. We now
// return nil for admins; HasPermission's ROLE_ADMIN branch (auth.go)
// is the authoritative short-circuit and never consults the map.
//
// FIX-C3: non-admin rows where role = RoleNone (a defensive grant left
// behind by a buggy revoke) used to leak into the result map and could
// match against HasPermission's `have, ok := u.Roles[cid]` check.
// Filter them out at the SQL boundary so the in-memory map only
// contains actionable grants.
func (c *panelConns) RolesFor(ctx context.Context, panelUserID int64, panelRole string) (map[dbadmin.ConnectionID]dbadmin.Role, error) {
	if panelRole == "ROLE_ADMIN" {
		// FIX-INT-14: nil map is fine — HasPermission's admin branch
		// is the authoritative gate; List's admin SQL branch returns
		// everything; nothing else reads u.Roles for an admin user.
		return nil, nil
	}
	rows, err := c.st.DB.QueryContext(ctx,
		`SELECT connection_id, role FROM aura_db_grants WHERE user_id = ? AND role > ?`,
		strconv.FormatInt(panelUserID, 10), int(dbadmin.RoleNone))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[dbadmin.ConnectionID]dbadmin.Role{}
	for rows.Next() {
		var (
			cid  string
			role int
		)
		if err := rows.Scan(&cid, &role); err != nil {
			return nil, err
		}
		out[dbadmin.ConnectionID(cid)] = dbadmin.Role(role)
	}
	return out, rows.Err()
}

func (c *panelConns) allIDs(ctx context.Context) ([]dbadmin.ConnectionID, error) {
	rows, err := c.st.DB.QueryContext(ctx, `SELECT id FROM aura_db_connections`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dbadmin.ConnectionID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, dbadmin.ConnectionID(id))
	}
	return out, rows.Err()
}

// List returns connections the user has any grant on. Filtering is
// authoritative (engine does not re-filter). ROLE_ADMIN sees all.
func (c *panelConns) List(ctx context.Context, u dbadmin.User) ([]dbadmin.Connection, error) {
	isAdmin := u.Attrs != nil && u.Attrs["role"] == "ROLE_ADMIN"
	var (
		rows *sql.Rows
		err  error
	)
	if isAdmin {
		rows, err = c.st.DB.QueryContext(ctx,
			`SELECT id,name,engine,host,port,database,username,tags,use_ssl,sslmode,ssh_tunnel_json,origin,owner,created_at,updated_at FROM aura_db_connections ORDER BY name`)
	} else {
		rows, err = c.st.DB.QueryContext(ctx, `
			SELECT cn.id,cn.name,cn.engine,cn.host,cn.port,cn.database,cn.username,cn.tags,cn.use_ssl,cn.sslmode,cn.ssh_tunnel_json,cn.origin,cn.owner,cn.created_at,cn.updated_at
			FROM aura_db_connections cn
			INNER JOIN aura_db_grants g ON g.connection_id = cn.id
			WHERE g.user_id = ? AND g.role >= ?
			ORDER BY cn.name`, u.ID, int(dbadmin.RoleViewer))
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []dbadmin.Connection{}
	for rows.Next() {
		conn, err := scanConn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, conn)
	}
	return out, rows.Err()
}

// Get fetches a single connection. Returns dbadmin.ErrNotFound if absent.
func (c *panelConns) Get(ctx context.Context, id dbadmin.ConnectionID) (dbadmin.Connection, error) {
	row := c.st.DB.QueryRowContext(ctx,
		`SELECT id,name,engine,host,port,database,username,tags,use_ssl,sslmode,ssh_tunnel_json,origin,owner,created_at,updated_at FROM aura_db_connections WHERE id = ?`,
		string(id))
	conn, err := scanConn(row)
	if errors.Is(err, sql.ErrNoRows) {
		return dbadmin.Connection{}, dbadmin.ErrNotFound
	}
	return conn, err
}

// Credentials returns decrypted credentials. Returns dbadmin.ErrNotFound
// if the connection does not exist.
//
// PR #10.5 / FIX-PD-SEC-03: decryption attempts the AAD-bound path
// first (CredsAAD); on failure, falls back to the legacy box.Decrypt
// for ciphertext written by pre-PR-#10.5 builds. The fallback path
// rewrites the row in-place under the new AAD so subsequent reads take
// the fast path and any same-KEK replay attack is closed once the
// connection has been touched once.
func (c *panelConns) Credentials(ctx context.Context, id dbadmin.ConnectionID) (dbadmin.Credentials, error) {
	var enc string
	err := c.st.DB.QueryRowContext(ctx,
		`SELECT creds_enc FROM aura_db_connections WHERE id = ?`, string(id)).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return dbadmin.Credentials{}, dbadmin.ErrNotFound
	}
	if err != nil {
		return dbadmin.Credentials{}, err
	}
	plain, derr := c.box.DecryptAAD(enc, CredsAAD)
	if derr != nil {
		// Legacy ciphertext (pre-PR-#10.5): fall back to the AAD-less
		// path. We don't surface the AAD error to the caller — a
		// connection was written before AAD existed; decrypting it
		// successfully is the expected migration path. On success,
		// re-encrypt under AAD and write back so the next read hits
		// the fast path.
		legacyPlain, legacyErr := c.box.Decrypt(enc)
		if legacyErr != nil {
			return dbadmin.Credentials{}, legacyErr
		}
		plain = legacyPlain
		if reEnc, rerr := c.box.EncryptAAD(plain, CredsAAD); rerr == nil {
			_, _ = c.st.DB.ExecContext(ctx,
				`UPDATE aura_db_connections SET creds_enc = ? WHERE id = ? AND creds_enc = ?`,
				reEnc, string(id), enc)
		}
	}
	var blob credBlob
	if err := json.Unmarshal([]byte(plain), &blob); err != nil {
		return dbadmin.Credentials{}, err
	}
	out := dbadmin.Credentials{Password: blob.Password}
	if len(blob.ClientCert) > 0 {
		out.ClientCert = append([]byte(nil), blob.ClientCert...)
	}
	if len(blob.ClientKey) > 0 {
		out.ClientKey = append([]byte(nil), blob.ClientKey...)
	}
	return out, nil
}

// Save creates or updates a connection + (on create) auto-grants the
// creator RoleOwner. The creator ID rides on Connection.Owner (the engine
// stamps it from the User.ID).
func (c *panelConns) Save(ctx context.Context, conn dbadmin.Connection, creds dbadmin.Credentials) (dbadmin.ConnectionID, error) {
	blob := credBlob{
		Password:   creds.Password,
		ClientCert: creds.ClientCert,
		ClientKey:  creds.ClientKey,
	}
	plain, err := json.Marshal(&blob)
	if err != nil {
		return "", err
	}
	// FIX-PD-SEC-03: bind ciphertext to a "dbadmin:creds:v1" label so
	// it cannot be silently cross-decrypted as panel-DB-password
	// material (and vice versa) under a same-KEK attack.
	enc, err := c.box.EncryptAAD(string(plain), CredsAAD)
	if err != nil {
		return "", err
	}
	tags := encodeTags(conn.Tags)
	sshJSON := ""
	if conn.SSHTunnel != nil {
		b, err := json.Marshal(conn.SSHTunnel)
		if err != nil {
			return "", err
		}
		sshJSON = string(b)
	}
	origin := string(conn.Origin)
	if origin == "" {
		origin = string(dbadmin.OriginManual)
	}
	now := time.Now().Unix()

	tx, err := c.st.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if conn.ID == "" {
		id, err := newConnectionID()
		if err != nil {
			return "", err
		}
		conn.ID = dbadmin.ConnectionID(id)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO aura_db_connections
			(id,name,engine,host,port,database,username,creds_enc,tags,use_ssl,sslmode,ssh_tunnel_json,origin,owner,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			string(conn.ID), conn.Name, int(conn.Engine), conn.Host, conn.Port,
			conn.Database, conn.Username, enc, tags, b2i(conn.UseSSL), conn.SSLMode,
			sshJSON, origin, conn.Owner, now, now,
		); err != nil {
			// FIX-C7: SQLite returns "UNIQUE constraint failed:
			// aura_db_connections.name" on duplicate name. Surfacing
			// the raw string makes the engine map it to 500 (unknown
			// error) when the right answer is 409 (conflict). Map it
			// to the typed sentinel so the SPA can show "a connection
			// named X already exists" instead of "internal error".
			return "", mapSaveErr(err)
		}
		// Auto-grant RoleOwner to the creator (non-empty Owner). The
		// engine sets Owner from User.ID before invoking Save.
		if conn.Owner != "" {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO aura_db_grants (user_id,connection_id,role,granted_by,granted_at)
				VALUES (?,?,?,?,?)
				ON CONFLICT(user_id, connection_id) DO UPDATE SET role = excluded.role`,
				conn.Owner, string(conn.ID), int(dbadmin.RoleOwner), conn.Owner, now,
			); err != nil {
				return "", err
			}
		}
	} else {
		// Update path. Existence check is implicit via affected rows.
		res, err := tx.ExecContext(ctx, `
			UPDATE aura_db_connections SET
				name=?,engine=?,host=?,port=?,database=?,username=?,creds_enc=?,tags=?,use_ssl=?,sslmode=?,ssh_tunnel_json=?,origin=?,owner=?,updated_at=?
			WHERE id=?`,
			conn.Name, int(conn.Engine), conn.Host, conn.Port,
			conn.Database, conn.Username, enc, tags, b2i(conn.UseSSL), conn.SSLMode,
			sshJSON, origin, conn.Owner, now, string(conn.ID))
		if err != nil {
			return "", mapSaveErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return "", dbadmin.ErrNotFound
		}
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}
	committed = true
	return conn.ID, nil
}

// Delete cascades grants via the ON DELETE CASCADE constraint.
//
// PR #10.5 / FIX-SDK-4: previously the connection row + cascaded grant
// rows + (in some SQLite builds) the FK-driven panel_user grant FK
// were all relying on SQLite's implicit per-statement transaction.
// That worked, but a future addition (e.g. tombstoning the conn into a
// soft-delete table, or emitting an audit row at delete time) would
// have silently split the operation across two implicit txs. Wrap the
// whole delete in an explicit BEGIN/COMMIT so future extensions
// inherit atomicity by default.
func (c *panelConns) Delete(ctx context.Context, id dbadmin.ConnectionID) error {
	tx, err := c.st.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	res, err := tx.ExecContext(ctx, `DELETE FROM aura_db_connections WHERE id = ?`, string(id))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return dbadmin.ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// mapSaveErr translates raw SQLite error strings into the typed sentinels
// the engine knows how to convert into HTTP status codes. SQLite emits
// the constraint message verbatim ("UNIQUE constraint failed:
// aura_db_connections.name"); without this mapping the engine treats
// it as opaque internal error → HTTP 500. PR #10.5 / FIX-C7.
func mapSaveErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "UNIQUE constraint failed") {
		return dbadmin.ErrConflict
	}
	return err
}

// Grant inserts or updates an explicit per-connection role grant. Used by
// the dbadmin engine via the ConnGrantMgmt route. Exposed as a method on
// the adapter so tests can drive it directly.
func (c *panelConns) Grant(ctx context.Context, panelUserID, connectionID, grantedBy string, role dbadmin.Role) error {
	_, err := c.st.DB.ExecContext(ctx, `
		INSERT INTO aura_db_grants (user_id,connection_id,role,granted_by,granted_at)
		VALUES (?,?,?,?,?)
		ON CONFLICT(user_id, connection_id) DO UPDATE SET role = excluded.role`,
		panelUserID, connectionID, int(role), grantedBy, time.Now().Unix())
	return err
}

// TableGrantRow aliases dbadmin.TableGrant so the panel adapter speaks
// the same shape as standalone. v0.3.2-B.
type TableGrantRow = dbadmin.TableGrant

// GrantTable upserts a per-table grant into aura_db_table_grants.
// role == RoleNone delegates to RevokeTable. v0.3.2-B.
//
// Signature matches standalone.Connections.GrantTable so the engine's
// TableGrantStore interface can hold either backend by value.
func (c *panelConns) GrantTable(ctx context.Context, granter, userID string, connID dbadmin.ConnectionID, schema, table string, role dbadmin.Role) error {
	if table == "" {
		return dbadmin.ErrInvalidInput
	}
	if role == dbadmin.RoleNone {
		return c.RevokeTable(ctx, granter, userID, connID, schema, table)
	}
	_, err := c.st.DB.ExecContext(ctx, `
		INSERT INTO aura_db_table_grants (user_id, connection_id, schema_name, table_name, role, granted_by, granted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, connection_id, schema_name, table_name) DO UPDATE SET
			role = excluded.role,
			granted_by = excluded.granted_by,
			granted_at = excluded.granted_at`,
		userID, string(connID), schema, table, int(role), granter, time.Now().Unix())
	return err
}

// RevokeTable deletes a single per-table grant. Returns ErrNotFound
// when no matching row exists. v0.3.2-B.
func (c *panelConns) RevokeTable(ctx context.Context, _ string, userID string, connID dbadmin.ConnectionID, schema, table string) error {
	res, err := c.st.DB.ExecContext(ctx, `
		DELETE FROM aura_db_table_grants
		WHERE user_id = ? AND connection_id = ? AND schema_name = ? AND table_name = ?`,
		userID, string(connID), schema, table)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return dbadmin.ErrNotFound
	}
	return nil
}

// ListTableGrants returns per-table grants on a connection, optionally
// filtered by user. v0.3.2-B.
func (c *panelConns) ListTableGrants(ctx context.Context, connID dbadmin.ConnectionID, userID string) ([]TableGrantRow, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if userID == "" {
		rows, err = c.st.DB.QueryContext(ctx, `
			SELECT user_id, connection_id, schema_name, table_name, role, granted_by, granted_at
			FROM aura_db_table_grants WHERE connection_id = ?`, string(connID))
	} else {
		rows, err = c.st.DB.QueryContext(ctx, `
			SELECT user_id, connection_id, schema_name, table_name, role, granted_by, granted_at
			FROM aura_db_table_grants WHERE connection_id = ? AND user_id = ?`, string(connID), userID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TableGrantRow{}
	for rows.Next() {
		var (
			g         TableGrantRow
			connStr   string
			roleInt   int
			grantedAt int64
		)
		if err := rows.Scan(&g.UserID, &connStr, &g.Schema, &g.Table, &roleInt, &g.GrantedBy, &grantedAt); err != nil {
			return nil, err
		}
		g.ConnectionID = dbadmin.ConnectionID(connStr)
		g.Role = dbadmin.Role(roleInt)
		g.GrantedAt = time.Unix(grantedAt, 0).UTC()
		out = append(out, g)
	}
	return out, rows.Err()
}

// ---- helpers ----

func newConnectionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "adc_" + hex.EncodeToString(b[:]), nil
}

func encodeTags(tags []dbadmin.Tag) string {
	if len(tags) == 0 {
		return ""
	}
	out := make([]string, len(tags))
	for i, t := range tags {
		out[i] = string(t)
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func decodeTags(s string) []dbadmin.Tag {
	if s == "" {
		return nil
	}
	var raw []string
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil
	}
	out := make([]dbadmin.Tag, len(raw))
	for i, r := range raw {
		out[i] = dbadmin.Tag(r)
	}
	return out
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func scanConn(s interface{ Scan(...any) error }) (dbadmin.Connection, error) {
	var (
		id, name, host, database, username, tags, sslmode, sshJSON, origin, owner string
		engine, port, useSSL                                                       int
		createdAt, updatedAt                                                       int64
	)
	if err := s.Scan(&id, &name, &engine, &host, &port, &database, &username, &tags, &useSSL, &sslmode, &sshJSON, &origin, &owner, &createdAt, &updatedAt); err != nil {
		return dbadmin.Connection{}, err
	}
	c := dbadmin.Connection{
		ID:        dbadmin.ConnectionID(id),
		Name:      name,
		Engine:    dbadmin.EngineKind(engine),
		Host:      host,
		Port:      port,
		Database:  database,
		Username:  username,
		Tags:      decodeTags(tags),
		UseSSL:    useSSL != 0,
		SSLMode:   sslmode,
		Origin:    dbadmin.Origin(origin),
		Owner:     owner,
		CreatedAt: time.Unix(createdAt, 0).UTC(),
		UpdatedAt: time.Unix(updatedAt, 0).UTC(),
	}
	if sshJSON != "" {
		var t dbadmin.SSHTunnel
		if err := json.Unmarshal([]byte(sshJSON), &t); err == nil {
			c.SSHTunnel = &t
		}
	}
	return c, nil
}

// Compile-time interface assertion.
var _ dbadmin.ConnectionStore = (*panelConns)(nil)

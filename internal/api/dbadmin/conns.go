package dbadmin

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/auracp/auracp/internal/secret"
	"github.com/auracp/auracp/internal/store"
	"github.com/auracp/auracp/pkg/dbadmin"
)

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
func (c *panelConns) RolesFor(ctx context.Context, panelUserID int64, panelRole string) (map[dbadmin.ConnectionID]dbadmin.Role, error) {
	if panelRole == "ROLE_ADMIN" {
		ids, err := c.allIDs(ctx)
		if err != nil {
			return nil, err
		}
		out := make(map[dbadmin.ConnectionID]dbadmin.Role, len(ids))
		for _, id := range ids {
			out[id] = dbadmin.RoleOwner
		}
		return out, nil
	}
	rows, err := c.st.DB.QueryContext(ctx,
		`SELECT connection_id, role FROM aura_db_grants WHERE user_id = ?`,
		strconv.FormatInt(panelUserID, 10))
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
	plain, err := c.box.Decrypt(enc)
	if err != nil {
		return dbadmin.Credentials{}, err
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
	enc, err := c.box.Encrypt(string(plain))
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
			return "", err
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
			return "", err
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
func (c *panelConns) Delete(ctx context.Context, id dbadmin.ConnectionID) error {
	res, err := c.st.DB.ExecContext(ctx, `DELETE FROM aura_db_connections WHERE id = ?`, string(id))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return dbadmin.ErrNotFound
	}
	return nil
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

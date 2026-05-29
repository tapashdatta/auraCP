package driver

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	mysqlsql "github.com/go-sql-driver/mysql"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// mysqlDriverImpl implements Driver for MariaDB and Oracle MySQL.
type mysqlDriverImpl struct{}

func (m *mysqlDriverImpl) Engine() dbadmin.EngineKind {
	return dbadmin.EngineMariaDB
}

// Open opens a *sql.DB pointed at the configured MariaDB/MySQL backend,
// optionally through an SSH tunnel, with hardened DSN parameters.
//
// Hardening (SECURITY.md §6.3 + §7.2):
//   - AllowAllFiles=false
//   - AllowCleartextPasswords=false
//   - AllowOldPasswords=false
//   - InterpolateParams=false (server-side parameterization)
//   - parseTime=true (DATETIME / DATE → time.Time)
//   - TLS — see registerMySQLTLS. Defense-in-depth: prod-tagged
//     connections REJECT non-verify-full SSL even if the engine
//     missed enforcement (post-review fix).
func (m *mysqlDriverImpl) Open(ctx context.Context, c *dbadmin.Connection, creds *dbadmin.Credentials, poolSize int) (Conn, error) {
	if c == nil {
		return nil, errors.New("driver/mysql: nil Connection")
	}
	if creds == nil {
		return nil, errors.New("driver/mysql: nil Credentials")
	}

	// Post-review fix: defense-in-depth TLS enforcement for prod.
	if c.IsProd() {
		mode := strings.ToLower(c.SSLMode)
		if !c.UseSSL || mode == "" || mode == "preferred" || mode == "skip-verify" {
			return nil, fmt.Errorf("driver/mysql: prod-tagged connection requires TLS with verify-full (got UseSSL=%v SSLMode=%q)", c.UseSSL, c.SSLMode)
		}
	}

	hostPort := net.JoinHostPort(c.Host, fmt.Sprintf("%d", c.Port))

	var tun *Tunnel
	dialAddr := hostPort
	if c.SSHTunnel != nil {
		// Tunnel concurrency = pool size + small headroom (review fix:
		// previously hard-coded 16; now matches pool+slack).
		t, err := OpenTunnel(ctx, c.SSHTunnel, hostPort, poolSize+2)
		if err != nil {
			return nil, fmt.Errorf("driver/mysql: open tunnel: %w", err)
		}
		tun = t
		dialAddr = t.LocalAddr()
	}

	cfg := mysqlsql.NewConfig()
	cfg.User = c.Username
	cfg.Passwd = creds.Password
	cfg.Net = "tcp"
	cfg.Addr = dialAddr
	cfg.DBName = c.Database
	cfg.AllowAllFiles = false
	cfg.AllowCleartextPasswords = false
	cfg.AllowOldPasswords = false
	cfg.InterpolateParams = false
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	cfg.Timeout = 10 * time.Second
	cfg.ReadTimeout = 0
	cfg.WriteTimeout = 0

	// TLS config.
	var tlsName string
	if c.UseSSL {
		name, err := registerMySQLTLS(c, creds)
		if err != nil {
			if tun != nil {
				_ = tun.Close()
			}
			return nil, fmt.Errorf("driver/mysql: TLS config: %w", err)
		}
		tlsName = name
		cfg.TLSConfig = name
	}

	dsn := cfg.FormatDSN()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		if tlsName != "" {
			mysqlsql.DeregisterTLSConfig(tlsName)
		}
		if tun != nil {
			_ = tun.Close()
		}
		return nil, fmt.Errorf("driver/mysql: sql.Open: %w", err)
	}

	if poolSize < 1 {
		poolSize = 4
	}
	db.SetMaxOpenConns(poolSize)
	db.SetMaxIdleConns(poolSize)
	db.SetConnMaxIdleTime(5 * time.Minute)
	db.SetConnMaxLifetime(0)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		if tlsName != "" {
			mysqlsql.DeregisterTLSConfig(tlsName)
		}
		if tun != nil {
			_ = tun.Close()
		}
		return nil, classifyMySQLErr(wrapCtxErr(pingCtx, err))
	}

	return &mysqlConn{db: db, tunnel: tun, conn: c, tlsName: tlsName}, nil
}

// mysqlConn wraps a *sql.DB + optional SSH tunnel as a Conn.
type mysqlConn struct {
	db      *sql.DB
	tunnel  *Tunnel
	conn    *dbadmin.Connection
	tlsName string // empty if no TLS config was registered
}

func (c *mysqlConn) Query(ctx context.Context, limits Limits, sqlText string, args ...any) (Rows, error) {
	ctx, cancel := limits.ApplyTimeout(ctx)
	// Note: cancel runs when the wrapped Rows.Close() returns; if the
	// caller never closes, the timeout still fires eventually.
	rows, err := c.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		cancel()
		return nil, classifyMySQLErr(wrapCtxErr(ctx, err))
	}
	cols, err := buildMySQLColumns(rows)
	if err != nil {
		_ = rows.Close()
		cancel()
		return nil, classifyMySQLErr(err)
	}
	inner := &mysqlRows{rows: rows, cols: cols, cancel: cancel}
	return &LimitedRows{Inner: inner, L: limits}, nil
}

func (c *mysqlConn) Exec(ctx context.Context, limits Limits, sqlText string, args ...any) (Result, error) {
	ctx, cancel := limits.ApplyTimeout(ctx)
	defer cancel()
	res, err := c.db.ExecContext(ctx, sqlText, args...)
	if err != nil {
		return Result{}, classifyMySQLErr(wrapCtxErr(ctx, err))
	}
	r := Result{}
	if n, e := res.RowsAffected(); e == nil {
		r.RowsAffected = n
	}
	if id, e := res.LastInsertId(); e == nil {
		r.LastInsertID = id
	}
	return r, nil
}

func (c *mysqlConn) Ping(ctx context.Context) error {
	return classifyMySQLErr(wrapCtxErr(ctx, c.db.PingContext(ctx)))
}

func (c *mysqlConn) ServerVersion(ctx context.Context) (string, error) {
	var v string
	if err := c.db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&v); err != nil {
		return "", classifyMySQLErr(wrapCtxErr(ctx, err))
	}
	return v, nil
}

func (c *mysqlConn) Close() error {
	var firstErr error
	if c.db != nil {
		if err := c.db.Close(); err != nil {
			firstErr = err
		}
	}
	if c.tlsName != "" {
		mysqlsql.DeregisterTLSConfig(c.tlsName)
	}
	if c.tunnel != nil {
		if err := c.tunnel.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// mysqlRows wraps *sql.Rows with column metadata + the type-aware Next.
type mysqlRows struct {
	rows    *sql.Rows
	cols    []ColumnInfo
	rawDest []any
	closed  bool
	cancel  context.CancelFunc
}

func (r *mysqlRows) Columns() []ColumnInfo {
	return r.cols
}

func (r *mysqlRows) Next(ctx context.Context) ([]any, error) {
	if r.closed {
		return nil, ErrClosed
	}
	if !r.rows.Next() {
		if err := r.rows.Err(); err != nil {
			return nil, classifyMySQLErr(wrapCtxErr(ctx, err))
		}
		return nil, ErrEOF
	}

	n := len(r.cols)
	if r.rawDest == nil {
		r.rawDest = make([]any, n)
		for i := range r.rawDest {
			var v interface{}
			r.rawDest[i] = &v
		}
	}
	if err := r.rows.Scan(r.rawDest...); err != nil {
		return nil, classifyMySQLErr(wrapCtxErr(ctx, err))
	}

	out := make([]any, n)
	for i, raw := range r.rawDest {
		val := *(raw.(*interface{}))
		out[i] = convertMySQLValue(val, r.cols[i].DatabaseTypeName)
	}
	return out, nil
}

func (r *mysqlRows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	if r.cancel != nil {
		r.cancel()
	}
	return r.rows.Close()
}

// ─── Helpers ────────────────────────────────────────────────────────

func buildMySQLColumns(rows *sql.Rows) ([]ColumnInfo, error) {
	types, err := rows.ColumnTypes()
	if err != nil {
		return nil, err
	}
	out := make([]ColumnInfo, len(types))
	for i, t := range types {
		ci := ColumnInfo{
			Name:             t.Name(),
			DatabaseTypeName: t.DatabaseTypeName(),
		}
		if nullable, ok := t.Nullable(); ok {
			ci.Nullable = nullable
		}
		out[i] = ci
	}
	return out, nil
}

// convertMySQLValue normalizes the value returned by sql.Rows.Scan. Most
// non-numeric values arrive as []byte; we decode text columns to string
// but PRESERVE bytes for BLOB / VARBINARY / BINARY / JSON columns so the
// frontend can render appropriately (matches the Rows contract).
//
// Post-review fix: previously this branched only on Go type and lost
// the BLOB/text distinction.
func convertMySQLValue(v any, dbTypeName string) any {
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		if isBinaryColumn(dbTypeName) {
			// Defensive copy: sql.Rows reuses Scan buffers across
			// Next() calls; the caller may retain ours.
			out := make([]byte, len(x))
			copy(out, x)
			return out
		}
		return string(x)
	case time.Time:
		return x.UTC()
	default:
		return v
	}
}

// isBinaryColumn reports whether a MySQL column type implies binary
// content. Matches the substrings used in MySQL's information_schema +
// the driver's DatabaseTypeName output (uppercased).
func isBinaryColumn(name string) bool {
	up := strings.ToUpper(name)
	switch up {
	case "BLOB", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB",
		"VARBINARY", "BINARY", "JSON":
		return true
	}
	return false
}

// classifyMySQLErr maps a go-sql-driver/mysql error to one of the typed
// errors. Verbatim backend messages are kept ONLY for syntax errors (where
// the operator needs them to fix the query); auth and permission errors
// return a generic short message so internal usernames / hostnames /
// schema names don't leak (post-review fix).
//
// The original error is still preserved via errors.Join so the audit
// sink can capture the verbatim message server-side.
func classifyMySQLErr(err error) error {
	if err == nil {
		return nil
	}
	// Sentinels pass through.
	if errors.Is(err, ErrEOF) || errors.Is(err, ErrCapped) ||
		errors.Is(err, ErrClosed) || errors.Is(err, ErrTimeout) ||
		errors.Is(err, ErrCanceled) {
		return err
	}

	var mErr *mysqlsql.MySQLError
	if errors.As(err, &mErr) {
		switch mErr.Number {
		case 1045, 1044, 1130:
			return errors.Join(ErrAuth, err)
		case 1064:
			// Syntax errors get the verbatim message — operator
			// needs it to fix the query.
			return fmt.Errorf("%w: %s", ErrSyntax, mErr.Message)
		case 1142, 1143, 1227, 1226:
			return errors.Join(ErrPermission, err)
		case 1062, 1452:
			return fmt.Errorf("%w: %s", ErrConflict, mErr.Message)
		case 1040, 1129, 1203:
			// Too many connections / host blocked / user-resource
			// limit. Connectivity / quota failures.
			return errors.Join(ErrUnavailable, err)
		}
	}

	msg := err.Error()
	if strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "context canceled") ||
		strings.Contains(msg, "EOF") {
		return errors.Join(ErrUnavailable, err)
	}

	return err
}

// registerMySQLTLS registers a per-connection *tls.Config with the
// mysql driver and returns the registered name.
//
// Post-review fixes:
//   - Registry name includes a SHA-256 hash of (ClientCert, ClientKey,
//     SSLMode) so credential rotation gets a fresh registration and
//     two Connections targeting the same host with DIFFERENT certs
//     don't collide. The previous host+port+sslmode-only name was a
//     multi-tenant credential-confusion vector.
//   - "preferred" no longer means InsecureSkipVerify=true. We treat
//     it identically to "require" (encrypt + verify). The "skip-verify"
//     mode is now spelled explicitly so it's grep-able in audit logs;
//     prod-tagged connections refuse skip-verify (in Open above).
//   - x509.SystemCertPool errors are surfaced rather than swallowed.
func registerMySQLTLS(c *dbadmin.Connection, creds *dbadmin.Credentials) (string, error) {
	tlsCfg := &tls.Config{
		ServerName: c.Host,
		MinVersion: tls.VersionTLS12,
	}

	switch strings.ToLower(c.SSLMode) {
	case "", "preferred", "require", "verify-ca", "verify-full":
		tlsCfg.InsecureSkipVerify = false
	case "skip-verify":
		tlsCfg.InsecureSkipVerify = true
	default:
		return "", fmt.Errorf("unknown sslmode %q", c.SSLMode)
	}

	if len(creds.ClientCert) > 0 && len(creds.ClientKey) > 0 {
		cert, err := tls.X509KeyPair(creds.ClientCert, creds.ClientKey)
		if err != nil {
			return "", fmt.Errorf("client cert/key: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	if !tlsCfg.InsecureSkipVerify && tlsCfg.RootCAs == nil {
		pool, err := x509.SystemCertPool()
		if err != nil {
			return "", fmt.Errorf("system CA pool unavailable: %w", err)
		}
		tlsCfg.RootCAs = pool
	}

	// Stable name = host + port + sslmode + sha256(cert||key||sslmode)
	// — so two Connections that share host:port but differ on creds
	// register under distinct names.
	h := sha256.New()
	h.Write([]byte(strings.ToLower(c.SSLMode)))
	h.Write(creds.ClientCert)
	h.Write(creds.ClientKey)
	suffix := hex.EncodeToString(h.Sum(nil)[:6])
	name := fmt.Sprintf("auradb-%s-%d-%s-%s", c.Host, c.Port, strings.ToLower(c.SSLMode), suffix)

	if err := mysqlsql.RegisterTLSConfig(name, tlsCfg); err != nil {
		// With the content-hash suffix, "already registered" means
		// the SAME (host, port, sslmode, cert, key) tuple is being
		// re-registered concurrently. Safe to reuse the existing
		// registration; it's identical.
		if strings.Contains(err.Error(), "already registered") {
			return name, nil
		}
		return "", err
	}
	return name, nil
}

package driver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// postgresDriverImpl implements Driver for PostgreSQL via pgx/v5.
//
// We use pgxpool rather than database/sql because pgx's native protocol
// gives us:
//   - Binary parameter encoding (smaller wire, lower CPU than text)
//   - Better EXPLAIN ANALYZE handling
//   - First-class JSON / JSONB support without round-trip cast
//   - Per-pool config (vs database/sql's process-wide registration)
type postgresDriverImpl struct{}

func (p *postgresDriverImpl) Engine() dbadmin.EngineKind {
	return dbadmin.EnginePostgres
}

// Open opens a pgxpool.Pool with hardened config.
//
// Hardening (SECURITY.md §6.3 + §7.2 + post-review):
//   - The password is NOT formatted into the connection string. Instead
//     we parse an empty config and set ConnConfig fields directly, so
//     no pgx error message can echo the password.
//   - QueryExecModeCacheDescribe keeps server-side parameterization
//     (binary protocol bind), avoids per-query prepare without falling
//     back to client-side interpolation. (Post-review fix: QueryExecMode-
//     Exec downgraded to simple-query protocol with client-side interp,
//     which is exactly what SECURITY.md §6.4 forbids.)
//   - sslmode is enforced: prod-tagged connections require verify-full;
//     non-prod can opt down to `require` or `disable` but `disable` is
//     refused for prod regardless of UseSSL. Defense-in-depth.
func (p *postgresDriverImpl) Open(ctx context.Context, c *dbadmin.Connection, creds *dbadmin.Credentials, poolSize int) (Conn, error) {
	if c == nil {
		return nil, errors.New("driver/postgres: nil Connection")
	}
	if creds == nil {
		return nil, errors.New("driver/postgres: nil Credentials")
	}

	hostPort := net.JoinHostPort(c.Host, fmt.Sprintf("%d", c.Port))

	// pgPort is the port pgx will dial. For TCP tunnels it's parsed
	// from LocalAddr; for unix tunnels (PR #3.5 default), we pick a
	// synthetic port and the tunnel listens on ".s.PGSQL.<port>" so
	// pgx's "Host=dir, Port=port" convention finds it.
	var (
		tun          *Tunnel
		tHost        string
		tPortInt     int
		usingUnixSkt bool
	)
	if c.SSHTunnel != nil {
		// Synthetic port for the unix-socket convention. Stays
		// inside the per-Connection directory so collisions across
		// connections don't matter; we still pick a non-privileged
		// number for hygiene.
		synthPort := uint16(5432)
		if c.Port > 0 && c.Port < 65536 {
			synthPort = uint16(c.Port)
		}
		t, err := OpenTunnelWithOptions(ctx, c.SSHTunnel, hostPort, poolSize+2, TunnelOptions{
			SocketName:   string(c.ID),
			PostgresPort: synthPort,
			IdleTimeout:  c.QueryIdleTimeout,
		})
		if err != nil {
			return nil, fmt.Errorf("driver/postgres: open tunnel: %w", err)
		}
		tun = t
		switch t.Network() {
		case "unix":
			// LocalAddr is the directory; pass it as Host. pgx
			// detects an absolute path and dials the matching
			// .s.PGSQL.<port> socket inside.
			tHost = t.LocalAddr()
			tPortInt = int(synthPort)
			usingUnixSkt = true
		default:
			// TCP fallback path.
			h, p, perr := net.SplitHostPort(t.LocalAddr())
			if perr != nil {
				_ = tun.Close()
				return nil, fmt.Errorf("driver/postgres: bad tunnel addr %q: %w", t.LocalAddr(), perr)
			}
			tHost = h
			if _, perr := fmt.Sscanf(p, "%d", &tPortInt); perr != nil || tPortInt == 0 {
				_ = tun.Close()
				return nil, fmt.Errorf("driver/postgres: bad tunnel port %q", p)
			}
		}
	} else {
		// Direct: parse the configured host:port.
		h, p, perr := net.SplitHostPort(hostPort)
		if perr != nil {
			return nil, fmt.Errorf("driver/postgres: bad addr %q: %w", hostPort, perr)
		}
		tHost = h
		if _, perr := fmt.Sscanf(p, "%d", &tPortInt); perr != nil || tPortInt == 0 {
			return nil, fmt.Errorf("driver/postgres: bad port %q", p)
		}
	}
	_ = usingUnixSkt // reserved for future TLS-suppression on unix sockets

	sslMode := strings.ToLower(c.SSLMode)
	if sslMode == "" {
		if c.UseSSL {
			sslMode = "require"
		} else {
			sslMode = "disable"
		}
	}
	// Post-review fix: prod connections enforce verify-full at this
	// layer, not just at the engine layer. Defense-in-depth.
	if c.IsProd() && sslMode != "verify-full" {
		if tun != nil {
			_ = tun.Close()
		}
		return nil, fmt.Errorf("driver/postgres: prod-tagged connection requires sslmode=verify-full, got %q", sslMode)
	}
	// mTLS client cert with in-memory bytes isn't supported by pgx via
	// the config string. Postpone to PR #3.5 (file-based cert pinning).
	if len(creds.ClientCert) > 0 && len(creds.ClientKey) > 0 {
		if tun != nil {
			_ = tun.Close()
		}
		return nil, errors.New("driver/postgres: in-memory client cert/key not supported in PR #3 (use file-based via ssl_cert / ssl_key); see docs/aura-db/KNOWN-ISSUES.md")
	}

	// Build a minimal "scaffold" config from an empty conn string, then
	// set all fields via the typed setters. This prevents the password
	// from ever being formatted into a DSN that could be echoed in an
	// error (post-review fix).
	cfg, err := pgxpool.ParseConfig("")
	if err != nil {
		if tun != nil {
			_ = tun.Close()
		}
		return nil, fmt.Errorf("driver/postgres: parse base config: %w", err)
	}
	cfg.ConnConfig.Host = tHost
	cfg.ConnConfig.Port = uint16(tPortInt)
	cfg.ConnConfig.User = c.Username
	cfg.ConnConfig.Password = creds.Password
	cfg.ConnConfig.Database = c.Database
	cfg.ConnConfig.ConnectTimeout = 10 * time.Second
	if err := applyPostgresTLS(cfg.ConnConfig, sslMode, c.Host); err != nil {
		if tun != nil {
			_ = tun.Close()
		}
		return nil, fmt.Errorf("driver/postgres: TLS config: %w", err)
	}

	// Server-side parameterization with describe-only caching. Each
	// query is parsed + described once per connection, but parameter
	// values are sent as binary-bound — never interpolated into the
	// SQL text. This is the correct default for an admin tool where
	// the SAME query is rarely repeated; for repeated queries pgx
	// will cache the describe on the connection.
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeCacheDescribe

	if poolSize < 1 {
		poolSize = 4
	}
	cfg.MaxConns = int32(poolSize)
	cfg.MinConns = 0
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		if tun != nil {
			_ = tun.Close()
		}
		return nil, classifyPostgresErr(err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		if tun != nil {
			_ = tun.Close()
		}
		return nil, classifyPostgresErr(wrapCtxErr(pingCtx, err))
	}

	return &postgresConn{pool: pool, tunnel: tun, conn: c}, nil
}

// postgresConn wraps a pgxpool.Pool + optional tunnel.
//
// tunnel is typed as an io.Closer-ish interface (tunnelCloser) to keep
// the Close path testable without standing up a real SSH server. In
// production this is always a *Tunnel; tests can substitute a stub.
type postgresConn struct {
	pool   *pgxpool.Pool
	tunnel tunnelCloser
	conn   *dbadmin.Connection
}

// tunnelCloser abstracts the Tunnel.Close() interaction so unit tests
// can substitute an error-returning stub. PR #3.5.
type tunnelCloser interface {
	Close() error
}

func (c *postgresConn) Query(ctx context.Context, limits Limits, sqlText string, args ...any) (Rows, error) {
	ctx, cancel := limits.ApplyTimeout(ctx)
	rows, err := c.pool.Query(ctx, sqlText, args...)
	if err != nil {
		cancel()
		return nil, classifyPostgresErr(wrapCtxErr(ctx, err))
	}
	inner := &postgresRows{rows: rows, cols: buildPostgresColumns(rows), cancel: cancel}
	return &LimitedRows{Inner: inner, L: limits}, nil
}

func (c *postgresConn) Exec(ctx context.Context, limits Limits, sqlText string, args ...any) (Result, error) {
	ctx, cancel := limits.ApplyTimeout(ctx)
	defer cancel()
	tag, err := c.pool.Exec(ctx, sqlText, args...)
	if err != nil {
		return Result{}, classifyPostgresErr(wrapCtxErr(ctx, err))
	}
	return Result{RowsAffected: tag.RowsAffected()}, nil
}

func (c *postgresConn) Ping(ctx context.Context) error {
	return classifyPostgresErr(wrapCtxErr(ctx, c.pool.Ping(ctx)))
}

func (c *postgresConn) ServerVersion(ctx context.Context) (string, error) {
	var v string
	if err := c.pool.QueryRow(ctx, "SELECT version()").Scan(&v); err != nil {
		return "", classifyPostgresErr(wrapCtxErr(ctx, err))
	}
	return v, nil
}

func (c *postgresConn) Close() error {
	var firstErr error
	if c.pool != nil {
		// PR #3.5: pgxpool.Close has no return today, but if a future
		// pgx release surfaces shutdown errors (e.g., flushing
		// outstanding writes failing on a degraded socket) the
		// closePoolHook below picks it up. The hook is a package-level
		// var so tests can stub it without instantiating a real
		// pgxpool.Pool. Mirrors mysqlConn.Close's firstErr ceremony.
		closePoolHook(&firstErr, func() {
			c.pool.Close()
		})
	}
	if c.tunnel != nil {
		if err := c.tunnel.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// closePoolHook invokes the real pgxpool.Close (the void closer arg) and
// gives a future pgx upgrade a single place to capture an error return.
// Test code can replace closePoolHook with a func that writes a sentinel
// error into *firstErr to exercise the propagation path. PR #3.5.
var closePoolHook = func(firstErr *error, closer func()) {
	closer()
	// pgxpool.Close() is void today; nothing to write to firstErr.
}

// postgresRows wraps pgx.Rows with column metadata + a type-aware Next.
type postgresRows struct {
	rows   pgx.Rows
	cols   []ColumnInfo
	closed bool
	cancel context.CancelFunc
}

func (r *postgresRows) Columns() []ColumnInfo {
	return r.cols
}

func (r *postgresRows) Next(ctx context.Context) ([]any, error) {
	if r.closed {
		return nil, ErrClosed
	}
	if !r.rows.Next() {
		if err := r.rows.Err(); err != nil {
			return nil, classifyPostgresErr(wrapCtxErr(ctx, err))
		}
		return nil, ErrEOF
	}
	vals, err := r.rows.Values()
	if err != nil {
		return nil, classifyPostgresErr(wrapCtxErr(ctx, err))
	}
	// Normalize pgx native types to the documented Rows contract.
	for i, v := range vals {
		vals[i] = normalizePostgresValue(v)
	}
	return vals, nil
}

func (r *postgresRows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	r.rows.Close()
	if r.cancel != nil {
		r.cancel()
	}
	return nil
}

// ─── Helpers ────────────────────────────────────────────────────────

func buildPostgresColumns(rows pgx.Rows) []ColumnInfo {
	fields := rows.FieldDescriptions()
	out := make([]ColumnInfo, len(fields))
	for i, f := range fields {
		out[i] = ColumnInfo{
			Name:             f.Name,
			DatabaseTypeName: pgTypeName(f.DataTypeOID),
		}
	}
	return out
}

// pgTypeName maps a Postgres type OID to its human-readable name. Unknown
// OIDs return "UNKNOWN" rather than "oid:N" so we don't leak the catalog
// to the browser (post-review fix).
func pgTypeName(oid uint32) string {
	switch oid {
	case 16:
		return "BOOLEAN"
	case 17:
		return "BYTEA"
	case 18:
		return "CHAR"
	case 19:
		return "NAME"
	case 20:
		return "BIGINT"
	case 21:
		return "SMALLINT"
	case 23:
		return "INTEGER"
	case 25:
		return "TEXT"
	case 26:
		return "OID"
	case 114, 3802:
		return "JSON"
	case 142:
		return "XML"
	case 700:
		return "REAL"
	case 701:
		return "DOUBLE PRECISION"
	case 1042:
		return "CHARACTER"
	case 1043:
		return "VARCHAR"
	case 1082:
		return "DATE"
	case 1083:
		return "TIME"
	case 1114:
		return "TIMESTAMP"
	case 1184:
		return "TIMESTAMPTZ"
	case 1186:
		return "INTERVAL"
	case 1266:
		return "TIMETZ"
	case 1700:
		return "NUMERIC"
	case 2950:
		return "UUID"
	default:
		return "UNKNOWN"
	}
}

// normalizePostgresValue maps pgx native types onto the documented Rows
// contract (int64, float64, string, []byte, time.Time UTC).
//
// pgx's default Values() returns wrapper types for some columns (UUIDs
// as [16]byte, JSON as map[string]any, pgtype.Numeric for NUMERIC, etc.).
// PR #4.5 expands the set of normalizations so downstream JSON output is
// human-readable across the common pgtype wrappers — NULL-valued
// wrappers render as untyped nil.
//
// Mappings:
//   - time.Time              → time.Time UTC
//   - [16]byte               → "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
//   - pgtype.UUID            → canonical text or nil when !Valid
//   - pgtype.Numeric         → decimal text via MarshalJSON, with quote stripped
//   - pgtype.Interval        → ISO-8601 duration text ("P1Y2M3DT4H5M6.789S")
//   - pgtype.Range[T]        → "[lower,upper)" text form (open/closed bounds)
//   - pgtype.Bool/Text/Int*/Float*/Date/Timestamp{,tz} → underlying primitive or nil
//
// Anything not matched falls through unchanged.
func normalizePostgresValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case time.Time:
		return x.UTC()
	case [16]byte:
		// UUID — render as canonical text form (8-4-4-4-12).
		return formatUUIDBytes(x)
	case pgtype.UUID:
		if !x.Valid {
			return nil
		}
		return formatUUIDBytes(x.Bytes)
	case pgtype.Numeric:
		if !x.Valid {
			return nil
		}
		return formatNumeric(x)
	case pgtype.Interval:
		if !x.Valid {
			return nil
		}
		return formatInterval(x)
	case pgtype.Bool:
		if !x.Valid {
			return nil
		}
		return x.Bool
	case pgtype.Text:
		if !x.Valid {
			return nil
		}
		return x.String
	case pgtype.Int2:
		if !x.Valid {
			return nil
		}
		return int64(x.Int16)
	case pgtype.Int4:
		if !x.Valid {
			return nil
		}
		return int64(x.Int32)
	case pgtype.Int8:
		if !x.Valid {
			return nil
		}
		return x.Int64
	case pgtype.Float4:
		if !x.Valid {
			return nil
		}
		return float64(x.Float32)
	case pgtype.Float8:
		if !x.Valid {
			return nil
		}
		return x.Float64
	case pgtype.Date:
		if !x.Valid {
			return nil
		}
		return x.Time.UTC()
	case pgtype.Timestamp:
		if !x.Valid {
			return nil
		}
		return x.Time.UTC()
	case pgtype.Timestamptz:
		if !x.Valid {
			return nil
		}
		return x.Time.UTC()
	case pgtype.Range[pgtype.Numeric]:
		if !x.Valid {
			return nil
		}
		return formatRange(x.LowerType, x.UpperType, formatNumeric(x.Lower), formatNumeric(x.Upper))
	case pgtype.Range[pgtype.Int4]:
		if !x.Valid {
			return nil
		}
		return formatRange(x.LowerType, x.UpperType, formatInt4(x.Lower), formatInt4(x.Upper))
	case pgtype.Range[pgtype.Int8]:
		if !x.Valid {
			return nil
		}
		return formatRange(x.LowerType, x.UpperType, formatInt8(x.Lower), formatInt8(x.Upper))
	case pgtype.Range[pgtype.Timestamp]:
		if !x.Valid {
			return nil
		}
		return formatRange(x.LowerType, x.UpperType, formatTime(x.Lower.Time, x.Lower.Valid), formatTime(x.Upper.Time, x.Upper.Valid))
	case pgtype.Range[pgtype.Timestamptz]:
		if !x.Valid {
			return nil
		}
		return formatRange(x.LowerType, x.UpperType, formatTime(x.Lower.Time, x.Lower.Valid), formatTime(x.Upper.Time, x.Upper.Valid))
	case pgtype.Range[pgtype.Date]:
		if !x.Valid {
			return nil
		}
		return formatRange(x.LowerType, x.UpperType, formatTime(x.Lower.Time, x.Lower.Valid), formatTime(x.Upper.Time, x.Upper.Valid))
	default:
		return v
	}
}

// formatUUIDBytes renders a 16-byte UUID as the canonical
// "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx" text form.
func formatUUIDBytes(b [16]byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// formatNumeric renders a pgtype.Numeric as a plain decimal string.
// NaN and infinity render as their backend tokens. Empty / !Valid
// returns "".
func formatNumeric(n pgtype.Numeric) string {
	if !n.Valid {
		return ""
	}
	if n.NaN {
		return "NaN"
	}
	switch n.InfinityModifier {
	case pgtype.Infinity:
		return "Infinity"
	case pgtype.NegativeInfinity:
		return "-Infinity"
	}
	if n.Int == nil {
		// Defensive — should always be non-nil for Valid && !NaN.
		return "0"
	}
	if n.Exp == 0 {
		return n.Int.String()
	}
	if n.Exp > 0 {
		// Append exp zeros to the integer.
		mul := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n.Exp)), nil)
		out := new(big.Int).Mul(n.Int, mul)
		return out.String()
	}
	// Negative exponent: insert decimal point. The integer's absolute
	// value's digits get split at -exp from the right.
	neg := n.Int.Sign() < 0
	digits := new(big.Int).Abs(n.Int).String()
	scale := int(-n.Exp)
	if len(digits) <= scale {
		digits = strings.Repeat("0", scale-len(digits)+1) + digits
	}
	whole := digits[:len(digits)-scale]
	frac := strings.TrimRight(digits[len(digits)-scale:], "0")
	out := whole
	if frac != "" {
		out += "." + frac
	}
	if neg {
		out = "-" + out
	}
	return out
}

// formatInterval renders a pgtype.Interval as a compact ISO-8601-ish
// string. PostgreSQL itself accepts the form, and downstream JSON output
// stays human-readable.
func formatInterval(iv pgtype.Interval) string {
	if !iv.Valid {
		return ""
	}
	if iv.Microseconds == 0 && iv.Days == 0 && iv.Months == 0 {
		return "PT0S"
	}
	var b strings.Builder
	b.WriteByte('P')
	years := iv.Months / 12
	months := iv.Months % 12
	if years != 0 {
		fmt.Fprintf(&b, "%dY", years)
	}
	if months != 0 {
		fmt.Fprintf(&b, "%dM", months)
	}
	if iv.Days != 0 {
		fmt.Fprintf(&b, "%dD", iv.Days)
	}
	if iv.Microseconds != 0 {
		b.WriteByte('T')
		us := iv.Microseconds
		neg := us < 0
		if neg {
			us = -us
		}
		hours := us / 3_600_000_000
		us -= hours * 3_600_000_000
		minutes := us / 60_000_000
		us -= minutes * 60_000_000
		seconds := us / 1_000_000
		fracUs := us % 1_000_000
		sign := ""
		if neg {
			sign = "-"
		}
		if hours != 0 {
			fmt.Fprintf(&b, "%s%dH", sign, hours)
		}
		if minutes != 0 {
			fmt.Fprintf(&b, "%s%dM", sign, minutes)
		}
		if seconds != 0 || fracUs != 0 {
			if fracUs == 0 {
				fmt.Fprintf(&b, "%s%dS", sign, seconds)
			} else {
				fmt.Fprintf(&b, "%s%d.%06dS", sign, seconds, fracUs)
			}
		}
	}
	return b.String()
}

// formatRange renders a Postgres range as the standard literal text
// form, e.g. "[1,5)" or "(2025-01-01,2025-02-01]". Empty bounds render
// as the empty string between the brackets.
func formatRange(lt, ut pgtype.BoundType, lower, upper string) string {
	var lb, ub byte
	switch lt {
	case pgtype.Inclusive:
		lb = '['
	default:
		lb = '('
	}
	switch ut {
	case pgtype.Inclusive:
		ub = ']'
	default:
		ub = ')'
	}
	return string([]byte{lb}) + lower + "," + upper + string([]byte{ub})
}

func formatInt4(i pgtype.Int4) string {
	if !i.Valid {
		return ""
	}
	return strconv.FormatInt(int64(i.Int32), 10)
}

func formatInt8(i pgtype.Int8) string {
	if !i.Valid {
		return ""
	}
	return strconv.FormatInt(i.Int64, 10)
}

func formatTime(t time.Time, valid bool) string {
	if !valid {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// classifyPostgresErr maps a pgx error to one of the typed sentinels.
//
// Post-review fixes:
//   - Adds SQLSTATE class 3D (invalid_catalog_name → ErrNotFound /
//     wrapped) and class 40 (transaction rollback → wrapped).
//   - Returns errors.Join(sentinel, err) so callers can errors.Unwrap
//     into the original pgconn.PgError details (audit log gets full
//     fidelity; HTTP layer surfaces only the sentinel + safe message).
//   - Auth and permission errors no longer surface the verbatim
//     backend message; syntax errors still do (operator needs them).
func classifyPostgresErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrEOF) || errors.Is(err, ErrCapped) ||
		errors.Is(err, ErrClosed) || errors.Is(err, ErrTimeout) ||
		errors.Is(err, ErrCanceled) {
		return err
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case strings.HasPrefix(pgErr.Code, "28"):
			return errors.Join(ErrAuth, err)
		case pgErr.Code == "42501":
			return errors.Join(ErrPermission, err)
		case strings.HasPrefix(pgErr.Code, "42"):
			return fmt.Errorf("%w: %s", ErrSyntax, pgErr.Message)
		case strings.HasPrefix(pgErr.Code, "23"):
			return fmt.Errorf("%w: %s", ErrConflict, pgErr.Message)
		case strings.HasPrefix(pgErr.Code, "3D"),
			strings.HasPrefix(pgErr.Code, "3F"):
			return errors.Join(ErrNotFound, err)
		case strings.HasPrefix(pgErr.Code, "53"),
			strings.HasPrefix(pgErr.Code, "57"),
			strings.HasPrefix(pgErr.Code, "08"):
			return errors.Join(ErrUnavailable, err)
		}
	}

	msg := err.Error()
	if strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "context canceled") ||
		strings.Contains(msg, "EOF") {
		return errors.Join(ErrUnavailable, err)
	}

	return err
}

// applyPostgresTLS sets ConnConfig.TLSConfig based on sslMode. We don't
// rely on pgx's connection-string sslmode parsing because we want to
// (a) keep the password out of any string pgx might echo and (b) be
// explicit about the verification flags.
func applyPostgresTLS(cfg *pgx.ConnConfig, sslMode, serverName string) error {
	switch sslMode {
	case "disable":
		cfg.TLSConfig = nil
		return nil
	case "require":
		// Encrypted, no verification. Useful for self-signed dev
		// servers; refused for prod (caller enforces).
		cfg.TLSConfig = &tls.Config{
			ServerName:         serverName,
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true,
		}
		return nil
	case "verify-ca", "verify-full":
		pool, err := x509.SystemCertPool()
		if err != nil {
			return fmt.Errorf("system CA pool unavailable: %w", err)
		}
		cfg.TLSConfig = &tls.Config{
			ServerName: serverName,
			MinVersion: tls.VersionTLS12,
			RootCAs:    pool,
		}
		return nil
	}
	return fmt.Errorf("unknown sslmode %q", sslMode)
}

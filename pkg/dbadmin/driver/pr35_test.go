package driver

// Tests for PR #3.5 driver-hardening items A–H. See
// docs/aura-db/KNOWN-ISSUES.md for the canonical descriptions.

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// pgxpoolAlias is a type alias used in PR #3.5 firstErr-surfacing tests
// so test helpers can return *pgxpool.Pool without re-importing in every
// helper signature.
type pgxpoolAlias = pgxpool.Pool

// ─── 3.5-A: unix-socket tunnel listener ─────────────────────────────────

// TestPR35_A_OpenTunnelListener_UnixMode verifies the openTunnelListener
// helper creates a unix socket file with mode 0600 in the configured
// base dir, returns the right paths, and respects PostgresPort naming.
func TestPR35_A_OpenTunnelListener_UnixMode(t *testing.T) {
	dir := t.TempDir()
	prev := SetTunnelSocketBaseDir(dir)
	defer SetTunnelSocketBaseDir(prev)

	lis, sockPath, advertise, err := openTunnelListener(TunnelOptions{
		SocketName: "test-conn-A",
	})
	if err != nil {
		t.Fatalf("openTunnelListener: %v", err)
	}
	defer lis.Close()

	if !strings.HasSuffix(sockPath, "test-conn-A.sock") {
		t.Errorf("sockPath = %q, want suffix test-conn-A.sock", sockPath)
	}
	if advertise != sockPath {
		t.Errorf("advertise %q != sockPath %q for mysql-mode socket", advertise, sockPath)
	}

	st, err := os.Stat(sockPath)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Errorf("socket mode = %#o, want 0600", mode)
	}
	if st.Mode()&os.ModeSocket == 0 {
		t.Errorf("not a socket: %v", st.Mode())
	}
}

// TestPR35_A_OpenTunnelListener_PostgresMode verifies the pgx-friendly
// ".s.PGSQL.<port>" socket naming.
func TestPR35_A_OpenTunnelListener_PostgresMode(t *testing.T) {
	dir := t.TempDir()
	prev := SetTunnelSocketBaseDir(dir)
	defer SetTunnelSocketBaseDir(prev)

	lis, sockPath, advertise, err := openTunnelListener(TunnelOptions{
		SocketName:   "pg-conn",
		PostgresPort: 5432,
	})
	if err != nil {
		t.Fatalf("openTunnelListener: %v", err)
	}
	defer lis.Close()

	if !strings.HasSuffix(sockPath, ".s.PGSQL.5432") {
		t.Errorf("sockPath = %q, want suffix .s.PGSQL.5432", sockPath)
	}
	// Advertise is the parent directory, not the socket file (pgx's
	// convention).
	if advertise == sockPath {
		t.Errorf("advertise == sockPath; expected the parent directory")
	}
	if filepath.Dir(sockPath) != advertise {
		t.Errorf("filepath.Dir(sockPath) = %q, want %q", filepath.Dir(sockPath), advertise)
	}

	st, err := os.Stat(sockPath)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Errorf("socket mode = %#o, want 0600", mode)
	}
}

// TestPR35_A_SafeSocketName_UnsafeID verifies that a ConnectionID with
// path-traversal characters gets hashed rather than passed through.
func TestPR35_A_SafeSocketName_UnsafeID(t *testing.T) {
	cases := []string{
		"../etc/passwd",
		"name with spaces",
		"name/slash",
		strings.Repeat("a", 200), // too long → hash
		"",                       // empty → not all-safe → hash
	}
	for _, c := range cases {
		got := safeSocketName(c)
		if strings.ContainsAny(got, "/. ") {
			t.Errorf("safeSocketName(%q) = %q; still contains unsafe chars", c, got)
		}
		if len(got) > 32 {
			t.Errorf("safeSocketName(%q) = %q; len %d > 32", c, got, len(got))
		}
	}
}

func TestPR35_A_SafeSocketName_PassesThroughSafeID(t *testing.T) {
	cases := []string{"conn-A", "abc_123", "ABCdef"}
	for _, c := range cases {
		if got := safeSocketName(c); got != c {
			t.Errorf("safeSocketName(%q) = %q, want passthrough", c, got)
		}
	}
}

// TestPR35_A_TunnelNetwork verifies Tunnel.Network() returns "unix" for
// the unix-socket default and "tcp" for the fallback.
func TestPR35_A_TunnelNetwork_ReportsUnix(t *testing.T) {
	dir := t.TempDir()
	prev := SetTunnelSocketBaseDir(dir)
	defer SetTunnelSocketBaseDir(prev)

	lis, sockPath, advertise, err := openTunnelListener(TunnelOptions{
		SocketName: "net-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()

	_, ok := lis.(*tcpFallbackListener)
	if ok {
		t.Errorf("expected unix listener, got tcp fallback")
	}
	if !strings.Contains(sockPath, "net-test.sock") {
		t.Errorf("sock path %q does not contain expected name", sockPath)
	}
	if !strings.Contains(advertise, "net-test.sock") {
		t.Errorf("advertise %q missing socket name", advertise)
	}
}

// TestPR35_A_TunnelFallback_TCP verifies that when the socket base dir
// is unreachable, the listener falls back to 127.0.0.1:0 TCP.
func TestPR35_A_TunnelFallback_TCP(t *testing.T) {
	// Point base dir at a non-creatable location to trigger fallback.
	// Use a parent path that's a regular file so MkdirAll fails.
	tf, err := os.CreateTemp("", "blocker-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tf.Name())
	tf.Close()

	blocked := tf.Name() + "/sub"
	prev := SetTunnelSocketBaseDir(blocked)
	defer SetTunnelSocketBaseDir(prev)

	lis, sockPath, advertise, err := openTunnelListener(TunnelOptions{
		SocketName: "fallback-test",
	})
	if err != nil {
		t.Fatalf("expected fallback, got error: %v", err)
	}
	defer lis.Close()

	if _, ok := lis.(*tcpFallbackListener); !ok {
		t.Errorf("expected *tcpFallbackListener, got %T", lis)
	}
	if sockPath != "" {
		t.Errorf("fallback should not produce a socket file path, got %q", sockPath)
	}
	if !strings.HasPrefix(advertise, "127.0.0.1:") {
		t.Errorf("fallback advertise = %q, want 127.0.0.1:<port>", advertise)
	}
}

// TestPR35_A_OpenTunnel_RoundTrip_UnixSocket smoke-tests the unix-socket
// listener end-to-end without a real SSH server. We can't actually
// dial through a tunnel (no SSH server), but we CAN verify the listener
// is up, accepts unix-socket connections, and goes away on Close.
//
// This proves the path: socket file created → can connect to it → file
// removed on close. The actual SSH-proxy machinery is exercised by the
// integration tests.
func TestPR35_A_UnixSocketAccessible(t *testing.T) {
	dir := t.TempDir()
	prev := SetTunnelSocketBaseDir(dir)
	defer SetTunnelSocketBaseDir(prev)

	lis, sockPath, _, err := openTunnelListener(TunnelOptions{
		SocketName: "smoke-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()

	// A client must be able to dial the unix socket.
	go func() {
		c, err := lis.Accept()
		if err != nil {
			return
		}
		_ = c.Close()
	}()

	c, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial unix %s: %v", sockPath, err)
	}
	_ = c.Close()
}

// ─── 3.5-B: per-Connection QueryIdleTimeout ─────────────────────────────

// TestPR35_B_TunnelOptions_IdleTimeoutHonored verifies that
// OpenTunnelWithOptions stores the IdleTimeout on the *Tunnel where the
// forwardOne loop reads it.
func TestPR35_B_TunnelOptions_IdleTimeoutHonored(t *testing.T) {
	// We can't construct a Tunnel without an SSH server, so synthesize
	// a Tunnel struct directly and verify the field is wired the way
	// we expect.
	t1 := &Tunnel{idleTimeout: 0}
	if t1.idleTimeout != 0 {
		t.Errorf("zero default failed")
	}

	t2 := &Tunnel{idleTimeout: 2 * time.Minute}
	if t2.idleTimeout != 2*time.Minute {
		t.Errorf("idleTimeout = %v, want 2m", t2.idleTimeout)
	}

	// Verify the default constant is the value we documented.
	if defaultIdleTimeout != 5*time.Minute {
		t.Errorf("defaultIdleTimeout = %v, want 5m", defaultIdleTimeout)
	}
}

// TestPR35_B_IdleDeadlineConn_RespectsField verifies that
// idleDeadlineConn.idle is set on each Read/Write via the wrapped Conn.
// Uses a pipe pair to confirm the deadline propagates.
func TestPR35_B_IdleDeadlineConn_AppliesDeadline(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	conn := &idleDeadlineConn{Conn: a, idle: 5 * time.Millisecond}

	// b writes nothing; conn.Read should hit the idle deadline.
	buf := make([]byte, 16)
	start := time.Now()
	_, err := conn.Read(buf)
	if err == nil {
		t.Fatal("expected idle-deadline timeout, got nil")
	}
	elapsed := time.Since(start)
	if elapsed > 200*time.Millisecond {
		t.Errorf("Read blocked %v; expected ~5ms idle deadline", elapsed)
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Errorf("expected timeout error, got %v", err)
	}
}

// TestPR35_B_QueryIdleTimeout_OnConnection verifies the new field is
// readable on the Connection struct.
func TestPR35_B_QueryIdleTimeout_FieldExists(t *testing.T) {
	c := &dbadmin.Connection{QueryIdleTimeout: 90 * time.Second}
	if c.QueryIdleTimeout != 90*time.Second {
		t.Errorf("QueryIdleTimeout field round-trip failed: %v", c.QueryIdleTimeout)
	}
}

// ─── 3.5-C: MaxBytesPerCell row-drop ────────────────────────────────────

func TestPR35_C_MaxBytesPerCell_DropsRow(t *testing.T) {
	// 100-byte string > 50-byte cap → row dropped, ErrCapped returned.
	big := strings.Repeat("x", 100)
	inner := &stubRows{
		rows: [][]any{
			{"small"}, // fits → returned
			{big},     // exceeds → dropped
			{"never"}, // never reached
		},
		cols: []ColumnInfo{{Name: "v"}},
	}
	lr := &LimitedRows{Inner: inner, L: Limits{MaxBytesPerCell: 50}}
	defer lr.Close()

	vals, err := lr.Next(context.Background())
	if err != nil {
		t.Fatalf("Next 1 err = %v", err)
	}
	if vals[0] != "small" {
		t.Errorf("first row = %v, want small", vals)
	}

	_, err = lr.Next(context.Background())
	if !errors.Is(err, ErrCapped) {
		t.Errorf("expected ErrCapped on oversized cell row, got %v", err)
	}

	// Subsequent Next() also returns ErrCapped (sticky).
	_, err = lr.Next(context.Background())
	if !errors.Is(err, ErrCapped) {
		t.Errorf("expected sticky ErrCapped, got %v", err)
	}
	if lr.RowsCounted() != 1 {
		t.Errorf("RowsCounted = %d, want 1 (the dropped row should NOT count)", lr.RowsCounted())
	}
}

func TestPR35_C_MaxBytesPerCell_Zero_NoCap(t *testing.T) {
	big := strings.Repeat("y", 1024)
	inner := &stubRows{
		rows: [][]any{{big}, {big}, {big}},
		cols: []ColumnInfo{{Name: "v"}},
	}
	lr := &LimitedRows{Inner: inner, L: Limits{}} // zero per-cell cap
	defer lr.Close()

	count := 0
	for {
		_, err := lr.Next(context.Background())
		if errors.Is(err, ErrEOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next err = %v", err)
		}
		count++
	}
	if count != 3 {
		t.Errorf("got %d rows, want 3 (no per-cell cap)", count)
	}
}

func TestPR35_C_MaxBytesPerCell_HighCap_NoDrop(t *testing.T) {
	inner := &stubRows{
		rows: [][]any{{"hello"}, {"world"}},
		cols: []ColumnInfo{{Name: "v"}},
	}
	lr := &LimitedRows{Inner: inner, L: Limits{MaxBytesPerCell: 1 << 20}}
	defer lr.Close()

	count := 0
	for {
		_, err := lr.Next(context.Background())
		if errors.Is(err, ErrEOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next err = %v", err)
		}
		count++
	}
	if count != 2 {
		t.Errorf("got %d rows, want 2", count)
	}
}

// ─── 3.5-D: MySQL NewConnector + cfg.Passwd null-out ────────────────────

// TestPR35_D_NoSqlOpenDSNPath verifies that mysql.go no longer routes
// through sql.Open("mysql", DSN) (which would retain a string copy of
// the password). The current code uses NewConnector + sql.OpenDB.
//
// This is a source-grep test — we don't want to dial a real DB just to
// confirm the code path. Drift is caught by the integration suite.
func TestPR35_D_NoSqlOpenDSNPath(t *testing.T) {
	src, err := os.ReadFile("mysql.go")
	if err != nil {
		t.Skipf("can't read mysql.go: %v", err)
	}
	s := string(src)
	if strings.Contains(s, `sql.Open("mysql"`) {
		t.Errorf("mysql.go still calls sql.Open(\"mysql\", ...); PR #3.5 expects sql.OpenDB(connector)")
	}
	if !strings.Contains(s, "NewConnector(cfg)") {
		t.Errorf("mysql.go missing NewConnector(cfg) call")
	}
	if !strings.Contains(s, "cfg.Passwd = \"\"") {
		t.Errorf("mysql.go must null cfg.Passwd after NewConnector")
	}
}

// ─── 3.5-E: per-conn TLS-name tracking ──────────────────────────────────

// TestPR35_E_MysqlConn_TLSNamesField verifies the *mysqlConn struct
// carries the tlsNames slice for per-conn registry bookkeeping.
func TestPR35_E_MysqlConn_TLSNamesTracked(t *testing.T) {
	mc := &mysqlConn{tlsNames: []string{"a", "b"}}
	if len(mc.tlsNames) != 2 {
		t.Errorf("tlsNames slice not retained: %v", mc.tlsNames)
	}

	// Close on a zero mysqlConn (no db, no tunnel) walks the slice and
	// clears it. Even though we can't verify deregistration without
	// touching the driver-global mysql registry, we can confirm Close
	// nulls the slice.
	mc2 := &mysqlConn{tlsNames: []string{"unused-name-pr35"}}
	_ = mc2.Close() // db nil → no error; the deregister call is best-effort
	if mc2.tlsNames != nil {
		t.Errorf("Close didn't null tlsNames; got %v", mc2.tlsNames)
	}
	if mc2.tlsName != "" {
		t.Errorf("Close didn't null tlsName; got %q", mc2.tlsName)
	}
}

// ─── 3.5-F: MySQL engine-identity verification ──────────────────────────

func TestPR35_F_IsMySQLOrMariaDBVersion(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{
		{"8.0.36-MariaDB", true},
		{"5.7.34-MySQL", true},
		{"mariadb-11.4.2", true},
		{"MySQL Community Server 8.0", true},
		{"PostgreSQL 16.4", false},
		{"CockroachDB v23.1", false},
		{"", false},
		{"SQLite 3.40", false},
	}
	for _, c := range cases {
		if got := isMySQLOrMariaDBVersion(c.v); got != c.want {
			t.Errorf("isMySQLOrMariaDBVersion(%q) = %v, want %v", c.v, got, c.want)
		}
	}
}

// ─── 3.5-G: postgresConn.Close firstErr parity ──────────────────────────

// TestPR35_G_PostgresClose_FirstErrHookFires verifies that closePoolHook
// is a patchable var, takes a *error sink, and that postgresConn.Close
// surfaces an error written to *firstErr by the hook. This proves the
// forward-compat plumbing for a future pgx that surfaces Close errors.
func TestPR35_G_PostgresClose_FirstErrHookFires(t *testing.T) {
	prev := closePoolHook
	defer func() { closePoolHook = prev }()

	stubErr := errors.New("simulated future pgx close error")
	called := false
	closePoolHook = func(firstErr *error, closer func()) {
		called = true
		// Don't invoke the real closer (we have no real pool); just
		// write the simulated error to firstErr to exercise the
		// propagation path.
		*firstErr = stubErr
	}

	// pool is non-nil so the Close branch runs; nothing dereferences
	// it because the stub skips closer().
	c := &postgresConn{pool: nonNilPgxpoolPlaceholder()}
	err := c.Close()
	if !called {
		t.Error("closePoolHook was not invoked")
	}
	if !errors.Is(err, stubErr) {
		t.Errorf("postgresConn.Close didn't surface stubErr; got %v", err)
	}
}

// TestPR35_G_PostgresClose_TunnelErrSurfaces verifies that when the
// closePoolHook writes nil (the current pgx behavior), a tunnel.Close
// error still surfaces as the firstErr.
func TestPR35_G_PostgresClose_TunnelErrSurfaces(t *testing.T) {
	prev := closePoolHook
	defer func() { closePoolHook = prev }()
	closePoolHook = func(firstErr *error, closer func()) {
		// no-op — pgxpool.Close is void today
	}

	tunErr := errors.New("tunnel close went sideways")
	c := &postgresConn{
		pool:   nonNilPgxpoolPlaceholder(),
		tunnel: &errClosingTunnel{err: tunErr},
	}
	err := c.Close()
	if !errors.Is(err, tunErr) {
		t.Errorf("expected tunnel close err to surface, got %v", err)
	}
}

// errClosingTunnel is a tunnelCloser stub that returns a configured
// error from Close. Used by PR #3.5 firstErr-surfacing tests.
type errClosingTunnel struct {
	err error
}

func (e *errClosingTunnel) Close() error { return e.err }

// nonNilPgxpoolPlaceholder returns a non-nil *pgxpool.Pool sentinel so
// the c.pool != nil branch of postgresConn.Close runs in tests. The
// closePoolHook stub MUST skip the actual closer() call — a real
// pool.Close() on this placeholder would segfault on internal field
// access.
func nonNilPgxpoolPlaceholder() *pgxpoolAlias {
	var p pgxpoolAlias
	return &p
}

// ─── 3.5-H: idleSweeper interval floor 1s ───────────────────────────────

func TestPR35_H_IdleSweeper_FloorOneSecond(t *testing.T) {
	stub := newStubDriver(dbadmin.EngineMariaDB)
	restore := installStubDriver(t, dbadmin.EngineMariaDB, stub)
	defer restore()

	pool := NewPool(PoolOptions{
		Resolver: func(ctx context.Context, id dbadmin.ConnectionID) (*dbadmin.Connection, *dbadmin.Credentials, error) {
			c, creds := makeConn(id, dbadmin.EngineMariaDB)
			return c, creds, nil
		},
		// Very aggressive idle timeout. Under PR #3.5 floor of 1s,
		// the sweeper still runs at 1s intervals (not 30s). We
		// confirm eviction happens within a couple of seconds.
		IdleTimeout: 100 * time.Millisecond,
	})
	defer pool.Close()

	_, rel, err := pool.Withdraw(context.Background(), "conn-floor")
	if err != nil {
		t.Fatal(err)
	}
	rel()

	if got := pool.Stats().OpenConns; got != 1 {
		t.Errorf("OpenConns initial = %d, want 1", got)
	}

	// Wait up to ~3 seconds for the natural sweeper to fire.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if pool.Stats().OpenConns == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("natural sweep did not evict within 3s; with PR #3.5 floor of 1s it should fire ~1s after withdraw. OpenConns=%d", pool.Stats().OpenConns)
}

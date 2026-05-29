package driver_test

// Integration tests run against real MariaDB + PostgreSQL instances.
// They're env-gated so `go test` on a plain laptop doesn't fail when no
// database is running. CI sets the env vars from docker-compose service
// containers; see .github/workflows/aura-db.yaml (PR #18).
//
// To run locally:
//
//	docker run -d --rm -p 3306:3306 -e MARIADB_ROOT_PASSWORD=test mariadb:11
//	docker run -d --rm -p 5432:5432 -e POSTGRES_PASSWORD=test postgres:16
//	export DBADMIN_TEST_MYSQL_DSN="root:test@tcp(127.0.0.1:3306)/mysql"
//	export DBADMIN_TEST_PG_HOST=127.0.0.1
//	export DBADMIN_TEST_PG_PORT=5432
//	export DBADMIN_TEST_PG_USER=postgres
//	export DBADMIN_TEST_PG_PASS=test
//	export DBADMIN_TEST_PG_DB=postgres
//	go test ./pkg/dbadmin/driver/...

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/driver"
)

func skipUnlessSet(t *testing.T, vars ...string) {
	t.Helper()
	for _, v := range vars {
		if os.Getenv(v) == "" {
			t.Skipf("integration test requires %s", v)
		}
	}
}

func mysqlConnFromEnv(t *testing.T) (*dbadmin.Connection, *dbadmin.Credentials) {
	t.Helper()
	skipUnlessSet(t, "DBADMIN_TEST_MYSQL_HOST", "DBADMIN_TEST_MYSQL_PORT",
		"DBADMIN_TEST_MYSQL_USER", "DBADMIN_TEST_MYSQL_PASS", "DBADMIN_TEST_MYSQL_DB")
	port, _ := strconv.Atoi(os.Getenv("DBADMIN_TEST_MYSQL_PORT"))
	return &dbadmin.Connection{
			Engine:   dbadmin.EngineMariaDB,
			Host:     os.Getenv("DBADMIN_TEST_MYSQL_HOST"),
			Port:     port,
			Database: os.Getenv("DBADMIN_TEST_MYSQL_DB"),
			Username: os.Getenv("DBADMIN_TEST_MYSQL_USER"),
			UseSSL:   false,
		}, &dbadmin.Credentials{
			Password: os.Getenv("DBADMIN_TEST_MYSQL_PASS"),
		}
}

func pgConnFromEnv(t *testing.T) (*dbadmin.Connection, *dbadmin.Credentials) {
	t.Helper()
	skipUnlessSet(t, "DBADMIN_TEST_PG_HOST", "DBADMIN_TEST_PG_PORT",
		"DBADMIN_TEST_PG_USER", "DBADMIN_TEST_PG_PASS", "DBADMIN_TEST_PG_DB")
	port, _ := strconv.Atoi(os.Getenv("DBADMIN_TEST_PG_PORT"))
	return &dbadmin.Connection{
			Engine:   dbadmin.EnginePostgres,
			Host:     os.Getenv("DBADMIN_TEST_PG_HOST"),
			Port:     port,
			Database: os.Getenv("DBADMIN_TEST_PG_DB"),
			Username: os.Getenv("DBADMIN_TEST_PG_USER"),
			UseSSL:   false,
			SSLMode:  "disable",
		}, &dbadmin.Credentials{
			Password: os.Getenv("DBADMIN_TEST_PG_PASS"),
		}
}

// ─── MySQL/MariaDB integration ──────────────────────────────────────

func TestIntegration_MySQL_QueryRoundtrip(t *testing.T) {
	c, creds := mysqlConnFromEnv(t)
	d, _ := driver.For(dbadmin.EngineMariaDB)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := d.Open(ctx, c, creds, 2)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()

	rows, err := conn.Query(ctx, driver.Limits{}, "SELECT 1, 'hello', NULL")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()

	cols := rows.Columns()
	if len(cols) != 3 {
		t.Fatalf("got %d columns, want 3", len(cols))
	}

	vals, err := rows.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(vals) != 3 {
		t.Fatalf("got %d vals, want 3", len(vals))
	}
	// vals[0] should be 1 (int64), vals[1] = "hello", vals[2] = nil.
	if vals[2] != nil {
		t.Errorf("vals[2] = %v (type %T), want nil", vals[2], vals[2])
	}

	if _, err := rows.Next(ctx); !errors.Is(err, driver.ErrEOF) {
		t.Errorf("expected ErrEOF, got %v", err)
	}
}

func TestIntegration_MySQL_ServerVersion(t *testing.T) {
	c, creds := mysqlConnFromEnv(t)
	d, _ := driver.For(dbadmin.EngineMariaDB)
	conn, err := d.Open(context.Background(), c, creds, 2)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()

	v, err := conn.ServerVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v == "" {
		t.Error("empty version")
	}
	t.Logf("MySQL/MariaDB version: %s", v)
}

func TestIntegration_MySQL_AuthFailure(t *testing.T) {
	c, _ := mysqlConnFromEnv(t)
	d, _ := driver.For(dbadmin.EngineMariaDB)
	_, err := d.Open(context.Background(), c, &dbadmin.Credentials{Password: "WRONG-PASSWORD"}, 2)
	if err == nil {
		t.Fatal("expected auth error")
	}
	if !errors.Is(err, driver.ErrAuth) && !errors.Is(err, driver.ErrUnavailable) {
		t.Errorf("error doesn't map to ErrAuth or ErrUnavailable: %v", err)
	}
}

func TestIntegration_MySQL_QueryTimeout(t *testing.T) {
	c, creds := mysqlConnFromEnv(t)
	d, _ := driver.For(dbadmin.EngineMariaDB)
	conn, err := d.Open(context.Background(), c, creds, 2)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = conn.Query(ctx, driver.Limits{}, "SELECT SLEEP(5)")
	if err == nil {
		t.Fatal("expected timeout, query succeeded")
	}
	if !errors.Is(err, driver.ErrTimeout) {
		t.Errorf("expected ErrTimeout, got %v", err)
	}
}

func TestIntegration_MySQL_SyntaxError(t *testing.T) {
	c, creds := mysqlConnFromEnv(t)
	d, _ := driver.For(dbadmin.EngineMariaDB)
	conn, err := d.Open(context.Background(), c, creds, 2)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()

	_, err = conn.Query(context.Background(), driver.Limits{}, "SELEC 1") // typo
	if err == nil {
		t.Fatal("expected syntax error")
	}
	if !errors.Is(err, driver.ErrSyntax) {
		t.Errorf("expected ErrSyntax, got %v", err)
	}
}

// ─── PostgreSQL integration ──────────────────────────────────────────

func TestIntegration_PG_QueryRoundtrip(t *testing.T) {
	c, creds := pgConnFromEnv(t)
	d, _ := driver.For(dbadmin.EnginePostgres)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := d.Open(ctx, c, creds, 2)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()

	rows, err := conn.Query(ctx, driver.Limits{}, "SELECT 1::int, 'hello'::text, NULL::text")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()

	cols := rows.Columns()
	if len(cols) != 3 {
		t.Fatalf("got %d columns, want 3", len(cols))
	}

	vals, err := rows.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(vals) != 3 {
		t.Fatalf("got %d vals, want 3", len(vals))
	}
	if vals[2] != nil {
		t.Errorf("vals[2] = %v, want nil", vals[2])
	}

	if _, err := rows.Next(ctx); !errors.Is(err, driver.ErrEOF) {
		t.Errorf("expected ErrEOF, got %v", err)
	}
}

func TestIntegration_PG_ServerVersion(t *testing.T) {
	c, creds := pgConnFromEnv(t)
	d, _ := driver.For(dbadmin.EnginePostgres)
	conn, err := d.Open(context.Background(), c, creds, 2)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()

	v, err := conn.ServerVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v, "PostgreSQL") {
		t.Errorf("version doesn't contain 'PostgreSQL': %s", v)
	}
}

func TestIntegration_PG_SyntaxError(t *testing.T) {
	c, creds := pgConnFromEnv(t)
	d, _ := driver.For(dbadmin.EnginePostgres)
	conn, err := d.Open(context.Background(), c, creds, 2)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()

	_, err = conn.Query(context.Background(), driver.Limits{}, "SELEC 1")
	if err == nil {
		t.Fatal("expected syntax error")
	}
	if !errors.Is(err, driver.ErrSyntax) {
		t.Errorf("expected ErrSyntax, got %v", err)
	}
}

func TestIntegration_PG_PermissionError(t *testing.T) {
	c, creds := pgConnFromEnv(t)
	d, _ := driver.For(dbadmin.EnginePostgres)
	conn, err := d.Open(context.Background(), c, creds, 2)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()

	// Try to read pg_authid which is restricted to superuser. If
	// our test user IS superuser, this test is meaningless; skip.
	if os.Getenv("DBADMIN_TEST_PG_USER") == "postgres" {
		t.Skip("test user is superuser; permission check meaningless")
	}
	_, err = conn.Query(context.Background(), driver.Limits{}, "SELECT * FROM pg_authid LIMIT 1")
	if err != nil && errors.Is(err, driver.ErrPermission) {
		// expected
		return
	}
	t.Errorf("expected ErrPermission, got %v", err)
}

// ─── Cross-engine: Pool ──────────────────────────────────────────────

func TestIntegration_Pool_RoundTrip(t *testing.T) {
	c, creds := mysqlConnFromEnv(t)
	resolverID := dbadmin.ConnectionID("test-mysql")
	c.ID = resolverID

	pool := driver.NewPool(driver.PoolOptions{
		Resolver: func(ctx context.Context, id dbadmin.ConnectionID) (*dbadmin.Connection, *dbadmin.Credentials, error) {
			return c, creds, nil
		},
		IdleTimeout: time.Minute,
		PoolSize:    2,
	})
	defer pool.Close()

	conn, release, err := pool.Withdraw(context.Background(), resolverID)
	if err != nil {
		t.Fatalf("Withdraw: %v", err)
	}
	defer release()

	rows, err := conn.Query(context.Background(), driver.Limits{}, "SELECT 42")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	vals, err := rows.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(vals) != 1 {
		t.Fatalf("got %d values, want 1", len(vals))
	}
}

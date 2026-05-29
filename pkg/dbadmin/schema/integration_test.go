package schema_test

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/driver"
	"github.com/auracp/auracp/pkg/dbadmin/schema"
)

// Same env-gating pattern as driver/integration_test.go. CI service
// containers populate the variables; local dev runs are skipped unless
// the operator sets them.

func skipUnless(t *testing.T, vars ...string) {
	t.Helper()
	for _, v := range vars {
		if os.Getenv(v) == "" {
			t.Skipf("integration test requires %s", v)
		}
	}
}

func openMySQL(t *testing.T) driver.Conn {
	t.Helper()
	skipUnless(t, "DBADMIN_TEST_MYSQL_HOST", "DBADMIN_TEST_MYSQL_PORT",
		"DBADMIN_TEST_MYSQL_USER", "DBADMIN_TEST_MYSQL_PASS", "DBADMIN_TEST_MYSQL_DB")
	port, _ := strconv.Atoi(os.Getenv("DBADMIN_TEST_MYSQL_PORT"))
	d, _ := driver.For(dbadmin.EngineMariaDB)
	conn, err := d.Open(context.Background(), &dbadmin.Connection{
		Engine:   dbadmin.EngineMariaDB,
		Host:     os.Getenv("DBADMIN_TEST_MYSQL_HOST"),
		Port:     port,
		Database: os.Getenv("DBADMIN_TEST_MYSQL_DB"),
		Username: os.Getenv("DBADMIN_TEST_MYSQL_USER"),
		UseSSL:   false,
	}, &dbadmin.Credentials{
		Password: os.Getenv("DBADMIN_TEST_MYSQL_PASS"),
	}, 2)
	if err != nil {
		t.Fatalf("driver.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func openPG(t *testing.T) driver.Conn {
	t.Helper()
	skipUnless(t, "DBADMIN_TEST_PG_HOST", "DBADMIN_TEST_PG_PORT",
		"DBADMIN_TEST_PG_USER", "DBADMIN_TEST_PG_PASS", "DBADMIN_TEST_PG_DB")
	port, _ := strconv.Atoi(os.Getenv("DBADMIN_TEST_PG_PORT"))
	d, _ := driver.For(dbadmin.EnginePostgres)
	conn, err := d.Open(context.Background(), &dbadmin.Connection{
		Engine:   dbadmin.EnginePostgres,
		Host:     os.Getenv("DBADMIN_TEST_PG_HOST"),
		Port:     port,
		Database: os.Getenv("DBADMIN_TEST_PG_DB"),
		Username: os.Getenv("DBADMIN_TEST_PG_USER"),
		SSLMode:  "disable",
	}, &dbadmin.Credentials{
		Password: os.Getenv("DBADMIN_TEST_PG_PASS"),
	}, 2)
	if err != nil {
		t.Fatalf("driver.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestIntegration_MySQL_ListDatabases(t *testing.T) {
	c := openMySQL(t)
	r, _ := schema.For(c, dbadmin.EngineMariaDB)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dbs, err := r.ListDatabases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(dbs) == 0 {
		t.Error("expected at least one user database")
	}
	for _, d := range dbs {
		if d == "information_schema" || d == "mysql" {
			t.Errorf("system database %q leaked into ListDatabases", d)
		}
	}
}

func TestIntegration_MySQL_GetTable_information_schema_tables(t *testing.T) {
	// information_schema is read-only and stable across MariaDB
	// versions; we use one of its tables as a known-good fixture
	// for a real GetTable round-trip.
	c := openMySQL(t)
	r, _ := schema.For(c, dbadmin.EngineMariaDB)
	tbl, err := r.GetTable(context.Background(), "information_schema", "TABLES")
	if err != nil {
		t.Fatal(err)
	}
	if len(tbl.Columns) == 0 {
		t.Error("information_schema.TABLES returned 0 columns")
	}
	wantCols := []string{"TABLE_SCHEMA", "TABLE_NAME", "TABLE_TYPE"}
	have := map[string]bool{}
	for _, c := range tbl.Columns {
		have[c.Name] = true
	}
	for _, w := range wantCols {
		if !have[w] {
			t.Errorf("expected column %q in information_schema.TABLES", w)
		}
	}
}

func TestIntegration_PG_ListSchemas(t *testing.T) {
	c := openPG(t)
	r, _ := schema.For(c, dbadmin.EnginePostgres)
	schemas, err := r.ListSchemas(context.Background(), os.Getenv("DBADMIN_TEST_PG_DB"))
	if err != nil {
		t.Fatal(err)
	}
	if len(schemas) == 0 {
		t.Error("expected at least one schema (public, usually)")
	}
	for _, s := range schemas {
		if s == "pg_catalog" || s == "information_schema" {
			t.Errorf("system schema %q leaked", s)
		}
	}
}

func TestIntegration_PG_GetTable_pg_class(t *testing.T) {
	c := openPG(t)
	r, _ := schema.For(c, dbadmin.EnginePostgres)
	tbl, err := r.GetTable(context.Background(), "pg_catalog", "pg_class")
	if err != nil {
		t.Fatal(err)
	}
	if len(tbl.Columns) == 0 {
		t.Error("pg_class returned 0 columns")
	}
	have := map[string]bool{}
	for _, c := range tbl.Columns {
		have[c.Name] = true
	}
	for _, w := range []string{"oid", "relname", "relkind"} {
		if !have[w] {
			t.Errorf("expected column %q in pg_class", w)
		}
	}
}

func TestIntegration_MySQL_Cache_RoundTrip(t *testing.T) {
	c := openMySQL(t)
	r, _ := schema.For(c, dbadmin.EngineMariaDB)
	cache := schema.NewCache(r, schema.CacheConfig{TTL: 5 * time.Minute, MaxEntries: 100})

	// First call hits the DB; second should be cached.
	dbs1, err := cache.ListDatabases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	dbs2, err := cache.ListDatabases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(dbs1) != len(dbs2) {
		t.Errorf("cache returned different lengths: %d vs %d", len(dbs1), len(dbs2))
	}
	cache.Invalidate("")
	dbs3, err := cache.ListDatabases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(dbs3) != len(dbs1) {
		t.Errorf("after invalidate, list differs: %v vs %v", dbs1, dbs3)
	}
}

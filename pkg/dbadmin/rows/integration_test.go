package rows_test

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/driver"
	"github.com/auracp/auracp/pkg/dbadmin/rows"
	"github.com/auracp/auracp/pkg/dbadmin/schema"
)

// Env-gated integration tests against real MariaDB + Postgres. Run with:
//
//	export DBADMIN_TEST_MYSQL_HOST=127.0.0.1 ... etc
//	go test -count=1 ./pkg/dbadmin/rows/

func envSkip(t *testing.T, vars ...string) {
	t.Helper()
	for _, v := range vars {
		if os.Getenv(v) == "" {
			t.Skipf("integration test requires %s", v)
		}
	}
}

func mysqlOperator(t *testing.T) (*rows.Operator, driver.Conn) {
	t.Helper()
	envSkip(t, "DBADMIN_TEST_MYSQL_HOST", "DBADMIN_TEST_MYSQL_PORT",
		"DBADMIN_TEST_MYSQL_USER", "DBADMIN_TEST_MYSQL_PASS", "DBADMIN_TEST_MYSQL_DB")
	port, _ := strconv.Atoi(os.Getenv("DBADMIN_TEST_MYSQL_PORT"))

	d, _ := driver.For(dbadmin.EngineMariaDB)
	conn, err := d.Open(context.Background(), &dbadmin.Connection{
		Engine:   dbadmin.EngineMariaDB,
		Host:     os.Getenv("DBADMIN_TEST_MYSQL_HOST"),
		Port:     port,
		Database: os.Getenv("DBADMIN_TEST_MYSQL_DB"),
		Username: os.Getenv("DBADMIN_TEST_MYSQL_USER"),
	}, &dbadmin.Credentials{Password: os.Getenv("DBADMIN_TEST_MYSQL_PASS")}, 2)
	if err != nil {
		t.Fatalf("driver.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	reader, _ := schema.For(conn, dbadmin.EngineMariaDB)
	op, err := rows.New(conn, reader, rows.Options{
		Limits: driver.Limits{MaxRows: 100, Timeout: 10 * time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	return op, conn
}

func pgOperator(t *testing.T) (*rows.Operator, driver.Conn) {
	t.Helper()
	envSkip(t, "DBADMIN_TEST_PG_HOST", "DBADMIN_TEST_PG_PORT",
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
	}, &dbadmin.Credentials{Password: os.Getenv("DBADMIN_TEST_PG_PASS")}, 2)
	if err != nil {
		t.Fatalf("driver.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	reader, _ := schema.For(conn, dbadmin.EnginePostgres)
	op, err := rows.New(conn, reader, rows.Options{
		Limits: driver.Limits{MaxRows: 100, Timeout: 10 * time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	return op, conn
}

// ─── MySQL integration ──────────────────────────────────────────────

func TestIntegration_MySQL_ReadInsertUpdateDelete(t *testing.T) {
	op, conn := mysqlOperator(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create scratch table.
	tableName := "aura_rows_it_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	createSQL := "CREATE TABLE " + tableName + " (id INT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(64) NOT NULL)"
	if _, err := conn.Exec(ctx, driver.Limits{Timeout: 10 * time.Second}, createSQL); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	defer conn.Exec(ctx, driver.Limits{Timeout: 5 * time.Second}, "DROP TABLE "+tableName)

	dbName := os.Getenv("DBADMIN_TEST_MYSQL_DB")

	// Insert.
	r, err := op.Insert(ctx, rows.InsertOpts{
		Schema: dbName, Table: tableName,
		Values: map[string]any{"name": "alice"},
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if r.LastInsertID == 0 {
		t.Error("Insert.LastInsertID is 0")
	}

	// Read.
	rr, err := op.Read(ctx, rows.ReadOpts{
		Schema: dbName, Table: tableName,
		Columns: []string{"id", "name"}, Limit: 10,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(rr.Rows) != 1 {
		t.Errorf("Read returned %d rows, want 1", len(rr.Rows))
	}

	// Update.
	id := r.LastInsertID
	_, err = op.UpdateByPK(ctx, rows.UpdateByPKOpts{
		Schema: dbName, Table: tableName,
		PK:  map[string]any{"id": id},
		Set: map[string]any{"name": "bob"},
	})
	if err != nil {
		t.Fatalf("UpdateByPK: %v", err)
	}

	// Verify update.
	rr, _ = op.Read(ctx, rows.ReadOpts{
		Schema: dbName, Table: tableName,
		Columns: []string{"name"},
		Filter:  []rows.Predicate{{Column: "id", Op: rows.OpEq, Value: id}},
		Limit:   10,
	})
	if len(rr.Rows) != 1 {
		t.Fatalf("post-update Read returned %d rows", len(rr.Rows))
	}
	got, _ := rr.Rows[0][0].(string)
	if got != "bob" {
		t.Errorf("post-update name = %q, want 'bob'", got)
	}

	// Delete.
	dr, err := op.DeleteByPK(ctx, rows.DeleteByPKOpts{
		Schema: dbName, Table: tableName,
		PK: map[string]any{"id": id},
	})
	if err != nil {
		t.Fatalf("DeleteByPK: %v", err)
	}
	if dr.RowsAffected != 1 {
		t.Errorf("Delete.RowsAffected = %d, want 1", dr.RowsAffected)
	}
}

// ─── Postgres integration ──────────────────────────────────────────────

func TestIntegration_PG_ReadInsertUpdateDelete(t *testing.T) {
	op, conn := pgOperator(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tableName := "aura_rows_it_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	createSQL := "CREATE TABLE public." + tableName + " (id SERIAL PRIMARY KEY, name TEXT NOT NULL)"
	if _, err := conn.Exec(ctx, driver.Limits{Timeout: 10 * time.Second}, createSQL); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	defer conn.Exec(ctx, driver.Limits{Timeout: 5 * time.Second}, "DROP TABLE public."+tableName)

	// Insert.
	_, err := op.Insert(ctx, rows.InsertOpts{
		Schema: "public", Table: tableName,
		Values: map[string]any{"name": "alice"},
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Read.
	rr, err := op.Read(ctx, rows.ReadOpts{
		Schema: "public", Table: tableName,
		Columns: []string{"id", "name"}, Limit: 10,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(rr.Rows) != 1 {
		t.Errorf("Read returned %d rows, want 1", len(rr.Rows))
	}

	id := rr.Rows[0][0]

	// Update.
	_, err = op.UpdateByPK(ctx, rows.UpdateByPKOpts{
		Schema: "public", Table: tableName,
		PK:  map[string]any{"id": id},
		Set: map[string]any{"name": "bob"},
	})
	if err != nil {
		t.Fatalf("UpdateByPK: %v", err)
	}

	// Delete.
	dr, err := op.DeleteByPK(ctx, rows.DeleteByPKOpts{
		Schema: "public", Table: tableName,
		PK: map[string]any{"id": id},
	})
	if err != nil {
		t.Fatalf("DeleteByPK: %v", err)
	}
	if dr.RowsAffected != 1 {
		t.Errorf("Delete.RowsAffected = %d, want 1", dr.RowsAffected)
	}
}

func TestIntegration_PG_NoPK_Refused(t *testing.T) {
	op, conn := pgOperator(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tableName := "aura_rows_nopk_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	createSQL := "CREATE TABLE public." + tableName + " (id INT, name TEXT)"
	if _, err := conn.Exec(ctx, driver.Limits{Timeout: 10 * time.Second}, createSQL); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	defer conn.Exec(ctx, driver.Limits{Timeout: 5 * time.Second}, "DROP TABLE public."+tableName)

	_, err := op.UpdateByPK(ctx, rows.UpdateByPKOpts{
		Schema: "public", Table: tableName,
		PK:  map[string]any{"id": 1},
		Set: map[string]any{"name": "x"},
	})
	if !errors.Is(err, rows.ErrNoPrimaryKey) {
		t.Errorf("err = %v, want ErrNoPrimaryKey", err)
	}
}

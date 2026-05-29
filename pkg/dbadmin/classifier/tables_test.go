package classifier_test

import (
	"testing"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/classifier"
)

// TestTables_Dedupe asserts that the AST collector dedupes
// (schema, object) pairs. A self-join references the same table twice
// in the FROM clause but Tables[] must list it once.
func TestTables_Dedupe(t *testing.T) {
	sql := `SELECT a.id, b.id FROM users a JOIN users b ON a.parent_id=b.id`
	pq, err := classifier.Classify(dbadmin.EngineMariaDB, sql)
	if err != nil {
		t.Fatal(err)
	}
	if len(pq.Statements) == 0 {
		t.Fatal("no statements")
	}
	tables := pq.Statements[0].Tables
	if len(tables) != 1 {
		t.Errorf("Tables count = %d, want 1 (self-join dedupe failure): %+v", len(tables), tables)
	}
	if tables[0].Object != "users" {
		t.Errorf("table object = %q, want users", tables[0].Object)
	}
}

// TestTables_PostgresSchemaQualified covers the canonical Postgres
// case where Schema is populated independently of Object.
func TestTables_PostgresSchemaQualified(t *testing.T) {
	sql := `SELECT * FROM public.users JOIN public.orders ON users.id=orders.uid`
	pq, err := classifier.Classify(dbadmin.EnginePostgres, sql)
	if err != nil {
		t.Fatal(err)
	}
	if pq.ParseSource == classifier.ParseSourceFallback {
		t.Skipf("Postgres AST disabled in this build")
	}
	tables := pq.Statements[0].Tables
	got := map[string]string{}
	for _, tg := range tables {
		got[tg.Object] = tg.Schema
	}
	if got["users"] != "public" {
		t.Errorf("users.Schema = %q, want public", got["users"])
	}
	if got["orders"] != "public" {
		t.Errorf("orders.Schema = %q, want public", got["orders"])
	}
}

// TestTables_MultiStatementPerStmtAssignment confirms that each
// statement's Tables[] reflects only its own touch list — no leakage
// from prior statements in a multi-statement query.
func TestTables_MultiStatementPerStmtAssignment(t *testing.T) {
	sql := `SELECT 1; INSERT INTO t VALUES (1); UPDATE u SET x=1 WHERE id=1`
	pq, err := classifier.Classify(dbadmin.EngineMariaDB, sql)
	if err != nil {
		t.Fatal(err)
	}
	if len(pq.Statements) != 3 {
		t.Fatalf("got %d statements, want 3", len(pq.Statements))
	}
	if len(pq.Statements[0].Tables) != 0 {
		t.Errorf("stmt[0].Tables = %+v, want empty", pq.Statements[0].Tables)
	}
	if len(pq.Statements[1].Tables) != 1 || pq.Statements[1].Tables[0].Object != "t" {
		t.Errorf("stmt[1].Tables = %+v, want [t]", pq.Statements[1].Tables)
	}
	if len(pq.Statements[2].Tables) != 1 || pq.Statements[2].Tables[0].Object != "u" {
		t.Errorf("stmt[2].Tables = %+v, want [u]", pq.Statements[2].Tables)
	}
}

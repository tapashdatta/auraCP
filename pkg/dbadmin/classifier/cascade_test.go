package classifier_test

import (
	"testing"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/classifier"
)

// TestCascade_MetricIncrementsOnFallback asserts that the global
// AST→tokenizer fallback counter advances when the AST parser fails.
func TestCascade_MetricIncrementsOnFallback(t *testing.T) {
	before := classifier.ASTFallbackTotal()
	// GRANT is UNUSED in vitess → guaranteed fallback.
	_, err := classifier.Classify(dbadmin.EngineMariaDB, `GRANT SELECT ON *.* TO 'alice'@'%'`)
	if err != nil {
		t.Fatal(err)
	}
	after := classifier.ASTFallbackTotal()
	if after <= before {
		t.Errorf("ASTFallbackTotal did not advance (before=%d after=%d)", before, after)
	}
}

// TestCascade_AggregateParseSource cycles the aggregate states:
// ParseSourceAST when all statements parsed by the AST,
// ParseSourceFallback when all fell back, and ParseSourceMixed when
// only some did.
func TestCascade_AggregateParseSource(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want classifier.ParseSource
	}{
		{"all AST", "SELECT 1; SELECT 2", classifier.ParseSourceAST},
		{"all fallback (GRANT/REVOKE)", `GRANT SELECT ON *.* TO 'a'@'%'; REVOKE ALL ON *.* FROM 'a'@'%'`, classifier.ParseSourceFallback},
		{"mixed", `SELECT 1; GRANT SELECT ON *.* TO 'a'@'%'`, classifier.ParseSourceMixed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pq, err := classifier.Classify(dbadmin.EngineMariaDB, c.sql)
			if err != nil {
				t.Fatal(err)
			}
			if pq.ParseSource != c.want {
				t.Errorf("aggregate ParseSource = %v, want %v", pq.ParseSource, c.want)
			}
		})
	}
}

// TestCascade_ForbiddenWinsOverASTRead verifies the SECURITY.md §6.3.2
// no-override property: the AST classifies the query as ClassRead, but
// the forbidden matcher promotes it to ClassForbidden — and the AST's
// read result is dropped.
func TestCascade_ForbiddenWinsOverASTRead(t *testing.T) {
	// LOAD_FILE(...) parses cleanly as a function call in vitess —
	// AST sees ClassRead. matchForbidden sees LOAD_FILE( and fires.
	sql := `SELECT LOAD_FILE('/etc/passwd')`
	pq, err := classifier.Classify(dbadmin.EngineMariaDB, sql)
	if err != nil {
		t.Fatal(err)
	}
	if pq.Class != classifier.ClassForbidden {
		t.Errorf("class = %v, want ClassForbidden", pq.Class)
	}
	if pq.Statements[0].Action != "" {
		t.Errorf("Action = %q, want empty (forbidden has no action)", pq.Statements[0].Action)
	}
}

// TestCascade_TooLargeStillRejected confirms the byte-size guardrail
// fires before the AST parser sees the input.
func TestCascade_TooLargeStillRejected(t *testing.T) {
	big := make([]byte, 17*1024*1024)
	for i := range big {
		big[i] = 'a'
	}
	_, err := classifier.Classify(dbadmin.EngineMariaDB, string(big))
	if err == nil {
		t.Error("expected ErrTooLarge, got nil")
	}
}

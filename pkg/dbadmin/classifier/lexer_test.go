package classifier

import (
	"testing"
)

func TestLex_BasicTokens(t *testing.T) {
	tokens := Lex("SELECT 1", LexOptions{Dialect: DialectMySQL})
	if len(tokens) != 3 {
		t.Fatalf("got %d tokens, want 3 (IDENT NUMBER EOF), got %v", len(tokens), tokens)
	}
	if tokens[0].Kind != TokIdent || tokens[0].Text != "SELECT" {
		t.Errorf("token[0] = %+v, want IDENT SELECT", tokens[0])
	}
	if tokens[1].Kind != TokNumber || tokens[1].Text != "1" {
		t.Errorf("token[1] = %+v, want NUMBER 1", tokens[1])
	}
	if tokens[2].Kind != TokEOF {
		t.Errorf("token[2] = %+v, want EOF", tokens[2])
	}
}

func TestLex_StringLiterals(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []TokenKind
	}{
		{"simple", "'hello'", []TokenKind{TokString, TokEOF}},
		{"escape", "'it''s'", []TokenKind{TokString, TokEOF}},
		{"with mysql backslash", `'one\'two'`, []TokenKind{TokString, TokEOF}},
		{"empty", "''", []TokenKind{TokString, TokEOF}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tokens := Lex(c.sql, LexOptions{Dialect: DialectMySQL})
			if len(tokens) != len(c.want) {
				t.Fatalf("got %d tokens, want %d, tokens=%v", len(tokens), len(c.want), tokens)
			}
			for i, want := range c.want {
				if tokens[i].Kind != want {
					t.Errorf("token[%d].Kind = %v, want %v", i, tokens[i].Kind, want)
				}
			}
		})
	}
}

func TestLex_Comments_AreSkipped(t *testing.T) {
	cases := []struct {
		name string
		sql  string
	}{
		{"line comment", "SELECT -- hidden\n1"},
		{"block comment", "SELECT /* hidden */ 1"},
		{"unterminated block", "SELECT /* never closed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tokens := Lex(c.sql, LexOptions{Dialect: DialectMySQL})
			// Find every non-EOF token; ensure no TokComment present.
			for _, tok := range tokens {
				if tok.Kind == TokComment {
					t.Errorf("got TokComment, expected comment stripped: %+v", tok)
				}
			}
		})
	}
}

func TestLex_Comments_PreservedWhenAsked(t *testing.T) {
	tokens := Lex("SELECT /* x */ 1", LexOptions{Dialect: DialectMySQL, KeepComments: true})
	sawComment := false
	for _, tok := range tokens {
		if tok.Kind == TokComment {
			sawComment = true
			break
		}
	}
	if !sawComment {
		t.Error("KeepComments=true didn't yield a TokComment")
	}
}

func TestLex_StringDoesntTerminateOnSemicolon(t *testing.T) {
	// This is the regex-classifier killer: a `;` inside a string must
	// not split the statement.
	sql := "SELECT 'a;b;c' AS s"
	tokens := Lex(sql, LexOptions{Dialect: DialectMySQL})
	// Find every TokPunct=";"; there must be none.
	for _, tok := range tokens {
		if tok.Kind == TokPunct && tok.Text == ";" {
			t.Errorf("unexpected semicolon token inside string literal: %+v", tok)
		}
	}
}

func TestLex_QuotedIdent_MySQL_Backtick(t *testing.T) {
	tokens := Lex("SELECT `LOAD_FILE` FROM t", LexOptions{Dialect: DialectMySQL})
	// The backtick-quoted LOAD_FILE must come through as
	// TokQuotedIdent, NOT TokIdent. This is the test that proves a
	// forbidden function name inside a quoted identifier doesn't fire
	// the forbidden matcher (which only inspects TokIdent).
	for _, tok := range tokens {
		if tok.Kind == TokIdent && upper(tok.Text) == "LOAD_FILE" {
			t.Errorf("backtick-quoted LOAD_FILE leaked as TokIdent: %+v", tok)
		}
	}
}

func TestLex_QuotedIdent_Postgres_DoubleQuote(t *testing.T) {
	tokens := Lex(`SELECT "pg_read_file" FROM t`, LexOptions{Dialect: DialectPostgres})
	for _, tok := range tokens {
		if tok.Kind == TokIdent && upper(tok.Text) == "PG_READ_FILE" {
			t.Errorf("double-quoted pg_read_file leaked as TokIdent: %+v", tok)
		}
	}
}

func TestLex_PostgresDollarString(t *testing.T) {
	sql := `SELECT $body$ ; LOAD_FILE('/etc/shadow') $body$`
	tokens := Lex(sql, LexOptions{Dialect: DialectPostgres})
	// The dollar-quoted body must come through as a single
	// TokDollarString — meaning LOAD_FILE and the embedded `;` are
	// invisible to subsequent matching passes.
	foundDollar := false
	for _, tok := range tokens {
		if tok.Kind == TokDollarString {
			foundDollar = true
		}
		if tok.Kind == TokIdent && upper(tok.Text) == "LOAD_FILE" {
			t.Errorf("LOAD_FILE leaked from dollar-quoted string: %+v", tok)
		}
		if tok.Kind == TokPunct && tok.Text == ";" {
			t.Errorf("semicolon leaked from dollar-quoted string: %+v", tok)
		}
	}
	if !foundDollar {
		t.Error("dollar-quoted string didn't produce a TokDollarString")
	}
}

func TestLex_PostgresNumberedParameter(t *testing.T) {
	tokens := Lex("SELECT * FROM t WHERE x = $1 AND y = $42", LexOptions{Dialect: DialectPostgres})
	count := 0
	for _, tok := range tokens {
		if tok.Kind == TokParameter {
			count++
		}
	}
	if count != 2 {
		t.Errorf("got %d TokParameter, want 2", count)
	}
}

func TestLex_MultiCharOperators(t *testing.T) {
	tokens := Lex("a != b OR c <= d OR e || f", LexOptions{Dialect: DialectMySQL})
	ops := []string{"!=", "<=", "||"}
	idx := 0
	for _, tok := range tokens {
		if tok.Kind != TokOperator {
			continue
		}
		if tok.Text == ops[idx] {
			idx++
			if idx == len(ops) {
				break
			}
		}
	}
	if idx != len(ops) {
		t.Errorf("got %d/%d multi-char operators matched", idx, len(ops))
	}
}

func TestLex_StatementSeparator(t *testing.T) {
	tokens := Lex("SELECT 1; DROP TABLE foo;", LexOptions{Dialect: DialectMySQL})
	semis := 0
	for _, tok := range tokens {
		if tok.Kind == TokPunct && tok.Text == ";" {
			semis++
		}
	}
	if semis != 2 {
		t.Errorf("got %d semicolons, want 2", semis)
	}
}

func TestLex_NumberFormats(t *testing.T) {
	cases := []string{"1", "42", "3.14", "0.5", ".5", "1e10", "1.5e-3", "0xff" /*hex falls through as ident*/}
	for _, c := range cases {
		tokens := Lex(c, LexOptions{Dialect: DialectMySQL})
		if len(tokens) < 2 {
			t.Errorf("Lex(%q) returned %d tokens, want >= 2", c, len(tokens))
		}
	}
}

func TestUpper_FastPath(t *testing.T) {
	// upper() is on the hot path — verify it returns the input
	// unchanged when already upper (no allocation in the common case).
	cases := []struct {
		in, want string
	}{
		{"SELECT", "SELECT"},
		{"select", "SELECT"},
		{"Select", "SELECT"},
		{"", ""},
		{"FROM_TABLE", "FROM_TABLE"},
	}
	for _, c := range cases {
		if got := upper(c.in); got != c.want {
			t.Errorf("upper(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

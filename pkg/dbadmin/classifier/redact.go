package classifier

import (
	"strings"
)

// RedactSensitiveInline returns a copy of the SQL with sensitive
// inline values redacted. Targets patterns that historically expose
// credentials in audit logs:
//
//   - CREATE USER ... IDENTIFIED BY '<password>'
//   - ALTER USER ... IDENTIFIED BY '<password>'
//   - CREATE ROLE ... PASSWORD '<password>'  (Postgres)
//   - ALTER ROLE ... WITH PASSWORD '<password>'  (Postgres)
//   - CREATE USER ... WITH PASSWORD '<password>'  (Postgres)
//   - MariaDB IDENTIFIED VIA <plugin> AS '<hash>'  (PR #7.5 H4)
//   - MariaDB IDENTIFIED VIA <plugin> USING '<hash>'  (PR #7.5 H4)
//   - Postgres CREATE/ALTER SUBSCRIPTION … CONNECTION '<dsn>'  (PR #7.5 H4)
//   - dblink_connect[_u]('<host=… password=… dsn>')  (PR #7.5 H4)
//   - COPY … FROM/TO PROGRAM '<shell command>'  (PR #7.5 H4)
//   - Any string literal containing a credentialed URI scheme:
//     postgresql://user:pw@…, mysql://, mongodb://, mariadb://, redis://,
//     postgres://, mongodb+srv://  (PR #7.5 H4)
//
// The string literal following the trigger keyword is replaced with
// '[redacted]'. The rest of the statement is preserved verbatim so
// audit readers can still see the affected username / role / target.
//
// Operates on the raw SQL string (not the token stream) because audit
// records preserve the operator's formatting. Re-tokenizes internally
// to find the redaction targets without regex.
//
// The post-pass URI sweep runs over every string literal in the
// statement regardless of dialect — `CONNECTION 'postgresql://u:p@h/d'`
// inside a non-CREATE-SUBSCRIPTION position (e.g. a user-defined
// foreign-data wrapper option, a future Postgres syntax we haven't
// special-cased, or a MariaDB FEDERATED `CONNECTION='mysql://u:p@…'`)
// still leaks credentials. The sweep redacts the whole literal because
// any literal carrying a credentialed URI is presumed sensitive — we
// can't safely surgically excise just the userinfo without altering
// the visible structure of the statement.
func RedactSensitiveInline(sql string, dialect Dialect) string {
	tokens := Lex(sql, LexOptions{Dialect: dialect, KeepComments: false})

	// Walk tokens looking for trigger patterns and record the byte
	// ranges to redact. Ranges may overlap (a CONNECTION '<dsn>' that
	// also matches the URI-scheme sweep is fine — applyRedactions
	// dedupes overlaps).
	var redactions []redaction

	for i, t := range tokens {
		if t.Kind != TokIdent {
			continue
		}
		up := upper(t.Text)

		// Pattern 1: IDENTIFIED BY '<pw>'  or  IDENTIFIED VIA <plugin> AS / USING '<hash>'
		if up == "IDENTIFIED" {
			next := nextIdent(tokens, i+1)
			if next == nil {
				continue
			}
			nextUp := upper(next.Text)
			nextIdx := indexOf(tokens, next)

			switch nextUp {
			case "BY":
				if str := nextString(tokens, nextIdx+1); str != nil {
					redactions = append(redactions, redaction{str.StartByte, str.EndByte})
				}
			case "VIA":
				// IDENTIFIED VIA <plugin> [AS|USING] '<hash>'
				// The plugin name is a bare identifier; the hash
				// lives in the next string literal. We walk forward
				// past the plugin identifier, look for AS / USING,
				// then take the next string.
				plugin := nextIdent(tokens, nextIdx+1)
				if plugin == nil {
					continue
				}
				pluginIdx := indexOf(tokens, plugin)
				marker := nextIdent(tokens, pluginIdx+1)
				if marker == nil {
					continue
				}
				mUp := upper(marker.Text)
				if mUp != "AS" && mUp != "USING" {
					// IDENTIFIED VIA pam (no hash) — nothing to
					// redact, and we don't want to grab an
					// unrelated downstream string literal.
					continue
				}
				if str := nextString(tokens, indexOf(tokens, marker)+1); str != nil {
					redactions = append(redactions, redaction{str.StartByte, str.EndByte})
				}
			}
			continue
		}

		// Pattern 2: PASSWORD '<pw>'  (Postgres-style)
		if up == "PASSWORD" {
			if str := nextString(tokens, i+1); str != nil {
				redactions = append(redactions, redaction{str.StartByte, str.EndByte})
			}
			continue
		}

		// Pattern 3: CONNECTION '<dsn>'  (Postgres CREATE/ALTER
		// SUBSCRIPTION; MariaDB FEDERATED CONNECTION='…'). The next
		// non-ident token may be `=` (FEDERATED uses `CONNECTION=`);
		// nextString walks past punctuation/operators happily.
		if up == "CONNECTION" {
			if str := nextString(tokens, i+1); str != nil {
				redactions = append(redactions, redaction{str.StartByte, str.EndByte})
			}
			continue
		}

		// Pattern 4: dblink_connect('<dsn>') and dblink_connect_u('<dsn>').
		// The function name itself appears as an IDENT immediately
		// before a '('. The DSN is the first string argument. There
		// is an optional connname string before the dsn —
		// dblink_connect('name', 'host=… password=…') — so we
		// redact the FIRST string argument that looks like a
		// connection string and also the SECOND if present (the
		// safe choice; a connname has no credentials but also nothing
		// privacy-sensitive about replacing it with [redacted]).
		// Practical impact: redact every string literal inside the
		// argument list up to the matching ')'.
		if up == "DBLINK_CONNECT" || up == "DBLINK_CONNECT_U" {
			// Confirm next non-whitespace token is '('.
			next := i + 1
			if next < len(tokens) && tokens[next].Kind == TokPunct && tokens[next].Text == "(" {
				depth := 1
				for j := next + 1; j < len(tokens) && depth > 0; j++ {
					tj := tokens[j]
					if statementEndsHere(tj) {
						break
					}
					if tj.Kind == TokPunct && tj.Text == "(" {
						depth++
					} else if tj.Kind == TokPunct && tj.Text == ")" {
						depth--
						if depth == 0 {
							break
						}
					}
					if tj.Kind == TokString || tj.Kind == TokDollarString {
						redactions = append(redactions, redaction{tj.StartByte, tj.EndByte})
					}
				}
			}
			continue
		}

		// Pattern 5: COPY … FROM PROGRAM '<cmd>'  /  COPY … TO PROGRAM '<cmd>'
		// The classifier already classes this as ClassForbidden, but
		// the operator's failed attempt still flows through history
		// (audit row written before driver refusal). The shell
		// command may carry credentials (curl -u user:pw, ssh
		// user@host, etc.). Redact the program string outright.
		if up == "PROGRAM" {
			// Confirm the prior non-whitespace ident was FROM or TO,
			// and walk-back further to confirm COPY context. Cheap:
			// inspect at most two prior IDENT tokens.
			prevIdent := prevIdentInStmt(tokens, i-1)
			if prevIdent == nil {
				continue
			}
			pup := upper(prevIdent.Text)
			if pup != "FROM" && pup != "TO" {
				continue
			}
			// Optional further-back COPY check — we don't require it
			// because some PROGRAM-bearing extension syntax may also
			// want redaction; the false-positive cost (redacting an
			// unrelated string literal after FROM PROGRAM ident) is
			// preferred over leaking a credentialed shell command.
			if str := nextString(tokens, i+1); str != nil {
				redactions = append(redactions, redaction{str.StartByte, str.EndByte})
			}
			continue
		}
	}

	// Post-pass: scan every string literal for credentialed URI
	// schemes. Catches CONNECTION-less leakage (FDW options, a
	// MariaDB FEDERATED CONNECTION='mysql://…' that the CONNECTION
	// pattern above already nailed but this catches the case where
	// the URI sits inside a comment-stripped string we didn't reach
	// via a keyword anchor — e.g. a future syntax we haven't
	// special-cased, or a literal in a CALL / SELECT statement).
	for i := range tokens {
		t := tokens[i]
		if t.Kind != TokString && t.Kind != TokDollarString {
			continue
		}
		if containsCredentialedURI(t.Text) {
			redactions = append(redactions, redaction{t.StartByte, t.EndByte})
		}
	}

	if len(redactions) == 0 {
		return sql
	}

	return applyRedactions(sql, redactions)
}

// redaction is one byte range in the original SQL slated for
// replacement with '[redacted]'.
type redaction struct {
	start, end int
}

// applyRedactions replaces every byte range with '[redacted]'.
// Overlapping or duplicate ranges are merged (same range emitted by
// multiple patterns — e.g. CONNECTION '<dsn>' that the URI sweep also
// catches — should produce one '[redacted]', not two). Ranges are
// applied in reverse-start order so earlier byte offsets stay valid.
func applyRedactions(sql string, redactions []redaction) string {
	// Sort by start ascending, then merge overlaps. Insertion sort is
	// fine (n is tiny in practice — single-digit per statement).
	for i := 1; i < len(redactions); i++ {
		for j := i; j > 0 && redactions[j-1].start > redactions[j].start; j-- {
			redactions[j-1], redactions[j] = redactions[j], redactions[j-1]
		}
	}
	merged := redactions[:0]
	for _, r := range redactions {
		if len(merged) > 0 && r.start <= merged[len(merged)-1].end {
			if r.end > merged[len(merged)-1].end {
				merged[len(merged)-1].end = r.end
			}
			continue
		}
		merged = append(merged, r)
	}

	out := sql
	for i := len(merged) - 1; i >= 0; i-- {
		r := merged[i]
		out = out[:r.start] + "'[redacted]'" + out[r.end:]
	}
	return out
}

// containsCredentialedURI checks whether the string literal text
// (including its surrounding quotes) carries any of the known DB-ish
// URI schemes with a userinfo component. We're permissive — anything
// with "://" preceded by one of the recognized scheme tokens and
// containing an "@" before the next slash counts.
//
// Recognized schemes: postgresql, postgres, mysql, mariadb, mongodb,
// mongodb+srv, redis, rediss. The check is lowercased substring +
// boundary verification so a string like "see postgresql://docs" won't
// false-positive (no userinfo, no @ before the next /).
//
// We accept a small false-positive cost: a string like
// "Re: postgresql://admin@docs" in a comment-as-string passed through
// the redactor would be redacted. The audit trail still tells the
// operator a sensitive-looking statement ran; the username portion is
// lost but that's the conservative choice.
func containsCredentialedURI(s string) bool {
	ls := strings.ToLower(s)
	// Quick reject if no scheme separator at all.
	if !strings.Contains(ls, "://") {
		return false
	}
	schemes := []string{
		"postgresql://",
		"postgres://",
		"mysql://",
		"mariadb://",
		"mongodb+srv://",
		"mongodb://",
		"redis://",
		"rediss://",
	}
	for _, sch := range schemes {
		idx := strings.Index(ls, sch)
		if idx < 0 {
			continue
		}
		// Verify the scheme isn't a substring of a longer
		// identifier — the byte before the scheme must be a
		// non-alphabetic boundary (start of string, quote, space,
		// equals, etc.).
		if idx > 0 {
			b := ls[idx-1]
			if (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '+' || b == '.' || b == '-' {
				continue
			}
		}
		// Check for userinfo: an '@' before the next '/' or end.
		rest := ls[idx+len(sch):]
		slash := strings.IndexByte(rest, '/')
		if slash < 0 {
			slash = len(rest)
		}
		if strings.IndexByte(rest[:slash], '@') >= 0 {
			return true
		}
	}
	return false
}

// nextIdent returns the next IDENT token at or after index start, or nil
// if none. Skips comments + whitespace (which the lexer omits anyway).
func nextIdent(tokens []Token, start int) *Token {
	for i := start; i < len(tokens); i++ {
		if tokens[i].Kind == TokIdent {
			return &tokens[i]
		}
		// Stop searching at statement boundary; redactions don't
		// cross statements.
		if statementEndsHere(tokens[i]) {
			return nil
		}
	}
	return nil
}

// prevIdentInStmt walks backward from start looking for the previous
// IDENT token within the same statement. Used by the COPY FROM PROGRAM
// matcher to confirm the keyword that precedes PROGRAM is FROM/TO.
func prevIdentInStmt(tokens []Token, start int) *Token {
	for i := start; i >= 0; i-- {
		if statementEndsHere(tokens[i]) {
			return nil
		}
		if tokens[i].Kind == TokIdent {
			return &tokens[i]
		}
	}
	return nil
}

// nextString returns the next STRING / DOLLAR-STRING token at or after
// index start, or nil if none before a statement boundary.
func nextString(tokens []Token, start int) *Token {
	for i := start; i < len(tokens); i++ {
		t := &tokens[i]
		if t.Kind == TokString || t.Kind == TokDollarString {
			return t
		}
		if statementEndsHere(*t) {
			return nil
		}
	}
	return nil
}

// indexOf returns the index of t in tokens, or -1. Linear scan; used in
// rare control-flow paths so the cost is fine.
func indexOf(tokens []Token, t *Token) int {
	for i := range tokens {
		if &tokens[i] == t {
			return i
		}
	}
	return -1
}

// RedactSensitiveParams takes a parameterized-query parameter map and
// returns a copy where values whose keys suggest sensitivity (password,
// secret, token, etc.) are replaced with "[redacted]". Substring match
// on the lowercased key; conservative.
func RedactSensitiveParams(params map[string]any) map[string]any {
	if len(params) == 0 {
		return params
	}
	out := make(map[string]any, len(params))
	for k, v := range params {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "password") ||
			strings.Contains(lk, "passwd") ||
			strings.Contains(lk, "secret") ||
			strings.Contains(lk, "token") ||
			strings.Contains(lk, "api_key") ||
			strings.Contains(lk, "apikey") ||
			strings.Contains(lk, "private_key") {
			out[k] = "[redacted]"
			continue
		}
		out[k] = v
	}
	return out
}

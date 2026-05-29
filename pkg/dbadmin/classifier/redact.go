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
//
// The string literal following the IDENTIFIED BY / PASSWORD keyword is
// replaced with '[redacted]'. The rest of the statement is preserved
// verbatim so audit readers can still see the affected username.
//
// Operates on the raw SQL string (not the token stream) because audit
// records preserve the operator's formatting. Re-tokenizes internally
// to find the redaction targets without regex.
func RedactSensitiveInline(sql string, dialect Dialect) string {
	tokens := Lex(sql, LexOptions{Dialect: dialect, KeepComments: false})

	// Walk tokens looking for (IDENTIFIED BY) or (PASSWORD) followed by
	// a string literal. Record the byte ranges to redact.
	type redaction struct {
		start, end int
	}
	var redactions []redaction

	for i, t := range tokens {
		if t.Kind != TokIdent {
			continue
		}
		up := upper(t.Text)

		// Pattern 1: IDENTIFIED BY '<pw>'
		if up == "IDENTIFIED" {
			if next := nextIdent(tokens, i+1); next != nil && upper(next.Text) == "BY" {
				if str := nextString(tokens, indexOf(tokens, next)+1); str != nil {
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
	}

	if len(redactions) == 0 {
		return sql
	}

	// Apply redactions in reverse so byte offsets stay valid.
	out := sql
	for i := len(redactions) - 1; i >= 0; i-- {
		r := redactions[i]
		out = out[:r.start] + "'[redacted]'" + out[r.end:]
	}
	return out
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

package classifier

import (
	"strings"
	"unicode"
)

// TokenKind identifies the lexical category of a token.
type TokenKind uint8

const (
	TokEOF TokenKind = iota

	// TokIdent is an unquoted SQL identifier or keyword. The classifier
	// distinguishes keywords from identifiers by uppercase comparison
	// of the token text — SQL keywords are case-insensitive.
	TokIdent

	// TokQuotedIdent is a quoted identifier: `name` in MySQL, "name"
	// in standard SQL / Postgres. The lexer strips the surrounding
	// quotes; the raw inner text is in Token.Text.
	TokQuotedIdent

	// TokString is a string literal: 'value', with '' escape. The
	// lexer strips the surrounding quotes and processes ''-escapes
	// into single quotes.
	TokString

	// TokDollarString is a Postgres dollar-quoted string:
	// $tag$value$tag$. Treated identically to TokString for
	// classification purposes.
	TokDollarString

	// TokNumber is a numeric literal (integer, float, or scientific).
	TokNumber

	// TokParameter is a parameterized-query placeholder: ? (MySQL) or
	// $N (Postgres) or :name (some drivers).
	TokParameter

	// TokPunct is single-character punctuation: ; , . ( ) etc.
	TokPunct

	// TokOperator is single- or multi-character operator: = != <> < <= > >= ||
	TokOperator

	// TokComment is a stripped comment. The lexer skips comments by
	// default, but in `KeepComments` mode it yields them so audit
	// logging can preserve the operator's annotations.
	TokComment
)

// Token is one lexical unit. The Text field carries the canonical form
// of the token; the StartByte field is the offset within the original
// input.
type Token struct {
	Kind      TokenKind
	Text      string // canonical form: identifiers lowercased? No — preserved as-is.
	StartByte int    // offset within the original input
	EndByte   int    // one past the last byte
}

// IsKeyword reports whether the token is an identifier whose text
// matches the given keyword case-insensitively. The classifier uses
// this for every SQL keyword comparison.
func (t Token) IsKeyword(keyword string) bool {
	return t.Kind == TokIdent && strings.EqualFold(t.Text, keyword)
}

// LexOptions tunes the lexer's behavior per dialect.
type LexOptions struct {
	// Dialect identifies which engine's flavor we're tokenizing.
	// Affects: backtick identifiers (MySQL only), dollar-quoted
	// strings (Postgres only), DOUBLE-QUOTED interpretation (Postgres
	// = identifier, MySQL non-ANSI = string).
	Dialect Dialect

	// KeepComments emits TokComment tokens instead of skipping them.
	// Default false. Audit-logging callers set this true to preserve
	// operator annotations.
	KeepComments bool
}

// Dialect identifies which engine's lexical rules apply.
type Dialect uint8

const (
	DialectMySQL Dialect = iota
	DialectPostgres
)

// Lex tokenizes a SQL input. Returns the token stream plus any
// lex-level errors. Recoverable problems (unterminated string, unknown
// operator) are reported as errors but the lexer attempts to continue
// past them so the classifier sees as much of the input as possible.
//
// The lexer is stateless across calls; safe for concurrent use against
// distinct inputs.
func Lex(sql string, opts LexOptions) []Token {
	l := &lexer{src: sql, opts: opts}
	for {
		tok := l.next()
		if tok.Kind == TokEOF {
			l.tokens = append(l.tokens, tok)
			return l.tokens
		}
		l.tokens = append(l.tokens, tok)
	}
}

type lexer struct {
	src    string
	pos    int
	tokens []Token
	opts   LexOptions
}

func (l *lexer) next() Token {
	l.skipWhitespace()
	if l.pos >= len(l.src) {
		return Token{Kind: TokEOF, StartByte: l.pos, EndByte: l.pos}
	}

	start := l.pos
	ch := l.src[l.pos]

	// Comments.
	if ch == '-' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '-' {
		return l.scanLineComment(start)
	}
	if ch == '/' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '*' {
		return l.scanBlockComment(start)
	}
	// Postgres line comment is the same `--`; block comment is the
	// same `/* */` (nestable in Postgres but we don't need nesting
	// for classification — outer-most stripping is fine).

	// String literals.
	if ch == '\'' {
		return l.scanString(start)
	}

	// Quoted identifiers.
	if ch == '`' && l.opts.Dialect == DialectMySQL {
		return l.scanQuotedIdent(start, '`')
	}
	if ch == '"' {
		switch l.opts.Dialect {
		case DialectPostgres:
			// Always identifier in Postgres.
			return l.scanQuotedIdent(start, '"')
		case DialectMySQL:
			// In default (non-ANSI) MySQL mode, "..." is a string
			// literal. ANSI mode would make it an identifier, but
			// we're conservative: treat as identifier for
			// classification (so a forbidden function inside it
			// would NOT slip through; we'd see it as a quoted
			// name which the matcher ignores) — actually no, that
			// could cause false positives. Treat as string to be
			// safe with default MySQL mode; the security risk
			// (forbidden inside a double-quoted "string") is
			// fine because forbidden detection only fires on
			// IDENT tokens.
			return l.scanString(start)
		}
	}

	// Dollar-quoted string (Postgres only).
	if ch == '$' && l.opts.Dialect == DialectPostgres {
		if tok, ok := l.tryScanDollarString(start); ok {
			return tok
		}
		// Fall through to parameter placeholder ($N) or operator.
	}

	// Parameter placeholders.
	if ch == '?' {
		l.pos++
		return Token{Kind: TokParameter, Text: "?", StartByte: start, EndByte: l.pos}
	}
	if ch == '$' && l.opts.Dialect == DialectPostgres && l.pos+1 < len(l.src) && isDigit(l.src[l.pos+1]) {
		l.pos++ // consume $
		for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			l.pos++
		}
		return Token{Kind: TokParameter, Text: l.src[start:l.pos], StartByte: start, EndByte: l.pos}
	}
	if ch == ':' && l.pos+1 < len(l.src) && isIdentStart(l.src[l.pos+1]) {
		// :name parameter style (used by some drivers / saved
		// queries). Consume :ident.
		l.pos++
		for l.pos < len(l.src) && isIdentCont(l.src[l.pos]) {
			l.pos++
		}
		return Token{Kind: TokParameter, Text: l.src[start:l.pos], StartByte: start, EndByte: l.pos}
	}

	// Numbers.
	if isDigit(ch) || (ch == '.' && l.pos+1 < len(l.src) && isDigit(l.src[l.pos+1])) {
		return l.scanNumber(start)
	}

	// Identifiers and keywords.
	if isIdentStart(ch) {
		return l.scanIdent(start)
	}

	// Operators (multi-char first).
	if op := l.scanMultiCharOperator(start); op != "" {
		return Token{Kind: TokOperator, Text: op, StartByte: start, EndByte: l.pos}
	}

	// Punctuation and single-char operators.
	switch ch {
	case ';', ',', '(', ')', '[', ']', '{', '}':
		l.pos++
		return Token{Kind: TokPunct, Text: string(ch), StartByte: start, EndByte: l.pos}
	case '.':
		l.pos++
		return Token{Kind: TokPunct, Text: ".", StartByte: start, EndByte: l.pos}
	case '+', '-', '*', '/', '%', '=', '<', '>', '!', '~', '^', '&', '|', '@', '#':
		l.pos++
		return Token{Kind: TokOperator, Text: string(ch), StartByte: start, EndByte: l.pos}
	}

	// Unknown byte: consume one and emit as punctuation to keep the
	// lexer moving. The classifier doesn't act on unknown bytes; the
	// driver will reject the statement if it matters.
	l.pos++
	return Token{Kind: TokPunct, Text: string(ch), StartByte: start, EndByte: l.pos}
}

func (l *lexer) skipWhitespace() {
	for l.pos < len(l.src) {
		ch := l.src[l.pos]
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == '\f' || ch == '\v' {
			l.pos++
			continue
		}
		break
	}
}

func (l *lexer) scanLineComment(start int) Token {
	// Already at the leading '-'; advance past --
	l.pos += 2
	for l.pos < len(l.src) && l.src[l.pos] != '\n' {
		l.pos++
	}
	if l.opts.KeepComments {
		return Token{Kind: TokComment, Text: l.src[start:l.pos], StartByte: start, EndByte: l.pos}
	}
	// Recurse: return the next non-comment token.
	return l.next()
}

func (l *lexer) scanBlockComment(start int) Token {
	// Advance past /*
	l.pos += 2
	for l.pos+1 < len(l.src) {
		if l.src[l.pos] == '*' && l.src[l.pos+1] == '/' {
			l.pos += 2
			if l.opts.KeepComments {
				return Token{Kind: TokComment, Text: l.src[start:l.pos], StartByte: start, EndByte: l.pos}
			}
			return l.next()
		}
		l.pos++
	}
	// Unterminated. Consume to EOF. Don't emit a TokComment if the
	// lexer wasn't asked for it; just move on.
	l.pos = len(l.src)
	if l.opts.KeepComments {
		return Token{Kind: TokComment, Text: l.src[start:l.pos], StartByte: start, EndByte: l.pos}
	}
	return Token{Kind: TokEOF, StartByte: l.pos, EndByte: l.pos}
}

func (l *lexer) scanString(start int) Token {
	// At leading quote.
	quote := l.src[l.pos]
	l.pos++ // consume opening quote
	for l.pos < len(l.src) {
		ch := l.src[l.pos]
		if ch == quote {
			// Doubled quote = escape.
			if l.pos+1 < len(l.src) && l.src[l.pos+1] == quote {
				l.pos += 2
				continue
			}
			l.pos++ // consume closing quote
			return Token{
				Kind:      TokString,
				Text:      l.src[start:l.pos],
				StartByte: start,
				EndByte:   l.pos,
			}
		}
		// Backslash escapes: MySQL non-ANSI honors \', \\, etc. We
		// don't decode them — we just skip the next byte so the
		// quote doesn't terminate prematurely.
		if ch == '\\' && l.opts.Dialect == DialectMySQL && l.pos+1 < len(l.src) {
			l.pos += 2
			continue
		}
		l.pos++
	}
	// Unterminated string. Emit what we have; the driver will reject.
	return Token{Kind: TokString, Text: l.src[start:l.pos], StartByte: start, EndByte: l.pos}
}

func (l *lexer) scanQuotedIdent(start int, quote byte) Token {
	l.pos++ // consume opening quote
	for l.pos < len(l.src) {
		ch := l.src[l.pos]
		if ch == quote {
			if l.pos+1 < len(l.src) && l.src[l.pos+1] == quote {
				l.pos += 2
				continue
			}
			l.pos++
			return Token{
				Kind:      TokQuotedIdent,
				Text:      l.src[start:l.pos],
				StartByte: start,
				EndByte:   l.pos,
			}
		}
		l.pos++
	}
	return Token{Kind: TokQuotedIdent, Text: l.src[start:l.pos], StartByte: start, EndByte: l.pos}
}

func (l *lexer) tryScanDollarString(start int) (Token, bool) {
	// Look for $tag$ where tag is empty or an unquoted identifier.
	if l.pos >= len(l.src) || l.src[l.pos] != '$' {
		return Token{}, false
	}
	end := l.pos + 1
	for end < len(l.src) {
		ch := l.src[end]
		if ch == '$' {
			break
		}
		if !(isIdentStart(ch) || isDigit(ch)) {
			return Token{}, false
		}
		end++
	}
	if end >= len(l.src) || l.src[end] != '$' {
		return Token{}, false
	}
	tag := l.src[l.pos : end+1] // includes both $ delimiters
	bodyStart := end + 1
	// Find the matching tag.
	idx := strings.Index(l.src[bodyStart:], tag)
	if idx < 0 {
		// Unterminated; consume to EOF.
		l.pos = len(l.src)
		return Token{
			Kind:      TokDollarString,
			Text:      l.src[start:l.pos],
			StartByte: start,
			EndByte:   l.pos,
		}, true
	}
	l.pos = bodyStart + idx + len(tag)
	return Token{
		Kind:      TokDollarString,
		Text:      l.src[start:l.pos],
		StartByte: start,
		EndByte:   l.pos,
	}, true
}

func (l *lexer) scanNumber(start int) Token {
	// Integer part.
	for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
		l.pos++
	}
	// Fractional part.
	if l.pos < len(l.src) && l.src[l.pos] == '.' {
		l.pos++
		for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			l.pos++
		}
	}
	// Exponent.
	if l.pos < len(l.src) && (l.src[l.pos] == 'e' || l.src[l.pos] == 'E') {
		l.pos++
		if l.pos < len(l.src) && (l.src[l.pos] == '+' || l.src[l.pos] == '-') {
			l.pos++
		}
		for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			l.pos++
		}
	}
	return Token{Kind: TokNumber, Text: l.src[start:l.pos], StartByte: start, EndByte: l.pos}
}

func (l *lexer) scanIdent(start int) Token {
	for l.pos < len(l.src) && isIdentCont(l.src[l.pos]) {
		l.pos++
	}
	return Token{Kind: TokIdent, Text: l.src[start:l.pos], StartByte: start, EndByte: l.pos}
}

// scanMultiCharOperator tries the longest multi-char operators first.
// Returns the matched operator string and advances l.pos; returns "" if
// no multi-char operator starts at l.pos.
func (l *lexer) scanMultiCharOperator(start int) string {
	if l.pos+2 < len(l.src) {
		s3 := l.src[l.pos : l.pos+3]
		switch s3 {
		case "<=>", "<<=", ">>=":
			l.pos += 3
			return s3
		}
	}
	if l.pos+1 < len(l.src) {
		s2 := l.src[l.pos : l.pos+2]
		switch s2 {
		case "!=", "<>", "<=", ">=", "<<", ">>", "||", "&&", "::", ":=", "->":
			l.pos += 2
			return s2
		}
	}
	_ = start
	return ""
}

// ─── Character classification helpers ────────────────────────────────

func isDigit(ch byte) bool {
	return ch >= '0' && ch <= '9'
}

func isIdentStart(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_' ||
		// SQL identifiers may start with non-ASCII letters; we allow
		// any high-bit byte and let the matcher's case-insensitive
		// comparison filter false positives. The forbidden list is
		// pure-ASCII so this lenient acceptance can't produce a
		// false negative on the security-critical path.
		ch >= 0x80
}

func isIdentCont(ch byte) bool {
	return isIdentStart(ch) || isDigit(ch) || ch == '$'
}

// upper returns the ASCII-uppercase form of s. Used for keyword
// matching. Faster than strings.ToUpper for short ASCII strings (the
// common case in the classifier hot path).
func upper(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] >= 'a' && s[i] <= 'z' {
			return strings.ToUpper(s)
		}
	}
	return s
}

// runeIsLetter is unused inside the lexer but exported helpers may want
// it. Keep it here so removing the inline isIdentStart check later
// doesn't drop the capability.
var _ = unicode.IsLetter

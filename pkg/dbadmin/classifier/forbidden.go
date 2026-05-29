package classifier

// This file defines the hard-forbidden list and the multi-token matcher
// that enforces it. Per SECURITY.md §6.3.2: these patterns are blocked
// at the parser level with no override, no per-connection flag, no UI
// escape hatch. Operators who need them connect via mysql/psql over
// SSH — which means they already have shell-level trust on the host.

// ForbiddenPattern is a sequence of token predicates. Every predicate
// must match an IDENT token in order (ignoring intervening whitespace,
// comments, and arbitrary other tokens, with the exception of statement
// separators ';' which reset matching state — patterns cannot span
// statements).
//
// The pattern matcher walks the token stream linearly; matching is
// greedy on each predicate. A match emits a ForbiddenMatch identifying
// the pattern and the offset of its first token.
type ForbiddenPattern struct {
	// Name is the human-readable identifier shown in error messages
	// and audit events.
	Name string

	// Reason is the operator-facing explanation: what the matched
	// construct can do that makes it forbidden.
	Reason string

	// Engines names the dialects this pattern applies to. Empty
	// means "both."
	Engines []Dialect

	// Sequence is the ordered list of token predicates that, when
	// matched in order, fire the pattern. See predicate constructors
	// below.
	Sequence []predicate

	// MaxGap caps how many tokens may sit between consecutive
	// predicates. Zero means unlimited (within the same statement).
	// Used for patterns that should match only adjacent tokens
	// (e.g., "INTO OUTFILE" — these must be back-to-back).
	MaxGap int
}

// predicate matches a single token. Constructed via the helper
// functions below.
type predicate func(t Token) bool

// kw matches an IDENT token equal to the given keyword (case-
// insensitive ASCII comparison).
func kw(keyword string) predicate {
	upperKW := upper(keyword)
	return func(t Token) bool {
		return t.Kind == TokIdent && upper(t.Text) == upperKW
	}
}

// kwAny matches an IDENT token equal to any of the given keywords.
func kwAny(keywords ...string) predicate {
	uppers := make([]string, len(keywords))
	for i, k := range keywords {
		uppers[i] = upper(k)
	}
	return func(t Token) bool {
		if t.Kind != TokIdent {
			return false
		}
		u := upper(t.Text)
		for _, kw := range uppers {
			if u == kw {
				return true
			}
		}
		return false
	}
}

// punct matches a punctuation token. Useful for "FUNCTION(" patterns
// that need the open-paren to confirm a function call shape.
func punct(s string) predicate {
	return func(t Token) bool {
		return t.Kind == TokPunct && t.Text == s
	}
}

// forbiddenPatterns is the full list. See SECURITY.md §6.3.2 for the
// canonical specification; this slice IS the implementation of that
// section.
var forbiddenPatterns = []ForbiddenPattern{
	// ─── MySQL/MariaDB ──────────────────────────────────────────────

	{
		Name:    "LOAD_FILE",
		Reason:  "MySQL's LOAD_FILE() can read arbitrary files readable by the server process; used in historic SQLi-to-RCE chains.",
		Engines: []Dialect{DialectMySQL},
		Sequence: []predicate{
			kw("LOAD_FILE"),
			punct("("),
		},
		MaxGap: 1, // function name and ( may have a single whitespace/comment token between
	},
	{
		Name:    "INTO OUTFILE",
		Reason:  "MySQL's INTO OUTFILE writes query results to a server-side file path; primitive for arbitrary-file-write attacks.",
		Engines: []Dialect{DialectMySQL},
		Sequence: []predicate{
			kw("INTO"),
			kwAny("OUTFILE", "DUMPFILE"),
		},
		MaxGap: 1,
	},
	{
		Name:    "LOAD DATA INFILE",
		Reason:  "MySQL's LOAD DATA INFILE reads server-side files into a table; the panel-managed CSV import uses a different code path that streams via stdin.",
		Engines: []Dialect{DialectMySQL},
		Sequence: []predicate{
			kw("LOAD"),
			kw("DATA"),
			kw("INFILE"),
		},
		MaxGap: 2,
	},
	{
		Name:    "sys_exec / sys_eval",
		Reason:  "These UDF function names are the classic mysql-udf RCE primitive. Aura DB refuses any reference to them.",
		Engines: []Dialect{DialectMySQL},
		Sequence: []predicate{
			kwAny("SYS_EXEC", "SYS_EVAL", "DO_SYSTEM"),
			punct("("),
		},
		MaxGap: 1,
	},
	{
		Name:    "SELECT ... INTO file",
		Reason:  "Variant of INTO OUTFILE that hides behind a SELECT INTO at a different syntactic position; same risk.",
		Engines: []Dialect{DialectMySQL},
		Sequence: []predicate{
			kw("INTO"),
			punct("@"),
			// Some MySQL CVE corpora use INTO @var := LOAD_FILE.
			// Catching the @ after INTO is sufficient to flag.
		},
		MaxGap: 2,
	},

	// ─── PostgreSQL ─────────────────────────────────────────────────

	{
		Name:    "COPY ... FROM PROGRAM / TO PROGRAM",
		Reason:  "Postgres COPY ... [FROM|TO] PROGRAM executes arbitrary shell commands as the database server user; classic Postgres RCE primitive.",
		Engines: []Dialect{DialectPostgres},
		Sequence: []predicate{
			kw("COPY"),
			kwAny("FROM", "TO"),
			kw("PROGRAM"),
		},
		// MaxGap=0 here would miss "COPY tbl (cols) FROM PROGRAM"
		// because column list intervenes. We rely on the ordered
		// match without intermediate FROM/TO/PROGRAM being legitimate
		// in the same statement; CVE corpus confirms no false
		// positives.
	},
	{
		Name:    "COPY ... FROM file path",
		Reason:  "Postgres COPY ... FROM '<path>' / TO '<path>' reads/writes server-side files. CSV import goes through the panel's stream-via-stdin path instead.",
		Engines: []Dialect{DialectPostgres},
		Sequence: []predicate{
			kw("COPY"),
			// Matcher below also fires on PROGRAM (above pattern
			// catches that), but having both patterns is
			// belt-and-braces. We additionally require the next
			// token after FROM/TO to be a STRING literal.
			kwAny("FROM", "TO"),
		},
		// We need a custom finalizer here: only fire when the token
		// after FROM/TO is a string literal (path). Handled in the
		// matcher's pattern-specific hook below.
	},
	{
		Name:    "pg_read_file family",
		Reason:  "Server-side filesystem-read functions; the panel does not expose filesystem access through SQL.",
		Engines: []Dialect{DialectPostgres},
		Sequence: []predicate{
			kwAny(
				"PG_READ_FILE",
				"PG_READ_BINARY_FILE",
				"PG_LS_DIR",
				"PG_STAT_FILE",
			),
			punct("("),
		},
		MaxGap: 1,
	},
	{
		Name:    "lo_import / lo_export with path",
		Reason:  "Large-object import/export to server-side file paths.",
		Engines: []Dialect{DialectPostgres},
		Sequence: []predicate{
			kwAny("LO_IMPORT", "LO_EXPORT"),
			punct("("),
		},
		MaxGap: 1,
	},
	{
		Name:    "CREATE EXTENSION ... FROM unsafe path",
		Reason:  "CREATE EXTENSION with a custom FROM path can load arbitrary extension code from a server-side file.",
		Engines: []Dialect{DialectPostgres},
		Sequence: []predicate{
			kw("CREATE"),
			kw("EXTENSION"),
			// Anywhere later in the statement: FROM <string>
			kw("FROM"),
		},
	},
	{
		Name:    "dblink_connect_u (unauth variant)",
		Reason:  "The _u variant of dblink_connect skips authentication for the new dblink connection.",
		Engines: []Dialect{DialectPostgres},
		Sequence: []predicate{
			kw("DBLINK_CONNECT_U"),
			punct("("),
		},
		MaxGap: 1,
	},
	{
		Name:    "Untrusted PL/* function language",
		Reason:  "PL/Python-untrusted, PL/Perl-untrusted, PL/sh, and similar untrusted procedural languages permit arbitrary code execution on the server.",
		Engines: []Dialect{DialectPostgres},
		Sequence: []predicate{
			kw("LANGUAGE"),
			kwAny(
				"PLPYTHONU",
				"PLPYTHON3U",
				"PLPERLU",
				"PLSH",
				"PLV8U",
				"PLR",
			),
		},
		MaxGap: 1,
	},

	// ─── Cross-engine (rare; here for completeness) ─────────────────

	// (intentionally empty — no current cross-engine forbidden
	// patterns. Reserved for future additions.)
}

// statementEndsHere reports whether the token is the start of a new
// statement boundary. Pattern matching state resets at statement
// boundaries.
func statementEndsHere(t Token) bool {
	return t.Kind == TokPunct && t.Text == ";"
}

// matchForbidden walks the token stream and returns every forbidden
// pattern hit. The matcher is engine-aware: it ignores patterns whose
// Engines field doesn't include the current dialect.
//
// The matcher is conservative: a partial match that runs into a
// statement separator (`;`) or exceeds MaxGap is dropped and matching
// restarts. False positives are possible (e.g., a table named "OUTFILE"
// after INTO would trigger the INTO OUTFILE pattern); they're listed in
// PR #2.5's open issues and resolved by the AST upgrade.
func matchForbidden(tokens []Token, dialect Dialect) []ForbiddenMatch {
	var hits []ForbiddenMatch

	// Track current statement index for the StatementIndex field.
	// IMPORTANT: stmtIdx must align with splitStatements' indexing,
	// which SKIPS empty statements (consecutive `;;`, leading `;`,
	// trailing `;`). We mirror that here: only advance stmtIdx when
	// a non-empty statement just ended.
	stmtIdx := 0
	statementHasContent := false

	// Active partial matches per pattern.
	type active struct {
		patternIdx int
		nextStep   int // index into pattern.Sequence
		matchStart int // byte offset of the first matched token
		lastByte   int // byte offset of the most recent matched token
		gapBudget  int // tokens remaining before we abandon the match
	}
	var actives []active

	startMatch := func(patternIdx int, t Token) {
		p := &forbiddenPatterns[patternIdx]
		gap := p.MaxGap
		if gap == 0 {
			gap = -1 // unlimited within statement
		}
		actives = append(actives, active{
			patternIdx: patternIdx,
			nextStep:   1,
			matchStart: t.StartByte,
			lastByte:   t.EndByte,
			gapBudget:  gap,
		})
	}

	for _, tok := range tokens {
		// Statement boundary clears every partial match. New
		// statement begins ONLY if the previous statement had
		// content — empty statements don't advance the index.
		if statementEndsHere(tok) {
			actives = actives[:0]
			if statementHasContent {
				stmtIdx++
				statementHasContent = false
			}
			continue
		}
		// EOF is not a statement separator; if we see it the loop
		// exits naturally on the next iteration.
		if tok.Kind == TokEOF {
			continue
		}
		// Anything else marks the current statement as having content.
		statementHasContent = true

		// Try to start each pattern's first step on this token.
		for pi := range forbiddenPatterns {
			p := &forbiddenPatterns[pi]
			if !dialectMatch(p.Engines, dialect) {
				continue
			}
			if len(p.Sequence) == 0 {
				continue
			}
			if p.Sequence[0](tok) {
				if len(p.Sequence) == 1 {
					// Single-token pattern matches outright.
					hits = append(hits, ForbiddenMatch{
						Pattern:        p.Name,
						Reason:         p.Reason,
						StatementIndex: stmtIdx,
						TokenOffset:    tok.StartByte,
					})
				} else {
					startMatch(pi, tok)
				}
			}
		}

		// Advance any active partials.
		next := actives[:0]
		for _, a := range actives {
			p := &forbiddenPatterns[a.patternIdx]
			if a.nextStep >= len(p.Sequence) {
				continue // already completed; defensive.
			}
			step := p.Sequence[a.nextStep]
			if step(tok) {
				a.nextStep++
				a.lastByte = tok.EndByte
				if a.nextStep == len(p.Sequence) {
					// Pattern completed. Some patterns
					// require a custom finalizer; check it.
					if finalize := finalizers[p.Name]; finalize != nil {
						if !finalize(tokens, tok) {
							continue
						}
					}
					hits = append(hits, ForbiddenMatch{
						Pattern:        p.Name,
						Reason:         p.Reason,
						StatementIndex: stmtIdx,
						TokenOffset:    a.matchStart,
					})
					continue
				}
				if a.gapBudget > 0 {
					a.gapBudget = p.MaxGap
				}
				next = append(next, a)
				continue
			}
			// Step didn't match this token. Decrement gap and keep
			// the active alive if budget remains.
			if a.gapBudget == 0 {
				continue
			}
			if a.gapBudget > 0 {
				a.gapBudget--
			}
			next = append(next, a)
		}
		actives = next
	}

	return hits
}

func dialectMatch(engines []Dialect, current Dialect) bool {
	if len(engines) == 0 {
		return true
	}
	for _, e := range engines {
		if e == current {
			return true
		}
	}
	return false
}

// finalizers is a map of pattern-name → optional validation that runs
// after the pattern's Sequence completes. Only registered patterns have
// a finalizer; others are accepted as-matched.
//
// finalizer receives the full token stream and the LAST matched token
// of the pattern. It returns true to accept the match, false to reject.
var finalizers = map[string]func(tokens []Token, lastMatched Token) bool{
	"COPY ... FROM file path": func(tokens []Token, last Token) bool {
		// We matched `COPY ... FROM` or `COPY ... TO`. Check that
		// the next non-whitespace token is a STRING. If it's an
		// IDENT (e.g., a table name in INSERT-FROM-COPY), it's a
		// different statement shape and we shouldn't flag.
		for i, t := range tokens {
			if t.StartByte != last.StartByte {
				continue
			}
			// Walk forward from i+1.
			for j := i + 1; j < len(tokens); j++ {
				nt := tokens[j]
				if nt.Kind == TokComment {
					continue
				}
				return nt.Kind == TokString
			}
		}
		return false
	},
}

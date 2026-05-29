package classifier

import (
	"strings"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// mysqlTokenizer implements Parser for MariaDB/MySQL.
//
// The classifier:
//  1. Tokenizes the input with DialectMySQL options.
//  2. Splits the token stream at `;` boundaries into per-statement sub-streams.
//  3. Classifies each sub-stream by its leading keyword + contextual checks
//     (WHERE presence for UPDATE/DELETE, SCOPE keyword for SET, etc.).
//  4. Runs the forbidden matcher across the full token stream; statements
//     that contain a forbidden match are escalated to ClassForbidden
//     regardless of their leading keyword.
//  5. Aggregates: ParsedQuery.Class is the strictest among statements.
type mysqlTokenizer struct{}

func (m *mysqlTokenizer) Parse(sql string) (ParsedQuery, error) {
	tokens := Lex(sql, LexOptions{Dialect: DialectMySQL})
	forbidden := matchForbidden(tokens, DialectMySQL)

	stmts := splitStatements(tokens, sql)
	parsed := make([]ParsedStatement, 0, len(stmts))
	for _, s := range stmts {
		ps := classifyStatement(s, DialectMySQL)
		ps.ParseSource = ParseSourceFallback
		parsed = append(parsed, ps)
	}

	// Apply forbidden matches: escalate to ClassForbidden, drop Action.
	for _, f := range forbidden {
		if f.StatementIndex >= 0 && f.StatementIndex < len(parsed) {
			parsed[f.StatementIndex].Class = ClassForbidden
			parsed[f.StatementIndex].Action = ""
		}
	}

	return ParsedQuery{
		Class:       strictestClass(parsed),
		Statements:  parsed,
		Forbidden:   forbidden,
		ParseSource: ParseSourceFallback,
	}, nil
}

// statementSlice carries the tokens that belong to one statement plus the
// byte offset of the statement's start within the original input.
type statementSlice struct {
	tokens []Token
	offset int
	text   string
}

// splitStatements walks tokens, splitting at `;`. Empty trailing fragments
// (after a final `;`) are discarded.
func splitStatements(tokens []Token, src string) []statementSlice {
	var out []statementSlice
	var current []Token
	start := 0
	for _, t := range tokens {
		if t.Kind == TokEOF {
			break
		}
		if statementEndsHere(t) {
			if len(current) > 0 {
				end := t.StartByte
				out = append(out, statementSlice{
					tokens: current,
					offset: start,
					text:   strings.TrimSpace(src[start:end]),
				})
			}
			current = nil
			start = t.EndByte
			continue
		}
		if len(current) == 0 {
			start = t.StartByte
		}
		current = append(current, t)
	}
	if len(current) > 0 {
		out = append(out, statementSlice{
			tokens: current,
			offset: start,
			text:   strings.TrimSpace(src[start:]),
		})
	}
	return out
}

// classifyStatement examines the token slice for one statement and produces
// a ParsedStatement.
func classifyStatement(s statementSlice, dialect Dialect) ParsedStatement {
	if len(s.tokens) == 0 {
		return ParsedStatement{Class: ClassRead, Kind: KindUnknown, RawText: s.text, Offset: s.offset}
	}

	first := firstSignificantIdent(s.tokens)
	if first == nil {
		return ParsedStatement{Class: ClassRead, Kind: KindUnknown, RawText: s.text, Offset: s.offset}
	}

	kind, class, action := classifyByKeyword(upper(first.Text), s.tokens, dialect)

	hasWhere := false
	if kind == KindUpdate || kind == KindDelete {
		hasWhere = containsKeyword(s.tokens, "WHERE")
		if !hasWhere {
			class = ClassWriteRowMass
		}
	}

	return ParsedStatement{
		Class:    class,
		Kind:     kind,
		Action:   action,
		HasWhere: hasWhere,
		RawText:  s.text,
		Offset:   s.offset,
	}
}

// firstSignificantIdent returns the first IDENT/QuotedIdent token (skipping
// comments, strings, parameters, numbers, operators). Returns nil if the
// statement has no leading identifier.
func firstSignificantIdent(tokens []Token) *Token {
	for i := range tokens {
		t := &tokens[i]
		if t.Kind == TokIdent || t.Kind == TokQuotedIdent {
			return t
		}
	}
	return nil
}

// classifyByKeyword maps the leading keyword to (kind, class, action).
// Conservative: ambiguous statements get the stricter class.
func classifyByKeyword(kw string, tokens []Token, dialect Dialect) (StatementKind, QueryClass, dbadmin.Action) {
	switch kw {
	case "SELECT":
		return KindSelect, ClassRead, dbadmin.ActionQueryRead
	case "SHOW":
		return KindShow, ClassRead, dbadmin.ActionQueryRead
	case "DESC", "DESCRIBE":
		return KindDescribe, ClassRead, dbadmin.ActionQueryRead
	case "EXPLAIN":
		return KindExplain, ClassRead, dbadmin.ActionQueryRead
	case "USE":
		return KindUse, ClassRead, dbadmin.ActionQueryRead

	case "WITH":
		// CTE. Class follows the wrapped statement.
		cls := withClass(tokens)
		act := dbadmin.ActionQueryRead
		if cls == ClassWriteRow || cls == ClassWriteRowMass {
			act = dbadmin.ActionQueryWrite
		} else if cls == ClassDDL {
			act = dbadmin.ActionQueryDDL
		}
		return KindWith, cls, act

	case "INSERT":
		return KindInsert, ClassWriteRow, dbadmin.ActionQueryWrite
	case "REPLACE":
		return KindReplace, ClassWriteRow, dbadmin.ActionQueryWrite
	case "UPDATE":
		return KindUpdate, ClassWriteRow, dbadmin.ActionQueryWrite
	case "DELETE":
		return KindDelete, ClassWriteRow, dbadmin.ActionQueryWrite
	case "MERGE":
		return KindMerge, ClassWriteRow, dbadmin.ActionQueryWrite
	case "TRUNCATE":
		return KindTruncate, ClassWriteRowMass, dbadmin.ActionQueryWrite

	case "CREATE":
		return KindCreate, ClassDDL, dbadmin.ActionQueryDDL
	case "ALTER":
		return KindAlter, ClassDDL, dbadmin.ActionQueryDDL
	case "DROP":
		return KindDrop, ClassDDL, dbadmin.ActionQueryDDL
	case "RENAME":
		return KindRename, ClassDDL, dbadmin.ActionQueryDDL
	case "REINDEX":
		return KindReindex, ClassDDL, dbadmin.ActionQueryDDL
	case "VACUUM":
		return KindVacuum, ClassDDL, dbadmin.ActionQueryDDL
	case "ANALYZE":
		return KindAnalyze, ClassDDL, dbadmin.ActionQueryDDL

	case "GRANT":
		return KindGrant, ClassDangerous, dbadmin.ActionQueryDangerous
	case "REVOKE":
		return KindRevoke, ClassDangerous, dbadmin.ActionQueryDangerous
	case "SET":
		if setIsGlobalScope(tokens) {
			return KindSet, ClassDangerous, dbadmin.ActionQueryDangerous
		}
		return KindSet, ClassRead, dbadmin.ActionQueryRead
	case "KILL":
		return KindKill, ClassDangerous, dbadmin.ActionQueryDangerous
	case "FLUSH":
		return KindFlush, ClassDangerous, dbadmin.ActionQueryDangerous
	case "RESET":
		return KindReset, ClassDangerous, dbadmin.ActionQueryDangerous

	case "START":
		return KindStart, ClassRead, dbadmin.ActionQueryRead
	case "COMMIT":
		return KindCommit, ClassRead, dbadmin.ActionQueryRead
	case "ROLLBACK":
		return KindRollback, ClassRead, dbadmin.ActionQueryRead
	case "SAVEPOINT":
		return KindSavepoint, ClassRead, dbadmin.ActionQueryRead
	case "BEGIN":
		return KindBegin, ClassRead, dbadmin.ActionQueryRead

	case "CALL":
		// Stored procedure invocation. Could do anything; default to
		// write-row (most stored procs at least UPDATE state).
		return KindCall, ClassWriteRow, dbadmin.ActionQueryWrite

	case "LOCK":
		return KindLockTables, ClassWriteRow, dbadmin.ActionQueryWrite
	case "UNLOCK":
		return KindUnlockTables, ClassRead, dbadmin.ActionQueryRead

	case "HANDLER":
		return KindHandler, ClassDDL, dbadmin.ActionQueryDDL

	case "CHANGE", "SLAVE", "REPLICA":
		return KindReplica, ClassDangerous, dbadmin.ActionQueryDangerous
	}

	_ = dialect
	// Unknown leading keyword — fail closed at DDL class (requires
	// step-up, refuses for non-DBA). Safer than ClassRead.
	return KindUnknown, ClassDDL, dbadmin.ActionQueryDDL
}

// containsKeyword reports whether any IDENT token matches keyword
// case-insensitively.
func containsKeyword(tokens []Token, keyword string) bool {
	up := upper(keyword)
	for _, t := range tokens {
		if t.Kind == TokIdent && upper(t.Text) == up {
			return true
		}
	}
	return false
}

// withClass scans a CTE body for INSERT/UPDATE/DELETE/MERGE/DDL to assign
// the wrapped statement's class. Default is ClassRead.
func withClass(tokens []Token) QueryClass {
	max := ClassRead
	for _, t := range tokens {
		if t.Kind != TokIdent {
			continue
		}
		switch upper(t.Text) {
		case "INSERT", "UPDATE", "DELETE", "REPLACE", "MERGE":
			if max < ClassWriteRow {
				max = ClassWriteRow
			}
		case "CREATE", "ALTER", "DROP", "RENAME":
			if max < ClassDDL {
				max = ClassDDL
			}
		case "TRUNCATE":
			if max < ClassWriteRowMass {
				max = ClassWriteRowMass
			}
		}
	}
	return max
}

// setIsGlobalScope reports whether a SET statement targets global / persist /
// persist_only scope (which is dangerous). Returns false for session-local
// SET (which is read-class).
func setIsGlobalScope(tokens []Token) bool {
	for _, t := range tokens {
		if t.Kind != TokIdent {
			continue
		}
		switch upper(t.Text) {
		case "GLOBAL", "PERSIST", "PERSIST_ONLY":
			return true
		case "SESSION", "LOCAL":
			return false
		}
	}
	return false
}

// strictestClass returns the strictest class across a slice of statements.
// ClassForbidden > ClassDangerous > ClassDDL > ClassWriteRowMass >
// ClassWriteRow > ClassRead.
func strictestClass(stmts []ParsedStatement) QueryClass {
	max := ClassRead
	for _, s := range stmts {
		if s.Class > max {
			max = s.Class
		}
	}
	return max
}

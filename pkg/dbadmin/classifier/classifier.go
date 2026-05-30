package classifier

import (
	"fmt"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// QueryClass is the security-policy class of a statement. Ordered from
// least to most restricted; multi-statement queries take the strictest
// class among their statements.
type QueryClass uint8

const (
	// ClassRead — SELECT, SHOW, EXPLAIN, DESCRIBE, WITH...SELECT, etc.
	// Allowed for RoleAnalyst and above. Never step-up.
	ClassRead QueryClass = iota

	// ClassWriteRow — INSERT, UPDATE with WHERE, DELETE with WHERE.
	// Allowed for RoleWriter and above.
	ClassWriteRow

	// ClassWriteRowMass — UPDATE/DELETE without WHERE, TRUNCATE.
	// Allowed for RoleWriter+ but always with step-up + typed
	// confirmation.
	ClassWriteRowMass

	// ClassDDL — CREATE, ALTER, DROP, RENAME, REINDEX, VACUUM, etc.
	// RoleDBA+, step-up for DROP/TRUNCATE/ALTER.
	ClassDDL

	// ClassDangerous — GRANT, REVOKE, SET GLOBAL/PERSIST, KILL,
	// replication commands, etc. RoleOwner only, always step-up.
	ClassDangerous

	// ClassForbidden — LOAD_FILE, INTO OUTFILE, COPY FROM PROGRAM,
	// pg_read_file, plpythonu, etc. Refused at this layer; no role
	// can authorize it.
	ClassForbidden
)

// String returns the canonical lowercased name for use in errors and
// audit events.
func (c QueryClass) String() string {
	switch c {
	case ClassRead:
		return "read"
	case ClassWriteRow:
		return "write-row"
	case ClassWriteRowMass:
		return "write-row-mass"
	case ClassDDL:
		return "ddl"
	case ClassDangerous:
		return "dangerous"
	case ClassForbidden:
		return "forbidden"
	default:
		return "unknown"
	}
}

// Action returns the dbadmin.Action that this class maps to for
// authorization. Empty when the class has no corresponding action
// (e.g., ClassForbidden has no authorizing role).
func (c QueryClass) Action() dbadmin.Action {
	switch c {
	case ClassRead:
		return dbadmin.ActionQueryRead
	case ClassWriteRow, ClassWriteRowMass:
		return dbadmin.ActionQueryWrite
	case ClassDDL:
		return dbadmin.ActionQueryDDL
	case ClassDangerous:
		return dbadmin.ActionQueryDangerous
	default:
		return ""
	}
}

// StatementKind identifies the concrete statement type. Used by the
// classifier to drive class assignment + by audit logging for
// human-readable summaries.
type StatementKind uint8

const (
	KindUnknown StatementKind = iota

	// Read-class kinds.
	KindSelect
	KindShow
	KindExplain
	KindDescribe
	KindWith // WITH ... can be read OR write; classifier inspects the body.

	// Write-row-class kinds.
	KindInsert
	KindUpdate
	KindDelete
	KindReplace // MySQL-specific
	KindMerge   // Postgres + SQL-standard

	// Write-row-mass (overlap with KindUpdate / KindDelete when no
	// WHERE present; KindTruncate is always mass).
	KindTruncate

	// DDL kinds.
	KindCreate
	KindAlter
	KindDrop
	KindRename
	KindReindex // Postgres
	KindVacuum  // Postgres
	KindAnalyze // Postgres + MySQL ANALYZE TABLE

	// Dangerous kinds.
	KindGrant
	KindRevoke
	KindSet // SET GLOBAL/PERSIST/SESSION — class depends on scope.
	KindKill
	KindFlush      // FLUSH PRIVILEGES, etc.
	KindStart      // START TRANSACTION etc.
	KindCommit     // COMMIT — not classified dangerous; here for kind ID
	KindRollback   // ROLLBACK
	KindSavepoint  // SAVEPOINT
	KindReplica    // CHANGE MASTER, START SLAVE, STOP SLAVE
	KindCall       // CALL stored_proc(...) — classified by inspection
	KindUse        // USE database; — read-class but recorded distinctly
	KindBegin      // BEGIN — alias for START TRANSACTION
	KindReset      // RESET PERSIST etc.
	KindLockTables // LOCK TABLES — class depends on whether WRITE
	KindUnlockTables
	KindHandler // MySQL HANDLER — exotic, classified ddl-conservatively
)

// String returns the canonical SQL keyword name (uppercase).
func (k StatementKind) String() string {
	switch k {
	case KindSelect:
		return "SELECT"
	case KindShow:
		return "SHOW"
	case KindExplain:
		return "EXPLAIN"
	case KindDescribe:
		return "DESCRIBE"
	case KindWith:
		return "WITH"
	case KindInsert:
		return "INSERT"
	case KindUpdate:
		return "UPDATE"
	case KindDelete:
		return "DELETE"
	case KindReplace:
		return "REPLACE"
	case KindMerge:
		return "MERGE"
	case KindTruncate:
		return "TRUNCATE"
	case KindCreate:
		return "CREATE"
	case KindAlter:
		return "ALTER"
	case KindDrop:
		return "DROP"
	case KindRename:
		return "RENAME"
	case KindReindex:
		return "REINDEX"
	case KindVacuum:
		return "VACUUM"
	case KindAnalyze:
		return "ANALYZE"
	case KindGrant:
		return "GRANT"
	case KindRevoke:
		return "REVOKE"
	case KindSet:
		return "SET"
	case KindKill:
		return "KILL"
	case KindFlush:
		return "FLUSH"
	case KindStart:
		return "START"
	case KindCommit:
		return "COMMIT"
	case KindRollback:
		return "ROLLBACK"
	case KindSavepoint:
		return "SAVEPOINT"
	case KindReplica:
		return "REPLICA"
	case KindCall:
		return "CALL"
	case KindUse:
		return "USE"
	case KindBegin:
		return "BEGIN"
	case KindReset:
		return "RESET"
	case KindLockTables:
		return "LOCK TABLES"
	case KindUnlockTables:
		return "UNLOCK TABLES"
	case KindHandler:
		return "HANDLER"
	default:
		return "UNKNOWN"
	}
}

// ParsedQuery is the result of running Classify against an entire SQL
// input. A multi-statement query produces one ParsedStatement per
// statement; ParsedQuery.Class is the strictest class among them.
type ParsedQuery struct {
	// Class is the effective class for authorization — the strictest
	// across all statements. If any statement is ClassForbidden, the
	// whole query is ClassForbidden.
	Class QueryClass

	// Statements lists every statement in declaration order. Empty
	// when the input was empty or only whitespace + comments.
	Statements []ParsedStatement

	// Forbidden lists the specific forbidden patterns that fired, in
	// declaration order. Empty when Class != ClassForbidden. The
	// engine surfaces these to the operator in the error message so
	// they understand which specific feature is blocked.
	Forbidden []ForbiddenMatch

	// ParseSource is the aggregate provenance of this parse. PR #2.5+:
	// ParseSourceAST when every statement came from the AST parser,
	// ParseSourceFallback when every statement came from the
	// tokenizer fallback, ParseSourceMixed when statements split
	// between the two sources. New in PR #2.5; older code that did
	// not set this field reads the zero value (ParseSourceAST). The
	// authorization layer does not read this field; it is purely
	// informational for audit/debug surfaces.
	ParseSource ParseSource
}

// ParsedStatement is one statement in a multi-statement query.
type ParsedStatement struct {
	// Class is this specific statement's class.
	Class QueryClass

	// Kind identifies the leading keyword (SELECT, INSERT, etc.).
	Kind StatementKind

	// Action is the dbadmin.Action that authorizes this statement.
	// Empty for ClassForbidden statements (no action authorizes them).
	Action dbadmin.Action

	// Tables lists the objects this statement touches. Populated by
	// the AST upgrade in PR #2.5 (Vitess for MySQL/MariaDB,
	// pg_query_go for Postgres). The Target.ConnectionID field is
	// left empty by the classifier; callers populate it from the
	// request context. Statements whose source falls back to the
	// tokenizer (ParseSource == ParseSourceFallback) leave Tables
	// nil — hosts that depend on per-table authorization must treat
	// that as "unknown tables touched" and refuse, or downgrade to
	// per-connection authorization.
	Tables []dbadmin.Target

	// HasWhere is true for UPDATE/DELETE statements that include a
	// WHERE clause. Used to distinguish ClassWriteRow from
	// ClassWriteRowMass without re-tokenizing the statement.
	HasWhere bool

	// RawText is the verbatim statement text (minus the trailing
	// statement separator). Useful for audit logging.
	RawText string

	// Offset is the byte offset of this statement within the original
	// input. Useful for error reporting ("statement at byte 47 is
	// forbidden").
	Offset int

	// ParseSource records whether this specific statement was
	// classified by the AST parser (ParseSourceAST) or the tokenizer
	// fallback (ParseSourceFallback). New in PR #2.5; pre-PR #2.5
	// hosts that read the zero value see ParseSourceAST, which is
	// the cascade's optimistic default. See ParsedQuery.ParseSource
	// for the aggregate.
	ParseSource ParseSource
}

// ForbiddenMatch describes why a statement was classified as forbidden.
type ForbiddenMatch struct {
	// Pattern names the forbidden pattern that fired (e.g.,
	// "LOAD_FILE", "INTO OUTFILE", "COPY ... FROM PROGRAM").
	Pattern string

	// Reason explains the security implication in operator-friendly
	// terms (e.g., "function can read arbitrary files on the database
	// server").
	Reason string

	// StatementIndex points at the offending statement in
	// ParsedQuery.Statements.
	StatementIndex int

	// TokenOffset is the byte offset of the matched token sequence
	// within the original input.
	TokenOffset int
}

// Parser is the interface the classifier uses to extract statement
// classification from SQL. PR #2 ships a tokenizer-based default
// implementation; PR #2.5 may add AST-based implementations backed by
// Vitess (MySQL) and pg_query_go (Postgres).
//
// Hosts MAY substitute their own Parser, but doing so opts out of the
// security guarantees in SECURITY.md §6.3 — auraCP and aura-db
// distributions use the bundled tokenizer-based parser exclusively.
type Parser interface {
	// Parse returns the structured parse of sql. Implementations MUST
	// be conservative: when classification is ambiguous, prefer the
	// stricter class. The classifier consumes Parse's output without
	// re-validation.
	Parse(sql string) (ParsedQuery, error)
}

// Classify is the package's public entry point. Selects the parser for
// the given engine and returns the parse result.
//
// Returns an error only when the engine is unsupported or sql exceeds
// the maximum size handled by the lexer. ErrTooLarge keeps lexer memory
// bounded; the engine enforces the operator-visible size cap separately
// at the HTTP layer.
func Classify(engine dbadmin.EngineKind, sql string) (ParsedQuery, error) {
	if len(sql) > maxSQLBytes {
		return ParsedQuery{}, ErrTooLarge
	}
	switch engine {
	case dbadmin.EngineMariaDB:
		return mysqlParser.Parse(sql)
	case dbadmin.EnginePostgres:
		return postgresParser.Parse(sql)
	case dbadmin.EngineMongo:
		// MongoDB connections do not accept raw SQL — the document
		// store speaks BSON commands. Rather than build a parallel
		// MQL classifier (deferred until UI scope demands it), refuse
		// raw SQL outright at this layer so the panel's /classify
		// and /query handlers cleanly reject the request and the UI
		// can hide the SQL editor for Mongo connections. The
		// structured rows.Operator surface remains available and is
		// authorized via ActionRowRead / ActionRowWrite as usual.
		return ParsedQuery{
			Class: ClassForbidden,
			Statements: []ParsedStatement{{
				Class:   ClassForbidden,
				Kind:    KindUnknown,
				RawText: sql,
			}},
			Forbidden: []ForbiddenMatch{{
				Pattern: "RAW_SQL_ON_MONGO",
				Reason:  "MongoDB connections do not accept raw SQL; use the structured row operations API",
			}},
			ParseSource: ParseSourceFallback,
		}, nil
	default:
		return ParsedQuery{}, fmt.Errorf("classifier: unsupported engine %v", engine)
	}
}

// maxSQLBytes caps the byte length the lexer will scan. The host-level
// QueryConfig.SQLInputMaxBytes is the operator-tunable limit; this is
// the lexer-internal upper bound that exists to keep tokenizer memory
// from being attacker-controllable.
const maxSQLBytes = 16 * 1024 * 1024 // 16 MiB; matches SECURITY.md §6.5 hard ceiling.

// ErrTooLarge is returned by Classify when the SQL exceeds maxSQLBytes.
var ErrTooLarge = fmt.Errorf("classifier: SQL exceeds %d bytes", maxSQLBytes)

// Default parser instances. Constructed once at package init and shared
// (parsers are stateless — they tokenize a fresh input on every call).
//
// PR #2.5: each engine's parser is now a cascade — the AST classifier
// (Vitess for MySQL, pg_query_go for Postgres) runs first, and the
// tokenizer-based classifier from PR #2 is the fallback when the AST
// parser fails or panics. The forbidden-token matcher runs
// unconditionally inside the cascade as the no-override defense from
// SECURITY.md §6.3.2.
var (
	mysqlParser    Parser = newCascadeParser(newMySQLASTClassifier(), &mysqlTokenizer{}, DialectMySQL)
	postgresParser Parser = newCascadeParser(newPostgresASTClassifier(), &postgresTokenizer{}, DialectPostgres)
)

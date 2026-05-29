package classifier

import (
	"strings"
	"sync"

	"github.com/auracp/auracp/pkg/dbadmin"
	"vitess.io/vitess/go/vt/sqlparser"
)

// mysqlASTClassifier is the PR #2.5 AST-driven classifier for
// MariaDB/MySQL. It uses vitess.io/vitess/go/vt/sqlparser. The
// classifier never returns an error: parse failures degrade to a
// per-statement fallback marker so the cascade can substitute the
// tokenizer's result.
//
// Coverage is intentionally narrower than the tokenizer for one
// category (vendor-specific statements like GRANT/REVOKE that vitess
// flags as UNUSED). The cascade catches those via fallback; the
// tokenizer's CVE-historical forbidden coverage remains intact.
type mysqlASTClassifier struct {
	parser *sqlparser.Parser
}

var mysqlASTOnce sync.Once

func newMySQLASTClassifier() *mysqlASTClassifier {
	c := &mysqlASTClassifier{}
	mysqlASTOnce.Do(func() {})
	p, err := sqlparser.New(sqlparser.Options{
		TruncateUILen:  4096,
		TruncateErrLen: 4096,
	})
	if err != nil {
		// Falling through with parser == nil makes every Parse()
		// call return errASTUnavailable, which the cascade handles.
		return c
	}
	c.parser = p
	return c
}

// Parse implements Parser by running the vitess parser per statement.
// See cascade.go for the AST↔tokenizer merge contract.
func (m *mysqlASTClassifier) Parse(sql string) (ParsedQuery, error) {
	if m.parser == nil {
		return ParsedQuery{}, errASTUnavailable
	}

	// Use the lexer's split (already battle-tested + dialect-aware)
	// rather than vitess's own SplitStatementToPieces. This keeps
	// statement-index alignment perfect with matchForbidden, which
	// also walks the same Lex output.
	tokens := Lex(sql, LexOptions{Dialect: DialectMySQL})
	slices := splitStatements(tokens, sql)
	if len(slices) == 0 {
		return ParsedQuery{
			Statements:  nil,
			ParseSource: ParseSourceAST,
		}, nil
	}

	out := make([]ParsedStatement, 0, len(slices))
	for _, slice := range slices {
		stmt := m.parseOne(slice)
		out = append(out, stmt)
	}

	return ParsedQuery{
		Statements:  out,
		Class:       strictestClass(out),
		ParseSource: aggregateParseSource(out),
	}, nil
}

// parseOne classifies a single statement slice. If vitess refuses the
// input, parseOne falls back to the tokenizer classifier for that
// statement and returns ParseSource=ParseSourceFallback so the cascade
// can compute the aggregate.
func (m *mysqlASTClassifier) parseOne(slice statementSlice) ParsedStatement {
	text := slice.text
	if text == "" {
		// Pure whitespace/comments. classifyStatement gives a stable
		// "unknown read" entry which is what the tokenizer would
		// produce too. Keep ParseSource=AST so empty inputs do not
		// trip the Mixed aggregate.
		ps := classifyStatement(slice, DialectMySQL)
		ps.ParseSource = ParseSourceAST
		return ps
	}

	stmt, err := m.parser.Parse(text)
	if err != nil || stmt == nil {
		// Per-statement fallback: re-run the tokenizer on the same
		// slice. The forbidden matcher in the cascade still has its
		// independent gate on the raw token stream.
		astFallbackTotal.Add(1)
		ps := classifyStatement(slice, DialectMySQL)
		ps.ParseSource = ParseSourceFallback
		return ps
	}

	kind, class, action := mysqlKindClassActionFromAST(stmt)
	// Refine ambiguous AST kinds using the lexical leading keyword.
	// vitess collapses DESCRIBE/DESC + DESCRIBE SELECT and BEGIN/START
	// TRANSACTION into a single AST node; the tokenizer kept them
	// distinct because callers (audit + UI) display them differently.
	kind = refineMySQLKindFromLead(kind, slice.tokens)
	hasWhere := mysqlHasWhereFromAST(stmt)

	// Refine UPDATE/DELETE class by WHERE: without WHERE, mass.
	if (kind == KindUpdate || kind == KindDelete) && !hasWhere {
		class = ClassWriteRowMass
	}

	tables := collectTablesMySQL(stmt)

	return ParsedStatement{
		Class:       class,
		Kind:        kind,
		Action:      action,
		Tables:      tables,
		HasWhere:    hasWhere,
		RawText:     text,
		Offset:      slice.offset,
		ParseSource: ParseSourceAST,
	}
}

// mysqlKindClassActionFromAST maps a parsed vitess Statement to
// (Kind, Class, Action). UPDATE/DELETE class is refined by the caller
// based on the presence of WHERE.
func mysqlKindClassActionFromAST(stmt sqlparser.Statement) (StatementKind, QueryClass, dbadmin.Action) {
	switch s := stmt.(type) {
	case *sqlparser.Select, *sqlparser.Union:
		return KindSelect, ClassRead, dbadmin.ActionQueryRead
	case *sqlparser.Show:
		_ = s
		return KindShow, ClassRead, dbadmin.ActionQueryRead
	case *sqlparser.Use:
		return KindUse, ClassRead, dbadmin.ActionQueryRead
	case *sqlparser.ExplainStmt, *sqlparser.ExplainTab, *sqlparser.VExplainStmt:
		return KindExplain, ClassRead, dbadmin.ActionQueryRead
	case *sqlparser.OtherAdmin:
		// REPAIR / OPTIMIZE / ANALYZE / CHECK — DDL class is the
		// conservative pick (doc.go contract).
		return KindAnalyze, ClassDDL, dbadmin.ActionQueryDDL
	case *sqlparser.Insert:
		if s.Action == sqlparser.ReplaceAct {
			return KindReplace, ClassWriteRow, dbadmin.ActionQueryWrite
		}
		return KindInsert, ClassWriteRow, dbadmin.ActionQueryWrite
	case *sqlparser.Update:
		return KindUpdate, ClassWriteRow, dbadmin.ActionQueryWrite
	case *sqlparser.Delete:
		return KindDelete, ClassWriteRow, dbadmin.ActionQueryWrite
	case *sqlparser.TruncateTable:
		return KindTruncate, ClassWriteRowMass, dbadmin.ActionQueryWrite
	case *sqlparser.CreateTable, *sqlparser.CreateView, *sqlparser.CreateDatabase, *sqlparser.CreateProcedure:
		return KindCreate, ClassDDL, dbadmin.ActionQueryDDL
	case *sqlparser.AlterTable, *sqlparser.AlterView, *sqlparser.AlterDatabase:
		return KindAlter, ClassDDL, dbadmin.ActionQueryDDL
	case *sqlparser.DropTable, *sqlparser.DropView, *sqlparser.DropDatabase, *sqlparser.DropProcedure:
		return KindDrop, ClassDDL, dbadmin.ActionQueryDDL
	case *sqlparser.RenameTable:
		return KindRename, ClassDDL, dbadmin.ActionQueryDDL
	case *sqlparser.Analyze:
		return KindAnalyze, ClassDDL, dbadmin.ActionQueryDDL
	case *sqlparser.CallProc:
		return KindCall, ClassWriteRow, dbadmin.ActionQueryWrite
	case *sqlparser.LockTables:
		return KindLockTables, ClassWriteRow, dbadmin.ActionQueryWrite
	case *sqlparser.UnlockTables:
		return KindUnlockTables, ClassRead, dbadmin.ActionQueryRead
	case *sqlparser.Begin:
		return KindBegin, ClassRead, dbadmin.ActionQueryRead
	case *sqlparser.Commit:
		return KindCommit, ClassRead, dbadmin.ActionQueryRead
	case *sqlparser.Rollback, *sqlparser.SRollback:
		return KindRollback, ClassRead, dbadmin.ActionQueryRead
	case *sqlparser.Savepoint:
		return KindSavepoint, ClassRead, dbadmin.ActionQueryRead
	case *sqlparser.Release:
		return KindSavepoint, ClassRead, dbadmin.ActionQueryRead
	case *sqlparser.Set:
		if setIsGlobalScopeAST(s) {
			return KindSet, ClassDangerous, dbadmin.ActionQueryDangerous
		}
		return KindSet, ClassRead, dbadmin.ActionQueryRead
	case *sqlparser.Flush:
		return KindFlush, ClassDangerous, dbadmin.ActionQueryDangerous
	case *sqlparser.Kill:
		return KindKill, ClassDangerous, dbadmin.ActionQueryDangerous
	case *sqlparser.CommentOnly:
		return KindUnknown, ClassRead, dbadmin.ActionQueryRead
	case *sqlparser.Stream, *sqlparser.VStream:
		// Vitess-internal; treat as read for conservatism (they
		// don't write).
		return KindSelect, ClassRead, dbadmin.ActionQueryRead
	case *sqlparser.Load:
		// LOAD DATA — the forbidden matcher gates this on path.
		return KindUnknown, ClassDDL, dbadmin.ActionQueryDDL
	case *sqlparser.PrepareStmt, *sqlparser.ExecuteStmt, *sqlparser.DeallocateStmt:
		// PREPARE / EXECUTE / DEALLOCATE: opaque body. Conservative:
		// treat as DDL.
		return KindUnknown, ClassDDL, dbadmin.ActionQueryDDL
	}
	// Unknown statement type — fall back to DDL class so the
	// authorization layer requires a DBA. The tokenizer does the same
	// for unrecognized leading keywords.
	return KindUnknown, ClassDDL, dbadmin.ActionQueryDDL
}

// setIsGlobalScopeAST mirrors setIsGlobalScope (tokenizer) but reads
// scope directly from the AST. GLOBAL / PERSIST / PERSIST_ONLY are
// dangerous; SESSION / LOCAL / NoScope (default = session) are read.
func setIsGlobalScopeAST(s *sqlparser.Set) bool {
	for _, expr := range s.Exprs {
		if expr == nil || expr.Var == nil {
			continue
		}
		switch expr.Var.Scope {
		case sqlparser.GlobalScope, sqlparser.PersistSysScope, sqlparser.PersistOnlySysScope:
			return true
		}
	}
	return false
}

// mysqlHasWhereFromAST reads UPDATE.Where / DELETE.Where directly.
func mysqlHasWhereFromAST(stmt sqlparser.Statement) bool {
	switch s := stmt.(type) {
	case *sqlparser.Update:
		return s.Where != nil
	case *sqlparser.Delete:
		return s.Where != nil
	}
	return false
}

// collectTablesMySQL walks the vitess AST and returns every table the
// statement references. Subqueries contribute too — per-table
// authorization in PR #4 needs to know which tables a SELECT reads
// even when nested inside another statement.
//
// Implementation note: a naive sqlparser.Walk over TableName double-
// counts because ColName.Qualifier is also of type TableName, and a
// JOIN's ON clause references aliases as ColName.Qualifier. We avoid
// the double-count by recording TableName ONLY when it appears as an
// AliasedTableExpr.Expr or in a dialect-specific slot (Insert.Table,
// CreateTable.Table, etc.). The walker then skips into ColName.
func collectTablesMySQL(stmt sqlparser.Statement) []dbadmin.Target {
	c := newTableCollector()

	// CTE-binding names that should NOT be recorded as tables. We
	// gather them in a first pass; the walk then skips TableName
	// references whose unqualified name + empty schema matches.
	cteNames := collectCTENames(stmt)

	addTableName := func(tn sqlparser.TableName) {
		name := tn.Name.String()
		if name == "" {
			return
		}
		schema := tn.Qualifier.String()
		if schema == "" {
			lower := strings.ToLower(name)
			if _, isCTE := cteNames[lower]; isCTE {
				return
			}
			// Vitess synthesises the MySQL pseudo-table "dual" for
			// FROM-less SELECTs (e.g. SELECT 1). It is never a real
			// object the operator needs auth for; skip.
			if lower == "dual" {
				return
			}
		}
		c.add(schema, name)
	}

	_ = sqlparser.Walk(func(node sqlparser.SQLNode) (kontinue bool, err error) {
		switch n := node.(type) {
		case *sqlparser.AliasedTableExpr:
			// FROM-clause table. The Expr can be a TableName, a
			// DerivedTable (subquery), or other simple table
			// expressions. Only TableName contributes a real table.
			if tn, ok := n.Expr.(sqlparser.TableName); ok {
				addTableName(tn)
			}
			return true, nil
		case *sqlparser.Insert:
			if n.Table != nil {
				if tn, ok := n.Table.Expr.(sqlparser.TableName); ok {
					addTableName(tn)
				}
			}
			return true, nil
		case *sqlparser.CreateTable:
			addTableName(n.Table)
			return true, nil
		case *sqlparser.CreateView:
			addTableName(n.ViewName)
			return true, nil
		case *sqlparser.AlterTable:
			addTableName(n.Table)
			return true, nil
		case *sqlparser.AlterView:
			addTableName(n.ViewName)
			return true, nil
		case *sqlparser.DropTable:
			for _, tn := range n.FromTables {
				addTableName(tn)
			}
			return true, nil
		case *sqlparser.DropView:
			for _, tn := range n.FromTables {
				addTableName(tn)
			}
			return true, nil
		case *sqlparser.TruncateTable:
			addTableName(n.Table)
			return true, nil
		case *sqlparser.RenameTable:
			for _, pair := range n.TablePairs {
				addTableName(pair.FromTable)
				addTableName(pair.ToTable)
			}
			return true, nil
		case *sqlparser.Delete:
			// Targets is the list of table names in
			// "DELETE t1, t2 FROM ..." form; the TableExprs slot
			// holds the FROM and is walked by AliasedTableExpr.
			for _, tn := range n.Targets {
				addTableName(tn)
			}
			return true, nil
		case *sqlparser.ColName:
			// Suppress the default walk into ColName.Qualifier
			// (which is a TableName but represents an alias, not a
			// schema reference).
			return false, nil
		}
		return true, nil
	}, stmt)

	return c.targets()
}

// refineMySQLKindFromLead is the small reconciliation layer between
// vitess's AST collapse and the tokenizer's per-keyword Kind. Without
// this, DESCRIBE becomes KindExplain and START TRANSACTION becomes
// KindBegin — both regressions the existing test suite catches.
func refineMySQLKindFromLead(astKind StatementKind, tokens []Token) StatementKind {
	first := firstSignificantIdent(tokens)
	if first == nil {
		return astKind
	}
	switch upper(first.Text) {
	case "DESC", "DESCRIBE":
		if astKind == KindExplain {
			return KindDescribe
		}
	case "START":
		if astKind == KindBegin {
			return KindStart
		}
	}
	return astKind
}

// collectCTENames returns a set (lowercased) of CTE binding names used
// in the statement. These are NOT real tables and must be excluded
// from collectTablesMySQL's output.
func collectCTENames(stmt sqlparser.Statement) map[string]struct{} {
	out := map[string]struct{}{}
	_ = sqlparser.Walk(func(node sqlparser.SQLNode) (bool, error) {
		if cte, ok := node.(*sqlparser.CommonTableExpr); ok && cte != nil {
			name := cte.ID.String()
			if name != "" {
				out[strings.ToLower(name)] = struct{}{}
			}
		}
		return true, nil
	}, stmt)
	return out
}

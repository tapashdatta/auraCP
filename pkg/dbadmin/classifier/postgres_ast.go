//go:build cgo

package classifier

import (
	"strings"

	"github.com/auracp/auracp/pkg/dbadmin"
	pgquery "github.com/pganalyze/pg_query_go/v5"
)

// postgresASTClassifier is the PR #2.5 AST-driven classifier for
// PostgreSQL. It uses github.com/pganalyze/pg_query_go/v5 which embeds
// libpg_query (the parser from the real Postgres source tree) via cgo.
//
// Build constraint: this file is included only when cgo is enabled.
// The companion postgres_ast_nocgo.go (built when cgo=0) emits an
// always-failing classifier that the cascade silently degrades to
// tokenizer-only operation for.
type postgresASTClassifier struct{}

func newPostgresASTClassifier() *postgresASTClassifier {
	return &postgresASTClassifier{}
}

// Parse implements Parser. Unlike the MySQL AST classifier (which can
// parse one statement at a time), pg_query_go parses the entire input
// at once and returns []*RawStmt with explicit StmtLocation/StmtLen
// offsets. We still align indices with splitStatements by skipping
// pg_query's empty entries the same way splitStatements skips empty
// token slices.
func (p *postgresASTClassifier) Parse(sql string) (ParsedQuery, error) {
	if strings.TrimSpace(sql) == "" {
		return ParsedQuery{ParseSource: ParseSourceAST}, nil
	}

	// Per-statement fallback uses the tokenizer's slice list. Lex
	// the input once so we have offsets that match the forbidden
	// matcher's StatementIndex.
	tokens := Lex(sql, LexOptions{Dialect: DialectPostgres})
	tokenSlices := splitStatements(tokens, sql)

	parsed, err := pgquery.Parse(sql)
	if err != nil {
		return ParsedQuery{}, errASTParseFailed
	}

	// pg_query's Stmts and our tokenSlices SHOULD have the same
	// length when both succeed (both skip empty statements). When
	// they disagree (extension syntax pg_query rejects but the
	// tokenizer happily handled, or vice versa) we use the
	// tokenizer's split as the authority and mark mismatched
	// indices as fallback.
	astByIdx := map[int]*pgquery.RawStmt{}
	for i, raw := range parsed.Stmts {
		if raw == nil || raw.Stmt == nil {
			continue
		}
		astByIdx[i] = raw
	}

	out := make([]ParsedStatement, 0, len(tokenSlices))
	var astForbidden []ForbiddenMatch
	for i, slice := range tokenSlices {
		raw, ok := astByIdx[i]
		if !ok {
			astFallbackTotal.Add(1)
			ps := classifyStatement(slice, DialectPostgres)
			ps.ParseSource = ParseSourceFallback
			out = append(out, ps)
			continue
		}
		ps := classifyPostgresStatement(raw, slice)
		// Emit AST-detected forbidden patterns that the tokenizer
		// can miss (e.g. quoted-identifier LANGUAGE "plpythonu").
		// The cascade merges these with the raw-token matcher's
		// hits in mergeForbidden.
		hits := detectPostgresASTForbidden(raw, i, slice.offset)
		astForbidden = append(astForbidden, hits...)
		out = append(out, ps)
	}

	return ParsedQuery{
		Statements:  out,
		Class:       strictestClass(out),
		Forbidden:   astForbidden,
		ParseSource: aggregateParseSource(out),
	}, nil
}

// detectPostgresASTForbidden scans the parsed AST for forbidden
// constructs that the tokenizer's IDENT-only matcher cannot reach:
// notably quoted-identifier language clauses on CREATE FUNCTION.
// Returns []ForbiddenMatch (possibly empty); each match aligns with
// stmtIdx and has TokenOffset set to the statement's start byte for
// audit reporting.
func detectPostgresASTForbidden(raw *pgquery.RawStmt, stmtIdx int, byteOffset int) []ForbiddenMatch {
	var hits []ForbiddenMatch
	if raw == nil || raw.Stmt == nil {
		return nil
	}
	switch n := raw.Stmt.GetNode().(type) {
	case *pgquery.Node_CreateFunctionStmt:
		if lang := extractFunctionLanguage(n.CreateFunctionStmt); lang != "" {
			if isUntrustedPLLanguage(lang) {
				hits = append(hits, ForbiddenMatch{
					Pattern:        "Untrusted PL/* function language",
					Reason:         "PL/Python-untrusted, PL/Perl-untrusted, PL/sh, and similar untrusted procedural languages permit arbitrary code execution on the server.",
					StatementIndex: stmtIdx,
					TokenOffset:    byteOffset,
				})
			}
		}
	}
	return hits
}

// extractFunctionLanguage returns the LANGUAGE clause value from a
// CREATE FUNCTION statement. Empty when no LANGUAGE option is present.
func extractFunctionLanguage(s *pgquery.CreateFunctionStmt) string {
	if s == nil {
		return ""
	}
	for _, opt := range s.GetOptions() {
		if opt == nil {
			continue
		}
		def, ok := opt.GetNode().(*pgquery.Node_DefElem)
		if !ok || def.DefElem == nil {
			continue
		}
		if strings.EqualFold(def.DefElem.GetDefname(), "language") {
			arg := def.DefElem.GetArg()
			if arg == nil {
				continue
			}
			if s, ok := arg.GetNode().(*pgquery.Node_String_); ok {
				return s.String_.GetSval()
			}
		}
	}
	return ""
}

// isUntrustedPLLanguage reports whether the given language name (any
// quoting stripped by the parser) is on the SECURITY.md §6.3.2
// untrusted-procedural-language list.
func isUntrustedPLLanguage(name string) bool {
	switch strings.ToLower(name) {
	case "plpythonu", "plpython3u", "plperlu", "plsh", "plv8u", "plr":
		return true
	}
	return false
}

func classifyPostgresStatement(raw *pgquery.RawStmt, slice statementSlice) ParsedStatement {
	node := raw.GetStmt()
	if node == nil {
		ps := classifyStatement(slice, DialectPostgres)
		ps.ParseSource = ParseSourceFallback
		return ps
	}
	kind, class, action := postgresKindClassActionFromAST(node)
	hasWhere := postgresHasWhereFromAST(node)
	if (kind == KindUpdate || kind == KindDelete) && !hasWhere {
		class = ClassWriteRowMass
	}
	tables := collectTablesPostgres(node)
	return ParsedStatement{
		Class:       class,
		Kind:        kind,
		Action:      action,
		Tables:      tables,
		HasWhere:    hasWhere,
		RawText:     slice.text,
		Offset:      slice.offset,
		ParseSource: ParseSourceAST,
	}
}

func postgresKindClassActionFromAST(node *pgquery.Node) (StatementKind, QueryClass, dbadmin.Action) {
	switch n := node.GetNode().(type) {
	case *pgquery.Node_SelectStmt:
		return KindSelect, ClassRead, dbadmin.ActionQueryRead
	case *pgquery.Node_InsertStmt:
		return KindInsert, ClassWriteRow, dbadmin.ActionQueryWrite
	case *pgquery.Node_UpdateStmt:
		return KindUpdate, ClassWriteRow, dbadmin.ActionQueryWrite
	case *pgquery.Node_DeleteStmt:
		return KindDelete, ClassWriteRow, dbadmin.ActionQueryWrite
	case *pgquery.Node_MergeStmt:
		return KindMerge, ClassWriteRow, dbadmin.ActionQueryWrite
	case *pgquery.Node_TruncateStmt:
		return KindTruncate, ClassWriteRowMass, dbadmin.ActionQueryWrite
	case *pgquery.Node_CopyStmt:
		// COPY can be read (TO stdout) or write (FROM stdin). The
		// forbidden matcher gates COPY ... PROGRAM and COPY with a
		// file path. Here we just pick a class that authorizes a
		// writer at minimum, since the most common COPY in panel
		// usage is import.
		copyStmt := n.CopyStmt
		if copyStmt != nil && copyStmt.IsFrom {
			return KindUnknown, ClassWriteRow, dbadmin.ActionQueryWrite
		}
		return KindSelect, ClassRead, dbadmin.ActionQueryRead
	case *pgquery.Node_CreateStmt, *pgquery.Node_CreateTableAsStmt, *pgquery.Node_CreateSchemaStmt,
		*pgquery.Node_CreateSeqStmt, *pgquery.Node_CreateExtensionStmt,
		*pgquery.Node_ViewStmt, *pgquery.Node_IndexStmt, *pgquery.Node_CreateFunctionStmt,
		*pgquery.Node_CreateEnumStmt, *pgquery.Node_CreateRangeStmt, *pgquery.Node_CreateDomainStmt,
		*pgquery.Node_CreatePolicyStmt, *pgquery.Node_CreateTrigStmt:
		return KindCreate, ClassDDL, dbadmin.ActionQueryDDL
	case *pgquery.Node_AlterTableStmt, *pgquery.Node_AlterOwnerStmt, *pgquery.Node_AlterObjectSchemaStmt,
		*pgquery.Node_AlterDefaultPrivilegesStmt, *pgquery.Node_AlterExtensionStmt,
		*pgquery.Node_AlterFunctionStmt, *pgquery.Node_AlterPolicyStmt,
		*pgquery.Node_AlterRoleStmt, *pgquery.Node_AlterDatabaseStmt:
		return KindAlter, ClassDDL, dbadmin.ActionQueryDDL
	case *pgquery.Node_DropStmt, *pgquery.Node_DropOwnedStmt, *pgquery.Node_DropRoleStmt,
		*pgquery.Node_DropdbStmt, *pgquery.Node_DropSubscriptionStmt:
		return KindDrop, ClassDDL, dbadmin.ActionQueryDDL
	case *pgquery.Node_RenameStmt:
		return KindRename, ClassDDL, dbadmin.ActionQueryDDL
	case *pgquery.Node_ReindexStmt:
		return KindReindex, ClassDDL, dbadmin.ActionQueryDDL
	case *pgquery.Node_VacuumStmt:
		return KindVacuum, ClassDDL, dbadmin.ActionQueryDDL
	case *pgquery.Node_GrantStmt:
		gs := n.GrantStmt
		if gs != nil && !gs.IsGrant {
			return KindRevoke, ClassDangerous, dbadmin.ActionQueryDangerous
		}
		return KindGrant, ClassDangerous, dbadmin.ActionQueryDangerous
	case *pgquery.Node_GrantRoleStmt:
		gr := n.GrantRoleStmt
		if gr != nil && !gr.IsGrant {
			return KindRevoke, ClassDangerous, dbadmin.ActionQueryDangerous
		}
		return KindGrant, ClassDangerous, dbadmin.ActionQueryDangerous
	case *pgquery.Node_VariableSetStmt:
		// SET LOCAL / SET SESSION / SET TRANSACTION = read.
		// pg_query treats RESET and SET TIME ZONE the same way; the
		// keyword variant is captured in Kind.
		return KindSet, ClassRead, dbadmin.ActionQueryRead
	case *pgquery.Node_VariableShowStmt:
		return KindShow, ClassRead, dbadmin.ActionQueryRead
	case *pgquery.Node_ExplainStmt:
		return KindExplain, ClassRead, dbadmin.ActionQueryRead
	case *pgquery.Node_TransactionStmt:
		// BEGIN / COMMIT / ROLLBACK / SAVEPOINT / RELEASE — all
		// read class.
		ts := n.TransactionStmt
		switch ts.GetKind() {
		case pgquery.TransactionStmtKind_TRANS_STMT_BEGIN, pgquery.TransactionStmtKind_TRANS_STMT_START:
			return KindBegin, ClassRead, dbadmin.ActionQueryRead
		case pgquery.TransactionStmtKind_TRANS_STMT_COMMIT:
			return KindCommit, ClassRead, dbadmin.ActionQueryRead
		case pgquery.TransactionStmtKind_TRANS_STMT_ROLLBACK:
			return KindRollback, ClassRead, dbadmin.ActionQueryRead
		case pgquery.TransactionStmtKind_TRANS_STMT_SAVEPOINT, pgquery.TransactionStmtKind_TRANS_STMT_RELEASE,
			pgquery.TransactionStmtKind_TRANS_STMT_ROLLBACK_TO:
			return KindSavepoint, ClassRead, dbadmin.ActionQueryRead
		}
		return KindStart, ClassRead, dbadmin.ActionQueryRead
	case *pgquery.Node_PrepareStmt, *pgquery.Node_ExecuteStmt, *pgquery.Node_DeallocateStmt:
		// Opaque body — same conservative bias as MySQL.
		return KindUnknown, ClassDDL, dbadmin.ActionQueryDDL
	case *pgquery.Node_CreateRoleStmt:
		return KindCreate, ClassDangerous, dbadmin.ActionQueryDangerous
	case *pgquery.Node_CreatedbStmt:
		return KindCreate, ClassDDL, dbadmin.ActionQueryDDL
	}
	return KindUnknown, ClassDDL, dbadmin.ActionQueryDDL
}

func postgresHasWhereFromAST(node *pgquery.Node) bool {
	switch n := node.GetNode().(type) {
	case *pgquery.Node_UpdateStmt:
		return n.UpdateStmt.GetWhereClause() != nil
	case *pgquery.Node_DeleteStmt:
		return n.DeleteStmt.GetWhereClause() != nil
	}
	return false
}

// collectTablesPostgres walks the parsed RawStmt and returns every
// table it references. RangeVar.Schemaname → Target.Schema,
// RangeVar.Relname → Target.Object. CTE binding names are excluded.
func collectTablesPostgres(node *pgquery.Node) []dbadmin.Target {
	c := newTableCollector()
	cteNames := map[string]struct{}{}
	collectPostgresCTENames(node, cteNames)
	walkPostgresTables(node, c, cteNames)
	return c.targets()
}

// collectPostgresCTENames gathers CTE binding names from the AST. CTE
// aliases are NOT real tables and must be excluded from per-table
// auth; we record them here so walkPostgresTables can skip RangeVars
// whose name matches an unqualified CTE binding.
func collectPostgresCTENames(node *pgquery.Node, out map[string]struct{}) {
	if node == nil {
		return
	}
	switch n := node.GetNode().(type) {
	case *pgquery.Node_SelectStmt:
		visitWithClause(n.SelectStmt.GetWithClause(), out)
		for _, child := range n.SelectStmt.GetFromClause() {
			collectPostgresCTENames(child, out)
		}
		if n.SelectStmt.GetLarg() != nil {
			collectPostgresCTENames(wrapSelect(n.SelectStmt.GetLarg()), out)
		}
		if n.SelectStmt.GetRarg() != nil {
			collectPostgresCTENames(wrapSelect(n.SelectStmt.GetRarg()), out)
		}
	case *pgquery.Node_InsertStmt:
		visitWithClause(n.InsertStmt.GetWithClause(), out)
		collectPostgresCTENames(n.InsertStmt.GetSelectStmt(), out)
	case *pgquery.Node_UpdateStmt:
		visitWithClause(n.UpdateStmt.GetWithClause(), out)
	case *pgquery.Node_DeleteStmt:
		visitWithClause(n.DeleteStmt.GetWithClause(), out)
	case *pgquery.Node_MergeStmt:
		visitWithClause(n.MergeStmt.GetWithClause(), out)
	}
}

// wrapSelect packages a *SelectStmt as a *Node so the visitor can
// recurse. pg_query's UNION leaves (Larg/Rarg) are bare SelectStmt
// pointers and need re-wrapping.
func wrapSelect(s *pgquery.SelectStmt) *pgquery.Node {
	return &pgquery.Node{Node: &pgquery.Node_SelectStmt{SelectStmt: s}}
}

func visitWithClause(w *pgquery.WithClause, out map[string]struct{}) {
	if w == nil {
		return
	}
	for _, cteNode := range w.GetCtes() {
		if cteNode == nil {
			continue
		}
		if cw, ok := cteNode.GetNode().(*pgquery.Node_CommonTableExpr); ok {
			name := cw.CommonTableExpr.GetCtename()
			if name != "" {
				out[strings.ToLower(name)] = struct{}{}
			}
		}
	}
}

// walkPostgresTables traverses the AST recording RangeVar references.
// We hand-roll the walk because pg_query's protobuf nodes have no
// uniform visitor; we cover the cases that produce table references in
// practice.
func walkPostgresTables(node *pgquery.Node, c *tableCollector, ctes map[string]struct{}) {
	if node == nil {
		return
	}
	switch n := node.GetNode().(type) {
	case *pgquery.Node_RangeVar:
		addPostgresRangeVar(n.RangeVar, c, ctes)
	case *pgquery.Node_SelectStmt:
		s := n.SelectStmt
		// Walk WithClause CTE bodies (they touch real tables).
		if w := s.GetWithClause(); w != nil {
			for _, cteNode := range w.GetCtes() {
				if cteNode == nil {
					continue
				}
				if cw, ok := cteNode.GetNode().(*pgquery.Node_CommonTableExpr); ok {
					walkPostgresTables(cw.CommonTableExpr.GetCtequery(), c, ctes)
				}
			}
		}
		for _, f := range s.GetFromClause() {
			walkPostgresTables(f, c, ctes)
		}
		// WHERE / target list can contain subselects that reference
		// other tables.
		walkPostgresTables(s.GetWhereClause(), c, ctes)
		for _, t := range s.GetTargetList() {
			walkPostgresTables(t, c, ctes)
		}
		if s.GetLarg() != nil {
			walkPostgresTables(wrapSelect(s.GetLarg()), c, ctes)
		}
		if s.GetRarg() != nil {
			walkPostgresTables(wrapSelect(s.GetRarg()), c, ctes)
		}
	case *pgquery.Node_InsertStmt:
		addPostgresRangeVar(n.InsertStmt.GetRelation(), c, ctes)
		walkPostgresTables(n.InsertStmt.GetSelectStmt(), c, ctes)
		if w := n.InsertStmt.GetWithClause(); w != nil {
			for _, cteNode := range w.GetCtes() {
				if cw, ok := cteNode.GetNode().(*pgquery.Node_CommonTableExpr); ok {
					walkPostgresTables(cw.CommonTableExpr.GetCtequery(), c, ctes)
				}
			}
		}
	case *pgquery.Node_UpdateStmt:
		addPostgresRangeVar(n.UpdateStmt.GetRelation(), c, ctes)
		for _, f := range n.UpdateStmt.GetFromClause() {
			walkPostgresTables(f, c, ctes)
		}
		walkPostgresTables(n.UpdateStmt.GetWhereClause(), c, ctes)
	case *pgquery.Node_DeleteStmt:
		addPostgresRangeVar(n.DeleteStmt.GetRelation(), c, ctes)
		for _, f := range n.DeleteStmt.GetUsingClause() {
			walkPostgresTables(f, c, ctes)
		}
		walkPostgresTables(n.DeleteStmt.GetWhereClause(), c, ctes)
	case *pgquery.Node_MergeStmt:
		addPostgresRangeVar(n.MergeStmt.GetRelation(), c, ctes)
		walkPostgresTables(n.MergeStmt.GetSourceRelation(), c, ctes)
	case *pgquery.Node_CopyStmt:
		addPostgresRangeVar(n.CopyStmt.GetRelation(), c, ctes)
		walkPostgresTables(n.CopyStmt.GetQuery(), c, ctes)
	case *pgquery.Node_CreateStmt:
		addPostgresRangeVar(n.CreateStmt.GetRelation(), c, ctes)
	case *pgquery.Node_CreateTableAsStmt:
		// CREATE TABLE ... AS SELECT records both target + source.
		if into := n.CreateTableAsStmt.GetInto(); into != nil {
			addPostgresRangeVar(into.GetRel(), c, ctes)
		}
		walkPostgresTables(n.CreateTableAsStmt.GetQuery(), c, ctes)
	case *pgquery.Node_AlterTableStmt:
		addPostgresRangeVar(n.AlterTableStmt.GetRelation(), c, ctes)
	case *pgquery.Node_RenameStmt:
		addPostgresRangeVar(n.RenameStmt.GetRelation(), c, ctes)
	case *pgquery.Node_DropStmt:
		// DropStmt.Objects is []*Node where each is typically a
		// List of String. We don't reconstruct schema-qualified
		// names here; the per-table auth in PR #4 may extend this.
		for _, obj := range n.DropStmt.GetObjects() {
			extractDropObject(obj, c)
		}
	case *pgquery.Node_TruncateStmt:
		for _, r := range n.TruncateStmt.GetRelations() {
			walkPostgresTables(r, c, ctes)
		}
	case *pgquery.Node_IndexStmt:
		addPostgresRangeVar(n.IndexStmt.GetRelation(), c, ctes)
	case *pgquery.Node_VacuumStmt:
		for _, rel := range n.VacuumStmt.GetRels() {
			walkPostgresTables(rel, c, ctes)
		}
	case *pgquery.Node_ReindexStmt:
		addPostgresRangeVar(n.ReindexStmt.GetRelation(), c, ctes)
	case *pgquery.Node_VacuumRelation:
		addPostgresRangeVar(n.VacuumRelation.GetRelation(), c, ctes)
	case *pgquery.Node_GrantStmt:
		// GrantStmt.Objects depends on Objtype. When ObjectType is
		// OBJECT_TABLE, Objects is []*Node of *Node_RangeVar.
		for _, obj := range n.GrantStmt.GetObjects() {
			walkPostgresTables(obj, c, ctes)
		}
	case *pgquery.Node_RangeSubselect:
		walkPostgresTables(n.RangeSubselect.GetSubquery(), c, ctes)
	case *pgquery.Node_JoinExpr:
		walkPostgresTables(n.JoinExpr.GetLarg(), c, ctes)
		walkPostgresTables(n.JoinExpr.GetRarg(), c, ctes)
	case *pgquery.Node_SubLink:
		walkPostgresTables(n.SubLink.GetSubselect(), c, ctes)
	case *pgquery.Node_BoolExpr:
		for _, a := range n.BoolExpr.GetArgs() {
			walkPostgresTables(a, c, ctes)
		}
	case *pgquery.Node_AExpr:
		walkPostgresTables(n.AExpr.GetLexpr(), c, ctes)
		walkPostgresTables(n.AExpr.GetRexpr(), c, ctes)
	case *pgquery.Node_ResTarget:
		walkPostgresTables(n.ResTarget.GetVal(), c, ctes)
	case *pgquery.Node_FuncCall:
		// Function arguments may contain subqueries.
		for _, a := range n.FuncCall.GetArgs() {
			walkPostgresTables(a, c, ctes)
		}
	}
}

func addPostgresRangeVar(rv *pgquery.RangeVar, c *tableCollector, ctes map[string]struct{}) {
	if rv == nil {
		return
	}
	name := rv.GetRelname()
	if name == "" {
		return
	}
	schema := rv.GetSchemaname()
	if schema == "" {
		if _, isCTE := ctes[strings.ToLower(name)]; isCTE {
			return
		}
	}
	c.add(schema, name)
}

// extractDropObject pulls a (schema, object) pair from a DROP statement's
// Objects element. Postgres represents the object name list as []*Node
// of String values when ObjectType is OBJECT_TABLE — e.g. ["myschema",
// "users"] for "DROP TABLE myschema.users".
func extractDropObject(obj *pgquery.Node, c *tableCollector) {
	if obj == nil {
		return
	}
	switch n := obj.GetNode().(type) {
	case *pgquery.Node_List:
		var parts []string
		for _, item := range n.List.GetItems() {
			if item == nil {
				continue
			}
			if s, ok := item.GetNode().(*pgquery.Node_String_); ok {
				parts = append(parts, s.String_.GetSval())
			}
		}
		switch len(parts) {
		case 1:
			c.add("", parts[0])
		case 2:
			c.add(parts[0], parts[1])
		case 3:
			// catalog.schema.object — keep schema.object.
			c.add(parts[1], parts[2])
		}
	case *pgquery.Node_RangeVar:
		addPostgresRangeVar(n.RangeVar, c, nil)
	case *pgquery.Node_String_:
		c.add("", n.String_.GetSval())
	}
}

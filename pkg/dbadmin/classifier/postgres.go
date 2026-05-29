package classifier

// postgresClassifier implements Parser for PostgreSQL.
//
// The classification core (splitStatements + classifyByKeyword) is shared
// with the MySQL classifier; this type only:
//
//   - Selects DialectPostgres for the lexer (so dollar-quoted strings and
//     standard double-quoted identifiers parse correctly).
//   - Runs the forbidden matcher with DialectPostgres so Postgres-specific
//     forbidden patterns (COPY ... FROM PROGRAM, pg_read_file, plpythonu,
//     etc.) fire.
//
// Engine-specific keyword recognition lives in classifyByKeyword's switch:
// Postgres-only keywords (REINDEX, VACUUM) are already handled there
// alongside their MySQL counterparts.
type postgresClassifier struct{}

func (p *postgresClassifier) Parse(sql string) (ParsedQuery, error) {
	tokens := Lex(sql, LexOptions{Dialect: DialectPostgres})
	forbidden := matchForbidden(tokens, DialectPostgres)

	stmts := splitStatements(tokens, sql)
	parsed := make([]ParsedStatement, 0, len(stmts))
	for _, s := range stmts {
		parsed = append(parsed, classifyStatement(s, DialectPostgres))
	}

	for _, f := range forbidden {
		if f.StatementIndex >= 0 && f.StatementIndex < len(parsed) {
			parsed[f.StatementIndex].Class = ClassForbidden
			parsed[f.StatementIndex].Action = ""
		}
	}

	return ParsedQuery{
		Class:      strictestClass(parsed),
		Statements: parsed,
		Forbidden:  forbidden,
	}, nil
}

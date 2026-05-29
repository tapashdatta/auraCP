//go:build !cgo

package classifier

import "log/slog"

// postgresASTClassifier (no-cgo build) is a stub. pg_query_go links
// libpg_query via cgo and cannot be built when CGO_ENABLED=0. The
// cascade detects errASTUnavailable and silently degrades to the
// tokenizer-based classifier for Postgres in this build — MySQL still
// gets the AST upgrade because Vitess is pure Go.
type postgresASTClassifier struct{}

func newPostgresASTClassifier() *postgresASTClassifier {
	slog.Info("classifier: Postgres AST disabled (no cgo); using tokenizer fallback (PR #2 behavior)")
	return &postgresASTClassifier{}
}

func (p *postgresASTClassifier) Parse(sql string) (ParsedQuery, error) {
	return ParsedQuery{}, errASTUnavailable
}

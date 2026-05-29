package classifier

// ParseSource identifies which underlying parser produced a
// classification result. New in PR #2.5 — purely informational; the
// authorization layer reads only ParsedQuery.Class / Statements /
// Forbidden. Hosts that want to surface parser provenance (audit logs,
// debug overlays) can read this field.
type ParseSource uint8

const (
	// ParseSourceAST means the per-statement AST parser (Vitess for
	// MySQL, pg_query_go for Postgres) produced the classification.
	ParseSourceAST ParseSource = iota

	// ParseSourceFallback means the tokenizer-based classifier (the
	// PR #2 implementation) produced the classification because the
	// AST parser failed, panicked, or was unavailable (no-cgo build
	// for Postgres).
	ParseSourceFallback

	// ParseSourceMixed appears only on ParsedQuery.ParseSource and
	// signals that statements in the multi-statement input came from
	// different sources (some AST, some fallback). Each individual
	// statement still has a single ParseSource of AST or Fallback.
	ParseSourceMixed
)

// String returns the canonical name for use in audit/debug output.
func (p ParseSource) String() string {
	switch p {
	case ParseSourceAST:
		return "ast"
	case ParseSourceFallback:
		return "fallback"
	case ParseSourceMixed:
		return "mixed"
	default:
		return "unknown"
	}
}

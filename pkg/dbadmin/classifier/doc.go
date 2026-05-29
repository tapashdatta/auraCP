// Package classifier parses and classifies SQL statements before they reach
// a database driver. Its primary job is the security-critical pre-execution
// filter described in SECURITY.md §6.3: refuse statements that hit the
// hard-forbidden list and label every other statement with the class that
// drives authorization + step-up + 4-eye decisions.
//
// Why this package exists:
//
// Historic Adminer and phpMyAdmin CVEs (LOAD_FILE-driven RCE,
// INTO-OUTFILE-driven file write, COPY-FROM-PROGRAM in Postgres, etc.)
// share one root cause: the application regex'd SQL to decide what was
// "safe" to run. Regex can't tell a SQL keyword inside a string literal
// from one outside; it can't strip a `/* ... */` comment without false
// positives; it can't see through dollar-quoted strings in Postgres.
// Every CVE class above had a bypass that exploited one of these gaps.
//
// This package takes the opposite approach: a proper lexical analysis
// that strips comments structurally, recognizes string + identifier
// boundaries, and matches the forbidden patterns against the TOKEN
// stream — not the raw text. A query like
//
//	SELECT /* harmless */ '; LOAD_FILE("/etc/shadow") --' AS x
//
// presents the forbidden function name inside a string literal; the
// tokenizer reports it as a STRING token, the forbidden matcher only
// looks at IDENT tokens, and the query is correctly classified as `read`
// (with one string literal that contains those exact bytes). No false
// negative.
//
// Strategy (PR #2):
//
// We ship a hand-written tokenizer plus a token-sequence pattern matcher.
// The Parser interface defined in this package is pluggable; PR #2.5 may
// swap in Vitess (for MySQL) and pg_query_go (for PostgreSQL) for full
// AST-level classification. Until then, the tokenizer covers the
// CVE-historical attack patterns with margin to spare, and the
// integration boundary is set so the upgrade is mechanical.
//
// Strategy (PR #2.5):
//
// AST primary, tokenizer fallback, forbidden matcher always-on. The
// classifier now wires the AST parser (vitess.io/vitess/go/vt/sqlparser
// for MariaDB/MySQL, github.com/pganalyze/pg_query_go/v5 for Postgres)
// in front of the PR #2 tokenizer. When the AST parser succeeds, its
// structured result drives Kind/Class/Action/Tables/HasWhere; when it
// fails or panics on a specific statement, the cascade transparently
// falls back to the PR #2 tokenizer for that statement. The
// forbidden-token matcher (forbidden.go) runs UNCONDITIONALLY against
// the raw token stream so that — even if a parser bug or vendor
// extension causes the AST to accept something dangerous — the lexer
// still catches LOAD_FILE/INTO OUTFILE/COPY FROM PROGRAM/pg_read_file/
// PLPYTHONU and similar patterns. See SECURITY.md §6.3.2 for the
// no-override contract that this defense-in-depth posture implements.
//
// The Postgres AST parser links libpg_query via cgo. Builds with
// CGO_ENABLED=0 silently degrade to the PR #2 tokenizer for Postgres
// (announced at process start via a single INFO-level log line);
// MySQL/MariaDB classification stays on the AST because Vitess is
// pure Go.
//
// Public surface:
//
//   - Classify(engine, sql) → ParsedQuery: the one function the engine
//     calls before dispatching a statement to a driver.
//   - QueryClass, StatementKind: enumerations of the security policy
//     classes and the concrete statement types.
//   - ParsedQuery, ParsedStatement: structured results.
//   - Forbidden(): inspect the hard-forbidden list at runtime.
//
// Stability: this package follows the SDK stability rules in
// docs/aura-db/SDK.md §8. Adding new forbidden patterns is additive-
// stable. Reclassifying a statement from a stricter class to a more
// permissive one is a breaking change (and requires an ADR).
package classifier

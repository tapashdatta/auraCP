package classifier

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync/atomic"
)

// cascadeParser is the PR #2.5 composite parser. It runs the AST parser
// first and falls back to the tokenizer per-statement when the AST
// parser fails. The forbidden-token matcher runs unconditionally over
// the raw token stream as the no-override defense from SECURITY.md
// §6.3.2.
//
// The cascade preserves these invariants:
//
//  1. Classify's public signature is unchanged.
//  2. A failure in the AST parser NEVER propagates as an error from
//     Parse — fallback succeeds because the tokenizer is total.
//  3. AST and tokenizer classifications are merged conservatively:
//     ClassForbidden wins over any other class, and the forbidden
//     matcher's hits are unioned with whatever the AST already reported.
//  4. statement-index alignment is preserved: the AST parser yields one
//     ParsedStatement per non-empty input statement, in the same order
//     splitStatements would have produced. The forbidden matcher's
//     StatementIndex aligns with that ordering.
type cascadeParser struct {
	ast      Parser
	fallback Parser
	dialect  Dialect
}

func newCascadeParser(ast, fallback Parser, dialect Dialect) *cascadeParser {
	return &cascadeParser{ast: ast, fallback: fallback, dialect: dialect}
}

// errASTUnavailable is returned by AST parsers that are compiled out
// (e.g. postgres_ast_nocgo.go). The cascade treats this as a permanent
// fallback signal and silently degrades to tokenizer-only operation for
// the dialect.
var errASTUnavailable = errors.New("classifier: AST parser unavailable in this build")

// errASTParseFailed is returned by AST parsers for a recoverable parse
// failure on a specific input. The cascade falls back per-statement.
var errASTParseFailed = errors.New("classifier: AST parse failed")

// astFallbackTotal counts AST→tokenizer fallbacks across the package's
// lifetime. PR #5 wires it to Prometheus; for now it is a package-level
// counter exposed for tests + debugging.
var astFallbackTotal atomic.Int64

// ASTFallbackTotal returns the running count of AST→tokenizer
// fallbacks. Exposed for tests + the (future) /metrics endpoint.
func ASTFallbackTotal() int64 { return astFallbackTotal.Load() }

// Parse runs the cascade: AST first, tokenizer for any statements the
// AST parser could not handle, and the forbidden matcher over the raw
// lexer stream.
func (c *cascadeParser) Parse(sql string) (ParsedQuery, error) {
	// Step 1: lex the raw SQL once. The output drives both the
	// forbidden matcher (always) and the tokenizer fallback (when
	// needed).
	rawTokens := Lex(sql, LexOptions{Dialect: c.dialect})
	rawForbidden := matchForbidden(rawTokens, c.dialect)

	// Step 2: try the AST parser. Recover any panic so a parser bug
	// cannot crash the classifier.
	astResult, astErr := c.safeASTParse(sql)

	// Step 3: handle the three top-level branches.
	switch {
	case errors.Is(astErr, errASTUnavailable):
		// No AST available for this dialect in this build. Use the
		// tokenizer's full result and substitute the (identical)
		// rawForbidden so callers see a single set of matches.
		fallback, _ := c.fallback.Parse(sql)
		fallback.Forbidden = rawForbidden
		applyForbiddenEscalation(&fallback, rawForbidden)
		fallback.Class = strictestClass(fallback.Statements)
		fallback.ParseSource = ParseSourceFallback
		return fallback, nil

	case astErr != nil:
		// AST parser failed wholesale (couldn't even split). Use the
		// tokenizer as the single source. Log + count.
		c.logASTFallback(sql, astErr, "whole_input")
		fallback, _ := c.fallback.Parse(sql)
		fallback.Forbidden = mergeForbidden(fallback.Forbidden, rawForbidden)
		applyForbiddenEscalation(&fallback, fallback.Forbidden)
		fallback.Class = strictestClass(fallback.Statements)
		fallback.ParseSource = ParseSourceFallback
		return fallback, nil
	}

	// Step 4: AST succeeded for at least the high-level split. Walk
	// the per-statement results and replace any that the AST parser
	// marked as fallback (it might have failed individual statements).
	// The AST classifier sets ParseSource on each statement.
	statements := astResult.Statements

	// Determine the aggregate ParseSource: AST if all stmts AST,
	// Fallback if all fallback, Mixed otherwise.
	aggregate := aggregateParseSource(statements)

	// Merge forbidden hits: AST result PLUS raw token matcher. The
	// merge dedupes by (pattern, stmt index, token offset).
	merged := mergeForbidden(astResult.Forbidden, rawForbidden)

	out := ParsedQuery{
		Statements: statements,
		Forbidden:  merged,
	}
	applyForbiddenEscalation(&out, merged)
	out.Class = strictestClass(out.Statements)
	out.ParseSource = aggregate
	return out, nil
}

// safeASTParse wraps the underlying AST parser in panic recovery. A
// panic is treated as a parse failure (returns errASTParseFailed wrapped
// around the recovered value).
func (c *cascadeParser) safeASTParse(sql string) (pq ParsedQuery, err error) {
	defer func() {
		if r := recover(); r != nil {
			astFallbackTotal.Add(1)
			slog.Warn("classifier: AST parser panicked, falling back",
				"dialect", c.dialect,
				"sql_sha256", shortSHA(sql),
				"panic", fmt.Sprintf("%v", r),
			)
			pq = ParsedQuery{}
			err = fmt.Errorf("%w: panic: %v", errASTParseFailed, r)
		}
	}()
	return c.ast.Parse(sql)
}

// logASTFallback emits a single info-level log entry per AST fallback.
// SECURITY: never log the raw SQL (may contain credentials or other
// sensitive bytes that have not yet been redacted). Log a short SHA
// prefix so multiple events for the same input collapse in the audit
// pipeline.
func (c *cascadeParser) logASTFallback(sql string, err error, scope string) {
	astFallbackTotal.Add(1)
	slog.Info("classifier: AST parse failed, falling back to tokenizer",
		"dialect", c.dialect,
		"scope", scope,
		"sql_sha256", shortSHA(sql),
		"err", err.Error(),
	)
}

func shortSHA(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

// mergeForbidden returns the union of two ForbiddenMatch slices. Dedupe
// key is (Pattern, StatementIndex, TokenOffset). When the same pattern
// fires from both sources at the same offset, the AST hit is preferred
// (its Reason may be more specific). Stable order: by StatementIndex,
// then by TokenOffset.
func mergeForbidden(ast, tokenizer []ForbiddenMatch) []ForbiddenMatch {
	if len(ast) == 0 && len(tokenizer) == 0 {
		return nil
	}
	type key struct {
		pattern string
		stmt    int
		offset  int
	}
	seen := make(map[key]struct{}, len(ast)+len(tokenizer))
	out := make([]ForbiddenMatch, 0, len(ast)+len(tokenizer))
	for _, m := range ast {
		k := key{m.Pattern, m.StatementIndex, m.TokenOffset}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, m)
	}
	for _, m := range tokenizer {
		k := key{m.Pattern, m.StatementIndex, m.TokenOffset}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StatementIndex != out[j].StatementIndex {
			return out[i].StatementIndex < out[j].StatementIndex
		}
		return out[i].TokenOffset < out[j].TokenOffset
	})
	return out
}

// applyForbiddenEscalation walks the forbidden matches and escalates
// the corresponding ParsedStatement entries to ClassForbidden (and
// clears their Action). This is the AST + tokenizer "any one says
// forbidden = forbidden" merge.
func applyForbiddenEscalation(pq *ParsedQuery, hits []ForbiddenMatch) {
	for _, h := range hits {
		if h.StatementIndex >= 0 && h.StatementIndex < len(pq.Statements) {
			pq.Statements[h.StatementIndex].Class = ClassForbidden
			pq.Statements[h.StatementIndex].Action = ""
		}
	}
}

// aggregateParseSource collapses per-statement ParseSource values into
// the aggregate value for ParsedQuery.ParseSource. Empty statement list
// → ParseSourceAST (consistent with the optimistic default of the zero
// value).
func aggregateParseSource(stmts []ParsedStatement) ParseSource {
	if len(stmts) == 0 {
		return ParseSourceAST
	}
	sawAST, sawFallback := false, false
	for _, s := range stmts {
		switch s.ParseSource {
		case ParseSourceAST:
			sawAST = true
		case ParseSourceFallback:
			sawFallback = true
		}
	}
	switch {
	case sawAST && sawFallback:
		return ParseSourceMixed
	case sawFallback:
		return ParseSourceFallback
	default:
		return ParseSourceAST
	}
}

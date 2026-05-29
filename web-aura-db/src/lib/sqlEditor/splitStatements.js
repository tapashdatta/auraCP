// Multi-statement SQL splitter. Walks the input character-by-character
// tracking string/comment/identifier states so a `;` inside a quoted
// string or comment does NOT terminate a statement. Pure state-machine,
// no regex backtracking — safe for very large docs.
//
// NOTE: this is NOT a real SQL parser. It tolerates the common quote/
// comment shapes (', ", `, --, /* */, $tag$ ... $tag$) used by MariaDB
// and Postgres but does not attempt to validate syntax. The canonical
// classification still happens server-side via classifier.Classify.

/**
 * @typedef {object} Statement
 * @property {number} start    inclusive byte offset of statement text
 * @property {number} end      exclusive byte offset (terminator excluded)
 * @property {string} text     raw text [start, end)
 * @property {string} trimmedText  trimmed copy for "is empty?" checks
 */

/**
 * SEC-3: a statement consisting ONLY of comments + whitespace must NOT
 * be submitted as an exec frame — server replies with an empty result
 * tab, which surfaces as a confusing "phantom" tab in the UI. Strip
 * line comments (`-- … \n`) and block comments (`/* … *​/`) and check
 * whether anything substantive remains. Quoted strings cannot appear
 * in a comment-only statement (the splitter has already escaped past
 * them in the outer state machine), so this lightweight strip is safe
 * here even though it would NOT be safe as the splitter itself.
 *
 * @param {string} text
 * @returns {boolean}
 */
export function isCommentOnly(text) {
  if (!text) return true
  // Drop block comments first (non-greedy, multi-line).
  let s = text.replace(/\/\*[\s\S]*?\*\//g, '')
  // Drop line comments to end of line.
  s = s.replace(/--[^\n]*/g, '')
  return s.trim().length === 0
}

/**
 * Split a SQL document into top-level statements terminated by `;`.
 * Returns an empty array when the input is whitespace/comments only.
 *
 * SEC-3: statements whose trimmed body is comment-only are filtered
 * out so the user doesn't get a phantom empty result tab from a
 * `-- foo;` line.
 *
 * @param {string} sql
 * @returns {Statement[]}
 */
export function splitStatements(sql) {
  /** @type {Statement[]} */
  const out = []
  if (!sql) return out
  let i = 0
  let stmtStart = 0
  const n = sql.length
  while (i < n) {
    const ch = sql[i]
    // Single-quote string (MariaDB + Postgres). Backslash escapes are
    // common in MariaDB; Postgres only honors them inside E'...'. We
    // accept both forms — false negatives are fine for split purposes.
    if (ch === "'") {
      i++
      while (i < n) {
        if (sql[i] === '\\' && i + 1 < n) { i += 2; continue }
        if (sql[i] === "'") {
          if (i + 1 < n && sql[i + 1] === "'") { i += 2; continue } // SQL-standard escape
          i++; break
        }
        i++
      }
      continue
    }
    // Double-quote identifier (Postgres) / string (MariaDB ANSI_QUOTES off).
    if (ch === '"') {
      i++
      while (i < n) {
        if (sql[i] === '\\' && i + 1 < n) { i += 2; continue }
        if (sql[i] === '"') {
          if (i + 1 < n && sql[i + 1] === '"') { i += 2; continue }
          i++; break
        }
        i++
      }
      continue
    }
    // Backtick identifier (MariaDB).
    if (ch === '`') {
      i++
      while (i < n) {
        if (sql[i] === '`') {
          if (i + 1 < n && sql[i + 1] === '`') { i += 2; continue }
          i++; break
        }
        i++
      }
      continue
    }
    // Postgres dollar-tagged string: $tag$...$tag$ where tag is [A-Za-z_]\w*
    if (ch === '$') {
      const m = /^\$([A-Za-z_]\w*)?\$/.exec(sql.slice(i))
      if (m) {
        const closer = m[0]
        const close = sql.indexOf(closer, i + closer.length)
        if (close < 0) { i = n; break }
        i = close + closer.length
        continue
      }
    }
    // Line comment
    if (ch === '-' && sql[i + 1] === '-') {
      const nl = sql.indexOf('\n', i)
      i = nl < 0 ? n : nl + 1
      continue
    }
    // Block comment (no nesting — Postgres allows nesting but MariaDB doesn't;
    // we conservatively non-nest, which is harmless for split purposes).
    if (ch === '/' && sql[i + 1] === '*') {
      const close = sql.indexOf('*/', i + 2)
      i = close < 0 ? n : close + 2
      continue
    }
    if (ch === ';') {
      const text = sql.slice(stmtStart, i)
      const trimmed = text.trim()
      // SEC-3: skip comment-only statements (e.g. `-- foo;`).
      if (trimmed.length > 0 && !isCommentOnly(trimmed)) {
        out.push({ start: stmtStart, end: i, text, trimmedText: trimmed })
      }
      i++
      stmtStart = i
      continue
    }
    i++
  }
  // Trailing statement without ; terminator.
  const tail = sql.slice(stmtStart, n)
  const trimmedTail = tail.trim()
  if (trimmedTail.length > 0 && !isCommentOnly(trimmedTail)) {
    out.push({ start: stmtStart, end: n, text: tail, trimmedText: trimmedTail })
  }
  return out
}

/**
 * Return the statement containing the cursor position. When the cursor
 * sits exactly on a terminator boundary, prefer the statement that ENDS
 * at the cursor (mirrors DataGrip/IntelliJ behavior). Returns null when
 * the doc has no statements.
 *
 * @param {Statement[]} stmts
 * @param {number} pos cursor offset
 * @returns {Statement | null}
 */
export function getStatementAtCursor(stmts, pos) {
  if (!stmts.length) return null
  // Prefer ends-at-cursor (post-`;` whitespace = previous stmt).
  for (let i = stmts.length - 1; i >= 0; i--) {
    const s = stmts[i]
    if (pos >= s.start && pos <= s.end) return s
  }
  // After the last statement's end → fall through to the last.
  return stmts[stmts.length - 1]
}

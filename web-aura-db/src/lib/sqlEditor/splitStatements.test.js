import { describe, it, expect } from 'vitest'
import { splitStatements, getStatementAtCursor, isCommentOnly } from './splitStatements.js'

describe('splitStatements', () => {
  it('returns empty for empty / whitespace input', () => {
    expect(splitStatements('')).toEqual([])
    expect(splitStatements('   \n\t  ')).toEqual([])
  })

  it('splits a simple two-statement document', () => {
    const s = splitStatements('SELECT 1; SELECT 2;')
    expect(s.length).toBe(2)
    expect(s[0].trimmedText).toBe('SELECT 1')
    expect(s[1].trimmedText).toBe('SELECT 2')
  })

  it('treats trailing statement without terminator as a statement', () => {
    const s = splitStatements('SELECT 1; SELECT 2')
    expect(s.length).toBe(2)
    expect(s[1].trimmedText).toBe('SELECT 2')
  })

  it('does NOT split on ";" inside a single-quoted string', () => {
    const s = splitStatements("INSERT INTO t VALUES ('a;b'); SELECT 1")
    expect(s.length).toBe(2)
    expect(s[0].trimmedText).toBe("INSERT INTO t VALUES ('a;b')")
  })

  it('handles escaped single quotes (SQL-standard "double single")', () => {
    const sql = "SELECT 'it''s; ok'; SELECT 2"
    const s = splitStatements(sql)
    expect(s.length).toBe(2)
    expect(s[0].trimmedText).toBe("SELECT 'it''s; ok'")
  })

  it('does NOT split on ";" inside a line comment', () => {
    const s = splitStatements('SELECT 1 -- a;b\n; SELECT 2')
    expect(s.length).toBe(2)
    expect(s[0].trimmedText.startsWith('SELECT 1')).toBe(true)
  })

  it('does NOT split on ";" inside a block comment', () => {
    const s = splitStatements('SELECT 1 /* x;y */; SELECT 2')
    expect(s.length).toBe(2)
  })

  it('does NOT split on ";" inside a backtick identifier', () => {
    const s = splitStatements('SELECT `a;b` FROM t; SELECT 2')
    expect(s.length).toBe(2)
  })

  it('does NOT split inside a Postgres dollar-tagged string', () => {
    const s = splitStatements("DO $$ BEGIN PERFORM 'a;b'; END $$; SELECT 1")
    expect(s.length).toBe(2)
    expect(s[1].trimmedText).toBe('SELECT 1')
  })

  // SEC-3 (PR #13.5): a comment-only "statement" (e.g. `-- foo;` on a
  // line by itself, or a stray /* */ block) used to slip through the
  // splitter and surface as a phantom empty result tab in the UI.
  it('SEC-3: line-comment-only statements are filtered out', () => {
    expect(splitStatements('-- foo bar')).toEqual([])
    expect(splitStatements('-- foo;')).toEqual([])
    expect(splitStatements('-- foo;\n-- bar;')).toEqual([])
  })

  it('SEC-3: block-comment-only statements are filtered out', () => {
    expect(splitStatements('/* foo */;')).toEqual([])
    expect(splitStatements('/* foo */ /* bar */')).toEqual([])
  })

  it('SEC-3: comment-then-statement keeps the statement', () => {
    const s = splitStatements('-- pre-comment\nSELECT 1')
    expect(s.length).toBe(1)
    expect(s[0].trimmedText).toMatch(/SELECT 1/)
  })

  it('SEC-3: statement-then-comment keeps the statement only', () => {
    const s = splitStatements('SELECT 1; -- trailing comment')
    expect(s.length).toBe(1)
    expect(s[0].trimmedText).toBe('SELECT 1')
  })
})

describe('isCommentOnly', () => {
  it('classifies empty / whitespace-only strings as comment-only', () => {
    expect(isCommentOnly('')).toBe(true)
    expect(isCommentOnly('   \n\t  ')).toBe(true)
  })
  it('classifies comment-only strings', () => {
    expect(isCommentOnly('-- foo')).toBe(true)
    expect(isCommentOnly('/* foo */')).toBe(true)
    expect(isCommentOnly('-- a\n-- b')).toBe(true)
    expect(isCommentOnly('/* a */\n-- b')).toBe(true)
  })
  it('rejects mixed comment+code strings', () => {
    expect(isCommentOnly('-- foo\nSELECT 1')).toBe(false)
    expect(isCommentOnly('SELECT 1 /* foo */')).toBe(false)
  })
})

describe('getStatementAtCursor', () => {
  it('returns null when no statements', () => {
    expect(getStatementAtCursor([], 0)).toBeNull()
  })

  it('returns the statement covering the cursor', () => {
    const sql = 'SELECT 1; SELECT 2;'
    const s = splitStatements(sql)
    // Cursor inside "SELECT 2" (position 13 is in the middle).
    const found = getStatementAtCursor(s, 13)
    expect(found?.trimmedText).toBe('SELECT 2')
  })

  it('prefers the statement ending AT the cursor on terminator boundaries', () => {
    const sql = 'SELECT 1; SELECT 2'
    const s = splitStatements(sql)
    // Cursor exactly at offset 8 (the ';' is at 8 — first stmt ends at 8).
    const found = getStatementAtCursor(s, 8)
    expect(found?.trimmedText).toBe('SELECT 1')
  })
})

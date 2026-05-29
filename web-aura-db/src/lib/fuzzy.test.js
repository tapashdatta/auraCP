import { describe, it, expect } from 'vitest'
import { match, highlight } from './fuzzy.js'

describe('fuzzy match', () => {
  it('returns positions:[] and score 0 for empty query', () => {
    const r = match('', 'anything')
    expect(r).not.toBeNull()
    expect(r.positions).toEqual([])
    expect(r.score).toBe(0)
  })

  it('returns null when target has no subsequence', () => {
    expect(match('xyz', 'abcdef')).toBeNull()
    expect(match('abz', 'abc')).toBeNull()
  })

  it('scores a perfect-prefix match higher than a buried match', () => {
    const a = match('sel', 'select * from t')
    const b = match('sel', 'this has a sel hiding')
    expect(a).not.toBeNull()
    expect(b).not.toBeNull()
    expect(a.score).toBeGreaterThan(b.score)
  })

  it('rewards consecutive characters', () => {
    // Same word-boundary topology (no underscores), only the spacing differs.
    const tight = match('foo', 'fooxxxxxx')
    const loose = match('foo', 'fxoxxxoxxx')
    expect(tight.score).toBeGreaterThan(loose.score)
  })

  it('rewards word boundary matches (camelCase / kebab / dot)', () => {
    const camel = match('cp', 'CommandPalette')
    const buried = match('cp', 'commandcp')
    expect(camel).not.toBeNull()
    expect(buried).not.toBeNull()
    expect(camel.score).toBeGreaterThan(buried.score)
  })

  it('returns positions that point at matched indices in the target', () => {
    const r = match('sql', 'select query level')
    expect(r).not.toBeNull()
    expect(r.positions.length).toBe(3)
    // verify positions are strictly increasing
    for (let i = 1; i < r.positions.length; i++) {
      expect(r.positions[i]).toBeGreaterThan(r.positions[i - 1])
    }
  })

  it('penalizes longer targets on ties so short candidates float up', () => {
    const short = match('ab', 'ab')
    const long  = match('ab', 'ab' + 'x'.repeat(80))
    expect(short.score).toBeGreaterThan(long.score)
  })

  it('is case-insensitive at the match level', () => {
    expect(match('FOO', 'foobar')).not.toBeNull()
    expect(match('foo', 'FOOBAR')).not.toBeNull()
  })
})

describe('fuzzy highlight', () => {
  it('returns a single non-hi chunk when no positions', () => {
    const chunks = highlight('hello', [])
    expect(chunks).toEqual([{ text: 'hello', hi: false }])
  })

  it('produces alternating hi/non-hi chunks', () => {
    const chunks = highlight('select', [0, 1, 4])
    // Expect [hi:'se', non-hi:'le', hi:'c', non-hi:'t'] or similar
    const text = chunks.map((c) => c.text).join('')
    expect(text).toBe('select')
    const his = chunks.filter((c) => c.hi).map((c) => c.text).join('')
    expect(his).toBe('sec')
  })

  it('coalesces consecutive positions into one chunk', () => {
    const chunks = highlight('foobar', [0, 1, 2])
    expect(chunks[0]).toEqual({ text: 'foo', hi: true })
    expect(chunks[1]).toEqual({ text: 'bar', hi: false })
  })
})

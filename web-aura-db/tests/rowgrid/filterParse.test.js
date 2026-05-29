import { describe, it, expect } from 'vitest'
import { parseFilterInput, serializeFilter } from '../../src/lib/rowgrid/filterParse.js'

describe('parseFilterInput', () => {
  it('returns null for blank input', () => {
    expect(parseFilterInput('', 'text')).toBeNull()
    expect(parseFilterInput('   ', 'text')).toBeNull()
  })
  it('recognises IS NULL / IS NOT NULL', () => {
    const a = parseFilterInput('is null', 'text')
    expect(a?.ok).toBe(true); expect(a?.op).toBe('IS NULL')
    const b = parseFilterInput('IS NOT NULL', 'text')
    expect(b?.ok).toBe(true); expect(b?.op).toBe('IS NOT NULL')
    const c = parseFilterInput('null', 'text')
    expect(c?.op).toBe('IS NULL')
  })
  it('parses equality and inequality', () => {
    expect(parseFilterInput('= 42', 'number')?.op).toBe('=')
    expect(parseFilterInput('=foo', 'text')?.op).toBe('=')
    expect(parseFilterInput('!= 5', 'number')?.op).toBe('!=')
  })
  it('parses range operators', () => {
    expect(parseFilterInput('> 100', 'number')?.op).toBe('>')
    expect(parseFilterInput('>= 100', 'number')?.op).toBe('>=')
    expect(parseFilterInput('< 5', 'number')?.op).toBe('<')
    expect(parseFilterInput('<= 5', 'number')?.op).toBe('<=')
  })
  it('parses LIKE / ILIKE', () => {
    expect(parseFilterInput('like %foo%', 'text')?.op).toBe('LIKE')
    expect(parseFilterInput('ILIKE bar', 'text')?.op).toBe('ILIKE')
  })
  it('parses IN with multiple values', () => {
    const r = parseFilterInput('in (1,2,3)', 'number')
    expect(r?.ok).toBe(true); expect(r?.op).toBe('IN')
    expect(r?.value).toEqual(['1', '2', '3'])
  })
  it('defaults text columns to ILIKE %x% substring', () => {
    const r = parseFilterInput('alice', 'text')
    expect(r?.ok).toBe(true); expect(r?.op).toBe('ILIKE'); expect(r?.value).toBe('%alice%')
  })
  it('numeric column rejects non-numeric default input', () => {
    const r = parseFilterInput('not a number', 'number')
    expect(r?.ok).toBe(false)
  })
  it('serializes IS NULL with empty value segment', () => {
    const r = parseFilterInput('is null', 'text')
    expect(serializeFilter('foo', r)).toBe('foo:IS NULL:')
  })
  it('serializes IN with JSON-encoded array (WIRE-03)', () => {
    const r = parseFilterInput('in (a,b)', 'text')
    expect(serializeFilter('foo', r)).toBe('foo:IN:["a","b"]')
  })
  it('serializes NOT IN with JSON-encoded array (WIRE-03)', () => {
    // parseFilterInput emits IN; NOT IN isn't currently recognized by the
    // parser regex but the serializer must still handle it for completeness.
    const parsed = { ok: true, op: 'NOT IN', value: ['1', '2', '3'], raw: 'not in (1,2,3)' }
    expect(serializeFilter('id', /** @type {any} */(parsed))).toBe('id:NOT IN:["1","2","3"]')
  })
})

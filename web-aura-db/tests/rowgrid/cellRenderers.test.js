import { describe, it, expect } from 'vitest'
import { classifyKind, renderCell, formatDateUTC, formatNumber, parseEditValue, binaryByteLen } from '../../src/lib/rowgrid/cellRenderers.js'

describe('classifyKind', () => {
  // edit-15 (PR #12.5): INT-family columns are now their own 'integer'
  // kind; only DECIMAL / NUMERIC / FLOAT / DOUBLE / REAL / MONEY stay
  // under 'number'. The split lets parseEditValue reject decimal input
  // on an INT column before it hits the wire.
  it('detects integer types as "integer"', () => {
    expect(classifyKind('INT')).toBe('integer')
    expect(classifyKind('BIGINT')).toBe('integer')
    expect(classifyKind('SMALLINT')).toBe('integer')
    expect(classifyKind('MEDIUMINT')).toBe('integer')
    expect(classifyKind('SERIAL')).toBe('integer')
    expect(classifyKind('BIGSERIAL')).toBe('integer')
  })
  it('detects decimal/float types as "number"', () => {
    expect(classifyKind('NUMERIC(10,2)')).toBe('number')
    expect(classifyKind('DECIMAL(8,2)')).toBe('number')
    expect(classifyKind('DOUBLE PRECISION')).toBe('number')
    expect(classifyKind('FLOAT')).toBe('number')
    expect(classifyKind('MONEY')).toBe('number')
  })
  it('detects boolean types', () => {
    expect(classifyKind('BOOL')).toBe('boolean')
    expect(classifyKind('BOOLEAN')).toBe('boolean')
    expect(classifyKind('TINYINT(1)')).toBe('boolean')
  })
  it('detects json / jsonb', () => {
    expect(classifyKind('JSON')).toBe('json')
    expect(classifyKind('JSONB')).toBe('json')
  })
  it('detects datetime types', () => {
    expect(classifyKind('TIMESTAMP')).toBe('datetime')
    expect(classifyKind('TIMESTAMPTZ')).toBe('datetime')
    expect(classifyKind('DATE')).toBe('datetime')
  })
  it('detects text types', () => {
    expect(classifyKind('VARCHAR(255)')).toBe('text')
    expect(classifyKind('TEXT')).toBe('text')
  })
  it('falls back to unknown', () => {
    expect(classifyKind('XML')).toBe('unknown')
    expect(classifyKind('')).toBe('unknown')
  })
})

describe('renderCell — NULL vs empty-string distinction', () => {
  it('renders null with the rg-cell--null class', () => {
    const r = renderCell(null, { kind: 'text', typeName: 'TEXT' })
    expect(r.isNull).toBe(true)
    expect(r.className).toContain('rg-cell--null')
    expect(r.text).toBe('NULL')
  })
  it('renders empty string distinctly from null', () => {
    const r = renderCell('', { kind: 'text', typeName: 'TEXT' })
    expect(r.isNull).toBe(false)
    expect(r.isEmpty).toBe(true)
    expect(r.className).toContain('rg-cell--empty')
  })
  it('renders a JSON object as collapsed', () => {
    const r = renderCell('{"a":1}', { kind: 'json', typeName: 'JSON' })
    expect(r.text.startsWith('{')).toBe(true)
    expect(r.text.endsWith('}')).toBe(true)
  })
  it('formats datetime in UTC YYYY-MM-DD HH:mm:ss', () => {
    const r = renderCell('2024-03-15T08:30:42Z', { kind: 'datetime', typeName: 'TIMESTAMPTZ' })
    expect(r.text).toBe('2024-03-15 08:30:42')
  })
  it('shows binary length for base64 blobs', () => {
    const r = renderCell('aGVsbG8=', { kind: 'binary', typeName: 'BYTEA' })
    expect(r.text).toBe('<5 bytes>')
  })
  it('flips negative numbers to .rg-cell--neg', () => {
    const r = renderCell(-42, { kind: 'number', typeName: 'INT' })
    expect(r.className).toContain('rg-cell--neg')
    expect(r.text).toBe('-42')
  })
})

describe('parseEditValue', () => {
  it('rejects empty string for required column', () => {
    const r = parseEditValue('', 'text', { nullable: false })
    expect(r.ok).toBe(false)
  })
  it('coerces empty to null for nullable column', () => {
    const r = parseEditValue('', 'text', { nullable: true })
    expect(r.ok).toBe(true)
    expect(r.value).toBe(null)
  })
  it('parses number / rejects garbage', () => {
    expect(parseEditValue('42', 'number').value).toBe(42)
    expect(parseEditValue('abc', 'number').ok).toBe(false)
  })
  // edit-15 (PR #12.5): integer kind rejects decimals + scientific
  // notation before they hit the driver (MySQL silently floors, Postgres
  // rejects with 22P02 — a parse-time error is more consistent).
  it('integer kind rejects decimals', () => {
    expect(parseEditValue('42', 'integer').value).toBe(42)
    expect(parseEditValue('-7', 'integer').value).toBe(-7)
    expect(parseEditValue('42.7', 'integer').ok).toBe(false)
    expect(parseEditValue('1e5', 'integer').ok).toBe(false)
    expect(parseEditValue('1,000', 'integer').ok).toBe(false)
    expect(parseEditValue('abc', 'integer').ok).toBe(false)
  })
  it('parses booleans flexibly', () => {
    expect(parseEditValue('true', 'boolean').value).toBe(true)
    expect(parseEditValue('FALSE', 'boolean').value).toBe(false)
    expect(parseEditValue('null', 'boolean').value).toBe(null)
    expect(parseEditValue('hat', 'boolean').ok).toBe(false)
  })
  it('parses valid JSON / rejects invalid', () => {
    expect(parseEditValue('{"a":1}', 'json').value).toEqual({ a: 1 })
    expect(parseEditValue('{nope', 'json').ok).toBe(false)
  })
  it('refuses to edit binary inline', () => {
    expect(parseEditValue('xx', 'binary').ok).toBe(false)
  })
})

describe('formatters', () => {
  it('formatNumber adds grouping for large values', () => {
    expect(formatNumber(1234567)).toBe('1,234,567')
    expect(formatNumber(42)).toBe('42')
  })
  it('formatDateUTC handles Date and string inputs', () => {
    expect(formatDateUTC('2024-01-02T03:04:05Z')).toBe('2024-01-02 03:04:05')
    expect(formatDateUTC(new Date('2024-01-02T03:04:05Z'))).toBe('2024-01-02 03:04:05')
  })
  it('binaryByteLen decodes base64 padding correctly', () => {
    expect(binaryByteLen('aGVsbG8=')).toBe(5)
    expect(binaryByteLen('aGk=')).toBe(2)
    expect(binaryByteLen('')).toBe(0)
  })
})

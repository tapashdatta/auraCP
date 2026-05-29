import { describe, it, expect } from 'vitest'
import { buildPKKey, parsePKKey } from '../../src/lib/rowgrid/pkKey.js'

describe('buildPKKey / parsePKKey', () => {
  it('encodes a single-column PK as col=val', () => {
    const row = [42, 'alice']
    const key = buildPKKey(row, ['id'], ['id', 'name'])
    expect(key).toBe('id=42')
  })
  it('encodes a composite PK with comma-joined segments', () => {
    const row = ['acme', 7, 'x']
    const key = buildPKKey(row, ['tenant', 'uid'], ['tenant', 'uid', 'note'])
    expect(key).toBe('tenant=acme,uid=7')
  })
  it('URL-encodes values containing reserved characters', () => {
    const row = ['a/b', 'foo,bar', 'x=y']
    const key = buildPKKey(row, ['p'], ['p', 'q', 'r'])
    expect(key).toBe('p=a%2Fb')
  })
  it('round-trips through parsePKKey', () => {
    const row = ['ten=ant', 5]
    const key = buildPKKey(row, ['tenant', 'uid'], ['tenant', 'uid'])
    const back = parsePKKey(key)
    expect(back.tenant).toBe('ten=ant')
    expect(back.uid).toBe('5')
  })
  it('empty pkColumns yields empty string', () => {
    expect(buildPKKey([1, 2], [], ['a', 'b'])).toBe('')
  })
})

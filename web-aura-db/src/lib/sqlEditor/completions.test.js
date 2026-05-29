import { describe, it, expect, beforeEach } from 'vitest'
import { makeCompletions } from './completions.js'
import { _resetForTests } from './schemaCache.svelte.js'

function fakeCtx(sql, cursor) {
  return {
    pos: cursor,
    explicit: false,
    matchBefore(re) {
      const slice = sql.slice(0, cursor)
      const m = slice.match(re)
      if (!m) return null
      return { from: cursor - m[0].length, to: cursor, text: m[0] }
    },
    state: {},
  }
}

describe('makeCompletions', () => {
  beforeEach(() => { _resetForTests() })

  it('emits SQL keywords when typing a bare identifier', () => {
    const fn = makeCompletions({ connId: 'c1', engine: 'postgres', getSql: () => 'SEL', getCursor: () => 3 })
    const r = fn(fakeCtx('SEL', 3))
    expect(r).not.toBeNull()
    const labels = r.options.map((o) => o.label)
    expect(labels).toContain('SELECT')
  })

  it('ranks keyword boost lower than schema/column boost', () => {
    const fn = makeCompletions({ connId: 'c1', engine: 'postgres', getSql: () => 'S', getCursor: () => 1 })
    const r = fn(fakeCtx('S', 1))
    const kw = r.options.find((o) => o.type === 'keyword')
    expect(kw?.boost).toBe(0)
  })

  it('includes Postgres-only keywords when engine=postgres', () => {
    const fn = makeCompletions({ connId: 'c1', engine: 'postgres', getSql: () => 'RET', getCursor: () => 3 })
    const r = fn(fakeCtx('RET', 3))
    expect(r.options.map((o) => o.label)).toContain('RETURNING')
  })

  it('omits Postgres-only keywords when engine=mariadb', () => {
    const fn = makeCompletions({ connId: 'c1', engine: 'mariadb', getSql: () => 'RET', getCursor: () => 3 })
    const r = fn(fakeCtx('RET', 3))
    expect(r.options.map((o) => o.label)).not.toContain('RETURNING')
  })
})

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { createClassifierStore } from './classifier.svelte.js'

function flush(ms = 320) { return new Promise((r) => setTimeout(r, ms)) }

describe('createClassifierStore', () => {
  beforeEach(() => {
    document.cookie = 'auracp_csrf=tok'
  })
  afterEach(() => { vi.restoreAllMocks() })

  it('debounces rapid updates into a single API call', async () => {
    let hits = 0
    globalThis.fetch = vi.fn(async () => {
      hits++
      return new Response(JSON.stringify({ class: 'read', statements: [{ class: 'read', kind: 'SELECT', action: 'query:read', hasWhere: false, offset: 0, rawText: 'SELECT 1' }], forbidden: [] }), { status: 200, headers: { 'content-type': 'application/json' } })
    })
    const cls = createClassifierStore({ connId: 'c1' })
    cls.update('SELECT 1')
    cls.update('SELECT 2')
    cls.update('SELECT 3')
    await flush()
    expect(hits).toBe(1)
    expect(cls.state.parsed?.class).toBe('read')
  })

  it('short-circuits empty docs with a synthetic empty parse (no fetch)', async () => {
    let hits = 0
    globalThis.fetch = vi.fn(async () => {
      hits++
      return new Response(JSON.stringify({}), { status: 200, headers: { 'content-type': 'application/json' } })
    })
    const cls = createClassifierStore({ connId: 'c1' })
    cls.update('')
    await flush()
    expect(hits).toBe(0)
    expect(cls.state.parsed?.statements).toEqual([])
  })

  it('surfaces forbidden matches from the server', async () => {
    globalThis.fetch = vi.fn(async () => new Response(JSON.stringify({
      class: 'forbidden',
      statements: [{ class: 'forbidden', kind: 'SELECT', action: '', hasWhere: false, offset: 0, rawText: "SELECT LOAD_FILE('x')" }],
      forbidden: [{ pattern: 'LOAD_FILE', reason: 'function can read arbitrary files', statementIndex: 0, tokenOffset: 7 }],
    }), { status: 200, headers: { 'content-type': 'application/json' } }))
    const cls = createClassifierStore({ connId: null, engine: 'mariadb' })
    cls.update("SELECT LOAD_FILE('x')")
    await flush()
    expect(cls.state.parsed?.class).toBe('forbidden')
    expect(cls.state.parsed?.forbidden.length).toBe(1)
  })
})

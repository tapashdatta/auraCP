import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { _resetForTests, loadSchemas, loadObjects, loadTable, peekObjects, getSchemas, setEngine, getEngine } from './schemaCache.svelte.js'
import * as apiMod from '../api.js'

function stubFetch(impl) { globalThis.fetch = vi.fn(impl) }

describe('schemaCache', () => {
  beforeEach(() => {
    _resetForTests()
    document.cookie = 'auracp_csrf=t'
  })
  afterEach(() => { vi.restoreAllMocks() })

  it('memoizes listSchemas across calls (single API hit)', async () => {
    let hits = 0
    stubFetch(async () => {
      hits++
      return new Response(JSON.stringify({ schemas: ['public', 'app'] }), { status: 200, headers: { 'content-type': 'application/json' } })
    })
    const a = await loadSchemas('c1')
    const b = await loadSchemas('c1')
    expect(a).toEqual(['public', 'app'])
    expect(b).toEqual(['public', 'app'])
    expect(hits).toBe(1)
    expect(getSchemas('c1')).toEqual(['public', 'app'])
  })

  it('deduplicates parallel inflight requests', async () => {
    let hits = 0
    stubFetch(async () => {
      hits++
      await new Promise((r) => setTimeout(r, 5))
      return new Response(JSON.stringify({ schemas: ['public'] }), { status: 200, headers: { 'content-type': 'application/json' } })
    })
    const [a, b, c] = await Promise.all([loadSchemas('c2'), loadSchemas('c2'), loadSchemas('c2')])
    expect(a).toEqual(['public'])
    expect(b).toEqual(a)
    expect(c).toEqual(a)
    expect(hits).toBe(1)
  })

  it('caches per-schema object listings', async () => {
    let hits = 0
    stubFetch(async () => {
      hits++
      return new Response(JSON.stringify({ tables: [{ name: 'users' }], views: [], functions: [], procedures: [] }), { status: 200, headers: { 'content-type': 'application/json' } })
    })
    await loadObjects('c3', 'public')
    await loadObjects('c3', 'public')
    expect(hits).toBe(1)
    expect(peekObjects('c3', 'public').tables.length).toBe(1)
  })

  it('stores engine separately from listSchemas', () => {
    setEngine('cX', 'postgres')
    expect(getEngine('cX')).toBe('postgres')
  })
})

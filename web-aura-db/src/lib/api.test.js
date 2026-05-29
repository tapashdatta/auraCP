import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { request, AuraDBError, AuraDBClient } from './api.js'

const ENV = { code: 'precondition_failed', message: 'something broke', detail: { field: 'x' }, request_id: 'req_abc' }

function stubFetch(impl) {
  globalThis.fetch = vi.fn(impl)
}

describe('AuraDBClient request()', () => {
  beforeEach(() => {
    document.cookie = 'auracp_csrf=tokenval'
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('decodes 2xx JSON responses', async () => {
    stubFetch(async () => new Response(JSON.stringify({ ok: true }), { status: 200, headers: { 'content-type': 'application/json' } }))
    const r = await request('/connections')
    expect(r).toEqual({ ok: true })
  })

  it('returns null for 204', async () => {
    stubFetch(async () => new Response(null, { status: 204 }))
    const r = await request('/connections/x', { method: 'DELETE' })
    expect(r).toBeNull()
  })

  it('throws AuraDBError on non-2xx and maps envelope fields', async () => {
    stubFetch(async () => new Response(JSON.stringify(ENV), { status: 422, headers: { 'content-type': 'application/json' } }))
    let caught
    try { await request('/connections') } catch (e) { caught = e }
    expect(caught).toBeInstanceOf(AuraDBError)
    expect(caught.status).toBe(422)
    expect(caught.code).toBe('precondition_failed')
    expect(caught.message).toBe('something broke')
    expect(caught.detail).toEqual({ field: 'x' })
    expect(caught.requestId).toBe('req_abc')
  })

  it('injects X-CSRF-Token on POST and JSON-encodes object bodies', async () => {
    let captured
    stubFetch(async (_url, init) => {
      captured = init
      return new Response(JSON.stringify({}), { status: 200, headers: { 'content-type': 'application/json' } })
    })
    await request('/connections', { method: 'POST', body: { name: 'p' } })
    expect(captured.method).toBe('POST')
    expect(captured.headers['X-CSRF-Token']).toBe('tokenval')
    expect(captured.headers['Content-Type']).toBe('application/json')
    expect(captured.body).toBe(JSON.stringify({ name: 'p' }))
  })

  it('does NOT inject CSRF on GET', async () => {
    let captured
    stubFetch(async (_url, init) => {
      captured = init
      return new Response(JSON.stringify({}), { status: 200, headers: { 'content-type': 'application/json' } })
    })
    await request('/connections')
    expect(captured.headers['X-CSRF-Token']).toBeUndefined()
  })

  it('falls back to status text when error body is non-JSON', async () => {
    stubFetch(async () => new Response('plain text error', { status: 500, statusText: 'oops', headers: { 'content-type': 'text/plain' } }))
    let caught
    try { await request('/connections') } catch (e) { caught = e }
    expect(caught).toBeInstanceOf(AuraDBError)
    expect(caught.code).toBe('unknown')
    expect(caught.message).toBe('oops')
  })
})

// ------------------------------------------------------------------------
// FIX-4 — user-controlled path segments are URL-encoded before being
// templated into the URL. Without this, an id like "../../history" would
// break out of /connections/{id} and let a caller hit unrelated routes.
// ------------------------------------------------------------------------
describe('AuraDBClient FIX-4 path traversal containment', () => {
  beforeEach(() => {
    document.cookie = 'auracp_csrf=tokenval'
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('URL-encodes a getConnection() id that contains "../"', async () => {
    let capturedUrl
    globalThis.fetch = vi.fn(async (url) => {
      capturedUrl = url
      return new Response(JSON.stringify({}), { status: 200, headers: { 'content-type': 'application/json' } })
    })
    const c = new AuraDBClient()
    await c.getConnection('../../history')
    // encodeURIComponent('../../history') === '..%2F..%2Fhistory'
    expect(capturedUrl).toBe('/api/dbadmin/connections/..%2F..%2Fhistory')
  })

  it('URL-encodes schema + table segments in getTable()', async () => {
    let capturedUrl
    globalThis.fetch = vi.fn(async (url) => {
      capturedUrl = url
      return new Response(JSON.stringify({}), { status: 200, headers: { 'content-type': 'application/json' } })
    })
    const c = new AuraDBClient()
    await c.getTable('c1', 'pub/lic', 'my table')
    expect(capturedUrl).toBe('/api/dbadmin/connections/c1/schemas/pub%2Flic/tables/my%20table')
  })

  it('classifySql posts to the connection-scoped path when connId given', async () => {
    let capturedUrl, captured
    globalThis.fetch = vi.fn(async (url, init) => {
      capturedUrl = url; captured = init
      return new Response(JSON.stringify({ class: 'read', statements: [], forbidden: [] }), { status: 200, headers: { 'content-type': 'application/json' } })
    })
    const c = new AuraDBClient()
    const r = await c.classifySql('c1', 'SELECT 1')
    expect(capturedUrl).toBe('/api/dbadmin/connections/c1/classify')
    expect(JSON.parse(captured.body)).toEqual({ statement: 'SELECT 1' })
    expect(r.class).toBe('read')
  })

  it('classifySql posts to /sql/classify when connId is null', async () => {
    let capturedUrl, captured
    globalThis.fetch = vi.fn(async (url, init) => {
      capturedUrl = url; captured = init
      return new Response(JSON.stringify({ class: 'read', statements: [], forbidden: [] }), { status: 200, headers: { 'content-type': 'application/json' } })
    })
    const c = new AuraDBClient()
    await c.classifySql(null, 'SELECT 1', 'postgres')
    expect(capturedUrl).toBe('/api/dbadmin/sql/classify')
    expect(JSON.parse(captured.body)).toEqual({ statement: 'SELECT 1', engine: 'postgres' })
  })

  it('searchHistory / updateHistory / deleteHistory hit connection-scoped paths', async () => {
    /** @type {string[]} */
    const urls = []
    globalThis.fetch = vi.fn(async (url) => {
      urls.push(String(url))
      return new Response(JSON.stringify({}), { status: 200, headers: { 'content-type': 'application/json' } })
    })
    const c = new AuraDBClient()
    await c.searchHistory('c1', { q: 'foo' })
    await c.updateHistory('c1', '42', { starred: true })
    await c.deleteHistory('c1', '42')
    expect(urls[0]).toBe('/api/dbadmin/connections/c1/history/search?q=foo')
    expect(urls[1]).toBe('/api/dbadmin/connections/c1/history/42')
    expect(urls[2]).toBe('/api/dbadmin/connections/c1/history/42')
  })

  it('URL-encodes deleteSaved() sid', async () => {
    let capturedUrl
    globalThis.fetch = vi.fn(async (url) => {
      capturedUrl = url
      return new Response(null, { status: 204 })
    })
    const c = new AuraDBClient()
    await c.deleteSaved('c1', '../other')
    expect(capturedUrl).toBe('/api/dbadmin/connections/c1/saved-queries/..%2Fother')
  })
})

// ------------------------------------------------------------------------
// PR #16 — exportTable: streams a file download, parses
// Content-Disposition for the filename, and triggers a Blob anchor click.
// ------------------------------------------------------------------------
describe('AuraDBClient.exportTable (PR #16)', () => {
  beforeEach(() => {
    document.cookie = 'auracp_csrf=tokenval'
    // jsdom needs URL.createObjectURL stubbed.
    if (!('createObjectURL' in URL)) {
      URL.createObjectURL = vi.fn(() => 'blob:fake')
    } else {
      vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:fake')
    }
    if (!('revokeObjectURL' in URL)) {
      URL.revokeObjectURL = vi.fn()
    } else {
      vi.spyOn(URL, 'revokeObjectURL').mockReturnValue(undefined)
    }
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })

  function mkResp(body, headers = {}) {
    const h = new Headers({
      'content-type': 'text/csv; charset=utf-8',
      'content-disposition': 'attachment; filename="users-20300101T000000Z.csv"',
      'x-aura-export-jobid': 'job-1',
      'x-aura-export-rowcap': '1000000',
      ...headers,
    })
    return new Response(body, { status: 200, headers: h })
  }

  it('POSTs the structured body to /connections/{id}/export with CSRF', async () => {
    let captured
    globalThis.fetch = vi.fn(async (url, init) => {
      captured = { url, init }
      return mkResp('a,b\r\n1,2\r\n')
    })
    const c = new AuraDBClient()
    const r = await c.exportTable('c1', {
      schema: 'public', table: 'users', format: 'csv',
      columns: ['a', 'b'], filter: [], sort: [],
    })
    expect(captured.url).toBe('/api/dbadmin/connections/c1/export')
    expect(captured.init.method).toBe('POST')
    expect(captured.init.headers['Content-Type']).toBe('application/json')
    expect(captured.init.headers['X-CSRF-Token']).toBe('tokenval')
    const body = JSON.parse(captured.init.body)
    expect(body.schema).toBe('public')
    expect(body.table).toBe('users')
    expect(body.format).toBe('csv')
    expect(body.columns).toEqual(['a', 'b'])
    expect(r.filename).toBe('users-20300101T000000Z.csv')
    expect(r.jobId).toBe('job-1')
    expect(r.rowCap).toBe(1_000_000)
    expect(r.bytes).toBe(new Blob(['a,b\r\n1,2\r\n']).size)
  })

  it('throws AuraDBError on non-2xx export', async () => {
    globalThis.fetch = vi.fn(async () => new Response(
      JSON.stringify({ error: { code: 'forbidden', message: 'no', request_id: 'r1' } }),
      { status: 403, headers: { 'content-type': 'application/json' } },
    ))
    const c = new AuraDBClient()
    let caught
    try { await c.exportTable('c1', { schema: 's', table: 't', format: 'csv' }) }
    catch (e) { caught = e }
    expect(caught).toBeInstanceOf(AuraDBError)
    expect(caught.status).toBe(403)
    expect(caught.code).toBe('forbidden')
  })

  it('rejects invalid arguments locally without a network call', async () => {
    const fetchSpy = vi.fn()
    globalThis.fetch = fetchSpy
    const c = new AuraDBClient()
    let caught
    try { await c.exportTable('', { schema: 's', table: 't', format: 'csv' }) }
    catch (e) { caught = e }
    expect(caught).toBeInstanceOf(AuraDBError)
    expect(fetchSpy).not.toHaveBeenCalled()
  })

  it('parses RFC 5987 filename* over filename=', async () => {
    globalThis.fetch = vi.fn(async () => mkResp('x', {
      'content-disposition': `attachment; filename="ascii.csv"; filename*=UTF-8''na%C3%AFve.csv`,
    }))
    const c = new AuraDBClient()
    const r = await c.exportTable('c1', { schema: 's', table: 't', format: 'csv' })
    expect(r.filename).toBe('naïve.csv')
  })
})

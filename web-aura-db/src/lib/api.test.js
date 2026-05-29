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

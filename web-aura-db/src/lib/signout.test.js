import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { signOut } from './signout.js'

// FIX-7 — Sign Out must POST /api/auth/logout (panel route) with the
// X-CSRF-Token header populated from the auracp_csrf cookie.
describe('signOut FIX-7 hits the correct endpoint', () => {
  let origLocation

  beforeEach(() => {
    document.cookie = 'auracp_csrf=signouttok'
    // jsdom's location.href setter throws on cross-origin navigation —
    // replace it with a writable stub so we can observe the final value.
    origLocation = window.location
    delete window.location
    // @ts-ignore — minimal stub is fine here.
    window.location = { href: '' }
  })
  afterEach(() => {
    // @ts-ignore
    window.location = origLocation
    vi.restoreAllMocks()
  })

  it('issues POST /api/auth/logout with the CSRF token, then redirects to /login', async () => {
    let captured
    globalThis.fetch = vi.fn(async (url, init) => {
      captured = { url, init }
      return new Response('{}', { status: 200, headers: { 'content-type': 'application/json' } })
    })
    await signOut()
    expect(captured.url).toBe('/api/auth/logout')
    expect(captured.init.method).toBe('POST')
    expect(captured.init.credentials).toBe('same-origin')
    expect(captured.init.headers['X-CSRF-Token']).toBe('signouttok')
    expect(window.location.href).toBe('/login')
  })

  it('still navigates to /login when the logout fetch throws', async () => {
    globalThis.fetch = vi.fn(async () => { throw new Error('network down') })
    await signOut()
    expect(window.location.href).toBe('/login')
  })
})

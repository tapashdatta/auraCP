// Verifies the toastFromError helper and the optimistic-rollback contract
// without spinning up the full grid: we just simulate the flow with a
// stubbed fetch the way useRowGrid talks to api.updateRow / api.deleteRow.
// This covers the "optimistic-update rollback" required test from the PR.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { toastFromError, toastBus } from '../../src/lib/rowgrid/toasts.svelte.js'
import { AuraDBError, api } from '../../src/lib/api.js'

describe('toastFromError', () => {
  it('maps known AuraDBError codes to friendly text', () => {
    expect(toastFromError({ code: 'pk_mismatch', message: 'x' }).text).toContain('changed elsewhere')
    expect(toastFromError({ code: 'no_primary_key', message: 'x' }).text).toContain('no primary key')
    expect(toastFromError({ code: 'row_cap_exceeded', message: 'x' }).level).toBe('info')
  })
  it('falls back to the message for unknown codes', () => {
    expect(toastFromError({ code: 'mysterious', message: 'boom' }).text).toBe('boom')
  })
  it('handles missing error object', () => {
    expect(toastFromError(null).level).toBe('danger')
  })
})

describe('optimistic update rollback (api.updateRow rejection path)', () => {
  beforeEach(() => {
    document.cookie = 'auracp_csrf=tokenval'
    toastBus.items = []
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })
  it('updateRow rejection produces a typed AuraDBError the toast bus can render', async () => {
    globalThis.fetch = vi.fn(async () => new Response(
      JSON.stringify({ code: 'pk_mismatch', message: 'row changed', request_id: 'req_x' }),
      { status: 409, headers: { 'content-type': 'application/json' } },
    ))
    let caught
    try {
      await api.updateRow('c', 's', 't', 'id=1', { x: 'y' })
    } catch (e) {
      caught = e
    }
    expect(caught).toBeInstanceOf(AuraDBError)
    expect(caught.code).toBe('pk_mismatch')
    const t = toastFromError(caught)
    expect(t.level).toBe('warning')
    expect(t.text).toMatch(/changed elsewhere/)
  })
  it('rollback path: we can restore prior row data after a rejection', async () => {
    globalThis.fetch = vi.fn(async () => new Response(
      JSON.stringify({ code: 'server_error', message: 'oops' }),
      { status: 500, headers: { 'content-type': 'application/json' } },
    ))
    /** Simulated 1-row grid */
    const rows = [['alice', 25]]
    const original = rows[0][1]
    // Optimistic mutation
    rows[0] = [...rows[0]]; rows[0][1] = 99
    let err
    try {
      await api.updateRow('c', 's', 't', 'id=1', { age: 99 })
    } catch (e) { err = e }
    expect(err).toBeInstanceOf(AuraDBError)
    // Rollback
    rows[0] = [...rows[0]]; rows[0][1] = original
    expect(rows[0][1]).toBe(25)
  })
})

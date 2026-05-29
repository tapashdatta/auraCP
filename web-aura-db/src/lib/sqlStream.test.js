import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { SqlStream } from './sqlStream.js'

describe('SqlStream frame routing', () => {
  beforeEach(() => {
    // FIX-1: SqlStream now requires the CSRF cookie to be present before it
    // will attempt to dial the WebSocket. The frame-routing tests don't
    // care about networking — they feed frames through the test seam — but
    // they DO need the per-handle .ready() promise to resolve so the
    // .catch(...) cleanup doesn't strip the handler entry before the test
    // exercises it.
    document.cookie = 'auracp_csrf=tokrouting'
  })

  it('dispatches meta/row/end frames to the correct ref handlers', () => {
    const s = new SqlStream()
    const calls = { meta: 0, rows: [], end: null, err: null }
    const handle = s.exec('conn1', 'select 1')
    handle
      .onMeta((f) => { calls.meta = f.columns.length })
      .onRow((row) => { calls.rows.push(row) })
      .onEnd((f) => { calls.end = f })
      .onError((e) => { calls.err = e })

    s._onFrame({ op: 'meta', ref: handle.ref, columns: [{ name: 'a' }, { name: 'b' }] })
    s._onFrame({ op: 'row',  ref: handle.ref, values: [1, 'x'] })
    s._onFrame({ op: 'row',  ref: handle.ref, values: [2, 'y'] })
    s._onFrame({ op: 'end',  ref: handle.ref, durationMs: 12, affected: 0 })

    expect(calls.meta).toBe(2)
    expect(calls.rows).toEqual([[1, 'x'], [2, 'y']])
    expect(calls.end).toEqual({ durationMs: 12, affected: 0 })
    expect(calls.err).toBeNull()
  })

  it('routes error frames and cleans up the handler entry', () => {
    const s = new SqlStream()
    const handle = s.exec('c', 'bad sql')
    let captured = null
    handle.onError((e) => { captured = e })
    s._onFrame({ op: 'error', ref: handle.ref, code: 'parse_error', message: 'oops' })
    expect(captured).toEqual({ code: 'parse_error', message: 'oops' })
    // After end/error, follow-up frames for the same ref must NOT call handlers.
    let extra = 0
    handle.onRow(() => { extra++ })
    s._onFrame({ op: 'row', ref: handle.ref, values: [1] })
    expect(extra).toBe(0)
  })

  it('ignores frames for unknown refs', () => {
    const s = new SqlStream()
    // Should not throw.
    expect(() => s._onFrame({ op: 'meta', ref: 'r_missing', columns: [] })).not.toThrow()
  })

  it('subscribe emits initial state synchronously', () => {
    const s = new SqlStream()
    const states = []
    s.subscribe((st) => states.push(st))
    expect(states).toEqual(['idle'])
  })
})

// ------------------------------------------------------------------------
// FIX-1 — CSRF subprotocol on the WebSocket constructor.
// ------------------------------------------------------------------------
describe('SqlStream FIX-1 CSRF subprotocol', () => {
  /** @type {WebSocket} */
  let lastWS
  let WSCtor

  beforeEach(() => {
    WSCtor = vi.fn(function (url, protos) {
      this.url = url
      this.protos = protos
      this.readyState = 0
      this._listeners = {}
      this.addEventListener = (k, fn) => { (this._listeners[k] ||= []).push(fn) }
      this.removeEventListener = () => {}
      this.send = vi.fn()
      this.close = vi.fn()
      lastWS = this
    })
    // @ts-ignore
    globalThis.WebSocket = WSCtor
    // Clear cookies before each test.
    document.cookie = 'auracp_csrf=; expires=Thu, 01 Jan 1970 00:00:00 GMT; path=/'
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('passes ["aura.sql.v1", "aura.csrf.<token>"] when the CSRF cookie is set', () => {
    document.cookie = 'auracp_csrf=tok123'
    const s = new SqlStream()
    s.connect().catch(() => {})
    expect(WSCtor).toHaveBeenCalledTimes(1)
    expect(lastWS.protos).toEqual(['aura.sql.v1', 'aura.csrf.tok123'])
  })

  it('does NOT open a WebSocket and transitions to "failed" when no CSRF cookie', async () => {
    const s = new SqlStream()
    const states = []
    s.subscribe((st) => states.push(st))
    await s.connect().catch(() => {})
    expect(WSCtor).not.toHaveBeenCalled()
    expect(s.state).toBe('failed')
    expect(states).toContain('failed')
  })

  it('rejects ready() and routes onError when CSRF is missing', async () => {
    const s = new SqlStream()
    const errs = []
    const handle = s.exec('c1', 'select 1').onError((e) => errs.push(e))
    // Let the microtask flush.
    await Promise.resolve()
    await Promise.resolve()
    expect(errs.length).toBe(1)
    expect(errs[0].code).toBe('stream_unavailable')
    // Handler entry cleaned up.
    expect(s.handlers.has(handle.ref)).toBe(false)
  })
})

// ------------------------------------------------------------------------
// FIX-2 — Hard cap + visibility gate on reconnect.
// ------------------------------------------------------------------------
describe('SqlStream FIX-2 reconnect hard cap and visibility gate', () => {
  let WSCtor
  /** @type {any} */
  let lastWS

  beforeEach(() => {
    document.cookie = 'auracp_csrf=tok123'
    WSCtor = vi.fn(function () {
      this._listeners = {}
      this.addEventListener = (k, fn) => { (this._listeners[k] ||= []).push(fn) }
      this.removeEventListener = () => {}
      this.send = vi.fn()
      this.close = vi.fn()
      lastWS = this
    })
    // @ts-ignore
    globalThis.WebSocket = WSCtor
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('does NOT schedule a setTimeout reconnect while document is hidden', () => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' })
    const setTimeoutSpy = vi.spyOn(globalThis, 'setTimeout')
    const s = new SqlStream()
    // Register an in-flight handler so the reconnect path actually fires.
    s.handlers.set('r1', { onError: () => {} })
    s.state = 'closed'
    s._scheduleReconnect()
    expect(setTimeoutSpy).not.toHaveBeenCalled()
  })

  it('transitions to "failed" after MAX_ATTEMPTS reconnect attempts', () => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' })
    const s = new SqlStream()
    s.handlers.set('r1', { onError: () => {} })
    // Each _scheduleReconnect call increments attempts; on the 21st call the
    // cap trips and the state becomes 'failed'. We re-arm state='closed'
    // before each call to simulate the close→schedule loop without actually
    // running the timer (so the test stays deterministic).
    let tripped = false
    for (let i = 0; i < 25; i++) {
      if (s.state === 'failed') { tripped = true; break }
      s.state = 'closed'
      s._scheduleReconnect()
    }
    if (s.state === 'failed') tripped = true
    expect(tripped).toBe(true)
    expect(s.state).toBe('failed')
  })
})

// ------------------------------------------------------------------------
// FIX-3 — Notify in-flight handlers on terminal failure.
// ------------------------------------------------------------------------
describe('SqlStream FIX-3 notifies handlers on failure', () => {
  beforeEach(() => {
    document.cookie = 'auracp_csrf=tok123'
  })

  it('invokes onError on every active handler when the stream is failed', () => {
    const s = new SqlStream()
    const calls = []
    s.handlers.set('a', { onError: (e) => calls.push(['a', e]) })
    s.handlers.set('b', { onError: (e) => calls.push(['b', e]) })
    s._fail('boom')
    expect(calls.length).toBe(2)
    expect(calls[0][1].code).toBe('stream_unavailable')
    expect(s.handlers.size).toBe(0)
  })
})

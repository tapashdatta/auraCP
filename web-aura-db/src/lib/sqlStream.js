// Lazy WebSocket client for /api/dbadmin/sql/stream. PR #11 ships the class +
// reconnect loop + frame routing as a stub the tab bar can subscribe against;
// PR #13 actually drives it with the SQL editor. See design "wsClientShape".
//
// Frame protocol (JSON, one per WS message):
//   client → server: {op:"exec",   ref, connId, sql, params?}
//   client → server: {op:"cancel", ref}
//   server → client: {op:"meta",   ref, columns, rowCount?}
//   server → client: {op:"row",    ref, values}
//   server → client: {op:"end",    ref, durationMs, affected?}
//   server → client: {op:"error",  ref, code, message}
//   server → client: {op:"ping"}        // heartbeat ~25s
//   client → server: {op:"pong"}

const URL_PATH = '/api/dbadmin/sql/stream'
const RECONNECT_BACKOFF = [500, 1000, 2000, 5000, 10000, 30000]
// FIX-2: hard cap on reconnect attempts so a permanently-broken WS doesn't
// hammer the server forever. After MAX_ATTEMPTS we drop to "failed" and
// every in-flight handle is notified.
const MAX_ATTEMPTS = 20

/** @typedef {'idle'|'opening'|'open'|'closed'|'error'|'failed'} StreamState */
/**
 * @typedef {object} ExecHandlers
 * @property {(frame:{columns:any[], rowCount?:number})=>void} [onMeta]
 * @property {(values:any[])=>void}                            [onRow]
 * @property {(frame:{durationMs:number, affected?:number})=>void} [onEnd]
 * @property {(frame:{code:string, message:string})=>void}     [onError]
 */
/**
 * @typedef {object} ExecHandle
 * @property {string} ref
 * @property {()=>void} cancel
 * @property {(fn:NonNullable<ExecHandlers['onMeta']>)=>ExecHandle} onMeta
 * @property {(fn:NonNullable<ExecHandlers['onRow']>)=>ExecHandle}  onRow
 * @property {(fn:NonNullable<ExecHandlers['onEnd']>)=>ExecHandle}  onEnd
 * @property {(fn:NonNullable<ExecHandlers['onError']>)=>ExecHandle} onError
 */

// FIX-1: CSRF cookie reader — same regex as api.js.
function readCsrfCookie() {
  if (typeof document === 'undefined') return ''
  const m = document.cookie.match(/(?:^|;\s*)auracp_csrf=([^;]+)/)
  return m ? decodeURIComponent(m[1]) : ''
}

export class SqlStream {
  constructor() {
    /** @type {WebSocket|null} */
    this.ws = null
    /** @type {StreamState} */
    this.state = 'idle'
    this.refCounter = 0
    /** @type {Map<string, ExecHandlers>} */
    this.handlers = new Map()
    this.backoffIdx = 0
    this.attempts = 0
    /** @type {Promise<void>|null} */
    this.readyPromise = null
    /** @type {Set<(s:StreamState)=>void>} */
    this.listeners = new Set()
    /** @type {string} */
    this.failureReason = ''
    /** @type {(()=>void)|null} */
    this._visListener = null
  }

  /**
   * Opens the socket (idempotent — repeated calls return the same promise).
   * Authentication piggybacks on the panel session cookie via WebSocket
   * upgrade — the CSRF token is passed as a Sec-WebSocket-Protocol
   * subprotocol ("aura.csrf.<token>") so the server can authorise the
   * upgrade without a custom header (browsers refuse non-standard headers
   * on the WebSocket constructor).
   */
  connect() {
    if (this.state === 'opening' || this.state === 'open') {
      return /** @type {Promise<void>} */ (this.readyPromise)
    }
    if (this.state === 'failed') {
      // Once we've given up, only an explicit reset (visibility recovery
      // or a fresh page) will re-open. Don't silently retry.
      return Promise.reject(new Error(this.failureReason || 'stream failed'))
    }
    // FIX-1: refuse to connect without CSRF — the server rejects the
    // upgrade anyway, so doing it client-side avoids a guaranteed-fail
    // round trip and gives a clearer UI state ("Sign in to use SQL
    // streaming") instead of reconnect spam.
    const csrf = readCsrfCookie()
    if (!csrf) {
      this._fail('unauthenticated: CSRF cookie missing')
      return Promise.reject(new Error(this.failureReason))
    }
    this.state = 'opening'
    this._emit()
    const scheme = typeof location !== 'undefined' && location.protocol === 'https:' ? 'wss' : 'ws'
    const host = typeof location !== 'undefined' ? location.host : 'localhost'
    const protos = ['aura.sql.v1', 'aura.csrf.' + csrf]
    this.ws = new WebSocket(`${scheme}://${host}${URL_PATH}`, protos)
    this.readyPromise = new Promise((resolve, reject) => {
      const ws = /** @type {WebSocket} */ (this.ws)
      ws.addEventListener('open', () => {
        this.state = 'open'
        this.backoffIdx = 0
        this.attempts = 0
        this._emit()
        resolve()
      })
      ws.addEventListener('error', () => {
        this.state = 'error'
        this._emit()
        // FIX-3: surface the failure to every in-flight handle so callers
        // see an explicit onError rather than a promise that never resolves.
        this._notifyHandlersError({ code: 'stream_unavailable', message: 'WebSocket error' })
        reject(new Error('websocket error'))
      })
      ws.addEventListener('close', () => {
        this.state = 'closed'
        this._emit()
        this._scheduleReconnect()
      })
      ws.addEventListener('message', (e) => {
        try { this._onFrame(JSON.parse(/** @type {string} */ (e.data))) }
        catch { /* ignore malformed frame */ }
      })
    })
    // Swallow the unhandled rejection on the promise itself; per-handle
    // onError callbacks already surface the failure.
    this.readyPromise.catch(() => {})
    return this.readyPromise
  }

  /** @returns {Promise<void>} */
  ready() { return this.readyPromise || this.connect() }

  /**
   * Submit a query. Returns a handle whose chained .onMeta / .onRow / .onEnd /
   * .onError methods install per-ref callbacks.
   *
   * @param {string} connId
   * @param {string} sql
   * @param {unknown[]} [params]
   * @returns {ExecHandle}
   */
  exec(connId, sql, params) {
    const ref = `r${++this.refCounter}`
    /** @type {ExecHandlers} */
    const h = {}
    this.handlers.set(ref, h)
    this.ready().then(() => {
      this.ws?.send(JSON.stringify({ op: 'exec', ref, connId, sql, params }))
    }).catch((err) => {
      // FIX-3: propagate ready() rejection to the handle owner.
      const code = (this.state === 'failed') ? 'stream_unavailable' : 'stream_unavailable'
      h.onError?.({ code, message: err?.message || 'stream unavailable' })
      this.handlers.delete(ref)
    })
    /** @type {ExecHandle} */
    const handle = {
      ref,
      cancel: () => this.ws?.send(JSON.stringify({ op: 'cancel', ref })),
      onMeta:  (fn) => { h.onMeta  = fn; return handle },
      onRow:   (fn) => { h.onRow   = fn; return handle },
      onEnd:   (fn) => { h.onEnd   = fn; return handle },
      onError: (fn) => { h.onError = fn; return handle },
    }
    return handle
  }

  /**
   * Test seam — feeds a frame as if it arrived from the server. Public so
   * vitest can exercise routing without standing up a real WS server.
   *
   * @param {any} f
   */
  _onFrame(f) {
    if (!f || typeof f !== 'object') return
    if (f.op === 'ping') {
      this.ws?.send(JSON.stringify({ op: 'pong' }))
      return
    }
    const h = this.handlers.get(f.ref)
    if (!h) return
    if (f.op === 'meta')  h.onMeta?.({ columns: f.columns, rowCount: f.rowCount })
    else if (f.op === 'row') h.onRow?.(f.values)
    else if (f.op === 'end') { h.onEnd?.({ durationMs: f.durationMs, affected: f.affected }); this.handlers.delete(f.ref) }
    else if (f.op === 'error') { h.onError?.({ code: f.code, message: f.message }); this.handlers.delete(f.ref) }
  }

  _scheduleReconnect() {
    if (this.state !== 'closed') return
    if (this.handlers.size === 0) return
    // FIX-2: hard cap on attempts.
    if (this.attempts >= MAX_ATTEMPTS) {
      this._fail('max reconnect attempts exceeded')
      return
    }
    // FIX-2: don't churn while the tab is hidden — wait for the user to
    // come back, then try exactly once with a reset backoff.
    if (typeof document !== 'undefined' && document.visibilityState !== 'visible') {
      if (this._visListener) return
      const onVis = () => {
        if (document.visibilityState === 'visible') {
          document.removeEventListener('visibilitychange', onVis)
          this._visListener = null
          // Reset backoff because the previous failures may have been
          // from the tab being throttled/suspended, not a real outage.
          this.backoffIdx = 0
          this._reconnect()
        }
      }
      this._visListener = onVis
      document.addEventListener('visibilitychange', onVis)
      return
    }
    const idx = Math.min(this.backoffIdx++, RECONNECT_BACKOFF.length - 1)
    const delay = RECONNECT_BACKOFF[idx]
    this.attempts++
    setTimeout(() => this._reconnect(), delay)
  }

  _reconnect() {
    // Clear ws/readyPromise so connect() doesn't think we're still open.
    this.ws = null
    this.readyPromise = null
    if (this.state === 'failed') return
    this.connect().catch(() => {})
  }

  /** @param {string} reason */
  _fail(reason) {
    this.state = 'failed'
    this.failureReason = reason
    this._emit()
    this._notifyHandlersError({ code: 'stream_unavailable', message: reason })
    this.handlers.clear()
    if (this._visListener && typeof document !== 'undefined') {
      document.removeEventListener('visibilitychange', this._visListener)
      this._visListener = null
    }
  }

  /** @param {{code:string,message:string}} err */
  _notifyHandlersError(err) {
    for (const h of this.handlers.values()) {
      try { h.onError?.(err) } catch { /* swallow handler errors */ }
    }
  }

  /** Explicit close — used by tests and on logout. */
  close() {
    if (this._visListener && typeof document !== 'undefined') {
      document.removeEventListener('visibilitychange', this._visListener)
      this._visListener = null
    }
    try { this.ws?.close() } catch { /* ignore */ }
    this._fail('closed')
  }

  /** @param {(s:StreamState)=>void} fn */
  subscribe(fn) {
    this.listeners.add(fn)
    fn(this.state)
    return () => this.listeners.delete(fn)
  }

  _emit() { for (const fn of this.listeners) fn(this.state) }
}

export const sqlStream = new SqlStream()

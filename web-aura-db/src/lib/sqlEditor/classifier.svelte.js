// Debounced server-side classifier client used by the editor to drive
// the toolbar chip + the linter. Calls /sql/classify (or its
// connection-scoped variant) at most once per WAIT ms after the doc
// stops changing. The result drives:
//   - the per-statement-under-cursor class chip
//   - the multi-statement summary popover
//   - the lint diagnostics (red squiggles under forbidden offsets)
//
// IMPORTANT: this is NEVER a security boundary. The actual gate is the
// server-side re-classify in handleQuery before dispatch.

import { api } from '../api.js'

/**
 * @typedef {object} ParsedQuery
 * @property {string} class
 * @property {Array<{class:string, kind:string, action:string, hasWhere:boolean, offset:number, rawText:string}>} statements
 * @property {Array<{pattern:string, reason:string, statementIndex:number, tokenOffset:number}>} forbidden
 */

const WAIT_MS = 250

// SEC-2 / classifier-griefing: cap the number of /classify round-trips
// per rolling minute regardless of debounce. The server route is
// authenticated but otherwise open to any session and writes an audit
// row per call; a tight client-side ceiling stops a runaway editor
// loop (or a confused tab) from filling the audit log even before the
// server-side audit-drop lands. 60/min is far above human typing
// cadence (debounce already collapses bursts to ~4/sec at peak typing,
// and on settle drops to 1/edit-burst); 60 leaves comfortable room for
// rapid load-into-editor sessions while choking obvious flooding.
const RATE_WINDOW_MS = 60_000
const RATE_LIMIT = 60

/**
 * Create a debounced classifier. The returned object exposes:
 *   - `state`: a $state object with .loading, .parsed, .error fields
 *   - `update(sql)`: schedule a classify call
 *   - `flush()`: force-run immediately, returning the promise
 *   - `cancel()`: drop the pending timer
 *
 * Callers pass `{ connId, engine }` — when connId is set the server
 * resolves the engine from the connection record; the engine arg is
 * only used for the connection-less path.
 *
 * @param {{connId: string|null, engine?: 'mariadb'|'postgres'}} opts
 */
export function createClassifierStore(opts) {
  const state = $state({
    /** @type {boolean} */
    loading: false,
    /** @type {ParsedQuery|null} */
    parsed: null,
    /** @type {Error|null} */
    error: null,
    /** @type {string} */
    lastSql: '',
  })

  /** @type {any} */
  let timer = null
  /** @type {string} */
  let pendingSql = ''
  /** @type {number} */
  let seq = 0
  /** @type {number[]} */
  const rateHits = []

  // SEC-2: returns true when this call slot is allowed under the
  // rolling-minute ceiling. Expired entries are dropped lazily.
  function allowRateSlot() {
    const now = Date.now()
    while (rateHits.length && rateHits[0] < now - RATE_WINDOW_MS) {
      rateHits.shift()
    }
    if (rateHits.length >= RATE_LIMIT) return false
    rateHits.push(now)
    return true
  }

  async function run(sql) {
    // SEC-2 / classifier-griefing: skip the round-trip if we just
    // classified the exact same string. Pure UX-cache — the canonical
    // server-side re-classify still runs at exec time inside
    // handleQuery, so this can never widen the gate.
    if (sql === state.lastSql && state.parsed && !state.error) {
      return
    }
    if (!allowRateSlot()) {
      // Silent drop — the chip just stays on the previous class until
      // the limiter window opens. Surfacing a toast here would itself
      // become a griefing channel (a noisy editor would burst toasts).
      return
    }
    const mySeq = ++seq
    state.loading = true
    state.error = null
    try {
      const r = await api.classifySql(opts.connId, sql, opts.engine)
      // Drop late returns.
      if (mySeq !== seq) return
      state.parsed = /** @type {ParsedQuery} */ (r)
      state.lastSql = sql
    } catch (err) {
      if (mySeq !== seq) return
      state.error = /** @type {Error} */ (err)
    } finally {
      if (mySeq === seq) state.loading = false
    }
  }

  function schedule() {
    if (timer != null) clearTimeout(timer)
    timer = setTimeout(() => {
      timer = null
      const s = pendingSql
      // Empty doc → synthesize an empty parse without a round-trip.
      if (s.trim() === '') {
        state.parsed = { class: 'read', statements: [], forbidden: [] }
        state.lastSql = ''
        state.loading = false
        return
      }
      run(s)
    }, WAIT_MS)
  }

  return {
    state,
    /** @param {string} sql */
    update(sql) {
      pendingSql = sql
      schedule()
    },
    flush() {
      if (timer != null) { clearTimeout(timer); timer = null }
      // INT-8: flush is called by execCurrent before resolving klass so
      // the tab label matches the classifier truth. Run synchronously
      // (returns the promise so the caller can await).
      if (!pendingSql || pendingSql.trim() === '') {
        state.parsed = { class: 'read', statements: [], forbidden: [] }
        state.lastSql = pendingSql || ''
        state.loading = false
        return Promise.resolve()
      }
      return run(pendingSql)
    },
    cancel() {
      if (timer != null) { clearTimeout(timer); timer = null }
    },
    // EXEC-6: drop pending timer + clear parse cache. Called on
    // connection switch so the toolbar chip doesn't lag behind.
    reset() {
      if (timer != null) { clearTimeout(timer); timer = null }
      pendingSql = ''
      seq++
      state.loading = false
      state.parsed = null
      state.error = null
      state.lastSql = ''
      // SEC-2: rolling window is per-store; reset clears it so a
      // connection switch starts with a fresh budget.
      rateHits.length = 0
    },
  }
}

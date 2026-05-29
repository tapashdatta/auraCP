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

  async function run(sql) {
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
    },
  }
}

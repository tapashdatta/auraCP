// Aura DB API client. One method per backend route. Mirrors panel's apiFetch
// CSRF model (reads auracp_csrf cookie; injects X-CSRF-Token on mutations).
// 401 forces a hard redirect to the panel /login — Aura DB has no login form.

const BASE = '/api/dbadmin'

function csrfToken() {
  const m = document.cookie.match(/(?:^|;\s*)auracp_csrf=([^;]+)/)
  return m ? decodeURIComponent(m[1]) : ''
}

/**
 * AuraDBError carries the response envelope's code/message/detail/request_id
 * fields so UI components can branch on .code and surface request_id to ops.
 */
export class AuraDBError extends Error {
  /**
   * @param {number} status
   * @param {string} code
   * @param {string} message
   * @param {unknown} [detail]
   * @param {string} [requestId]
   */
  constructor(status, code, message, detail, requestId) {
    super(message)
    this.name = 'AuraDBError'
    this.status = status
    this.code = code
    this.detail = detail
    this.requestId = requestId
  }
}

/**
 * Low-level request. Exposed so tests can stub `globalThis.fetch`.
 *
 * @param {string} path
 * @param {{method?:string, headers?:Record<string,string>, body?:unknown, signal?:AbortSignal}} [opts]
 * @returns {Promise<any>}
 */
export async function request(path, opts = {}) {
  const method = (opts.method || 'GET').toUpperCase()
  /** @type {Record<string,string>} */
  const headers = { Accept: 'application/json', ...(opts.headers || {}) }
  let body = opts.body
  if (body && typeof body === 'object' && !(body instanceof FormData) && !(body instanceof Blob)) {
    headers['Content-Type'] = 'application/json'
    body = JSON.stringify(body)
  }
  if (method !== 'GET' && method !== 'HEAD') {
    headers['X-CSRF-Token'] = csrfToken()
  }
  const res = await fetch(BASE + path, {
    method,
    headers,
    body: /** @type {BodyInit | null | undefined} */ (body),
    credentials: 'same-origin',
    // WIRE-07: thread the caller's AbortSignal through to fetch so
    // useRowGrid can cancel an in-flight reload when a new one fires.
    signal: opts.signal,
  })
  if (res.status === 401) {
    // Hard redirect to the panel login. Strip 'request' loop by using the
    // SPA's hash route as the return target.
    if (typeof location !== 'undefined') {
      const ret = '/dbadmin/' + (location.hash || '')
      location.href = '/login?next=' + encodeURIComponent(ret)
    }
    throw new AuraDBError(401, 'unauthenticated', 'session expired')
  }
  if (!res.ok) {
    let env = /** @type {any} */ ({})
    try { env = await res.json() } catch { /* non-JSON error body */ }
    // WIRE-01: the canonical envelope is { error: { code, message, request_id, details } }
    // but legacy / tests may emit a flat { code, message, request_id, detail } body.
    // Unwrap both shapes so the UI sees consistent fields.
    const e = (env && typeof env === 'object' && env.error && typeof env.error === 'object') ? env.error : env
    throw new AuraDBError(
      res.status,
      e.code || env.code || 'unknown',
      e.message || env.message || res.statusText || 'request failed',
      e.details !== undefined ? e.details : (e.detail !== undefined ? e.detail : env.detail),
      e.request_id || env.request_id,
    )
  }
  if (res.status === 204) return null
  const ct = res.headers.get('content-type') || ''
  if (!ct.includes('application/json')) return null
  return res.json()
}

// FIX-4: every user-controlled path segment goes through encodeURIComponent
// before being interpolated into a URL. Without this, an id like
// "../../history" would let a caller traverse out of /connections/{id} and
// hit unrelated routes. encodeURIComponent escapes '/', '?', '#', '%' and
// every other reserved/unsafe character.
const enc = encodeURIComponent

/**
 * AuraDBClient — typed surface for every documented route. Methods return
 * Promise<T>; on failure they reject with AuraDBError.
 */
export class AuraDBClient {
  // Connections
  listConnections()              { return request('/connections') }
  /** @param {string} id */
  getConnection(id)              { return request(`/connections/${enc(id)}`) }
  /** @param {object} body */
  createConnection(body)         { return request('/connections', { method: 'POST', body }) }
  /** @param {string} id @param {object} body */
  updateConnection(id, body)     { return request(`/connections/${enc(id)}`, { method: 'PUT', body }) }
  /** @param {string} id */
  deleteConnection(id)           { return request(`/connections/${enc(id)}`, { method: 'DELETE' }) }
  /** @param {string} id */
  testConnection(id)             { return request(`/connections/${enc(id)}/test`, { method: 'POST' }) }
  /** @param {string} id @param {string} challenge */
  revealPassword(id, challenge)  { return request(`/connections/${enc(id)}/password/reveal`, { method: 'POST', body: { challenge } }) }

  // Schemas / objects
  /** @param {string} id */
  listSchemas(id)                { return request(`/connections/${enc(id)}/schemas`) }
  /** @param {string} id @param {string} schema */
  listObjects(id, schema)        { return request(`/connections/${enc(id)}/schemas/${enc(schema)}/objects`) }
  /** @param {string} id @param {string} schema @param {string} table */
  getTable(id, schema, table)    { return request(`/connections/${enc(id)}/schemas/${enc(schema)}/tables/${enc(table)}`) }

  // Rows (used by PR #12). The insert/update bodies wrap into
  // {values}/{set} envelopes matching httpapi.insertRowRequest /
  // updateRowRequest.
  /** @param {string} id @param {string} s @param {string} t @param {Record<string,string>} q @param {AbortSignal} [signal] */
  listRows(id, s, t, q, signal)  { return request(`/connections/${enc(id)}/schemas/${enc(s)}/tables/${enc(t)}/rows?${new URLSearchParams(q).toString()}`, { signal }) }
  /** @param {string} id @param {string} s @param {string} t @param {object} row @param {AbortSignal} [signal] */
  insertRow(id, s, t, row, signal) { return request(`/connections/${enc(id)}/schemas/${enc(s)}/tables/${enc(t)}/rows`, { method: 'POST', body: { values: row }, signal }) }
  /**
   * Patch a row. edit-1: the optional `where` snapshot lets the backend
   * verify the row has not been modified out-of-band before applying the
   * `set` patch. Backend returns 409/conflict if the snapshot mismatches.
   * @param {string} id @param {string} s @param {string} t @param {string} pk @param {object} patch
   * @param {{where?:object, signal?:AbortSignal}} [opts]
   */
  updateRow(id, s, t, pk, patch, opts = {}) {
    /** @type {any} */
    const body = { set: patch }
    if (opts.where && typeof opts.where === 'object') body.where = opts.where
    return request(`/connections/${enc(id)}/schemas/${enc(s)}/tables/${enc(t)}/rows/${enc(pk)}`, { method: 'PATCH', body, signal: opts.signal })
  }
  /** @param {string} id @param {string} s @param {string} t @param {string} pk @param {AbortSignal} [signal] */
  deleteRow(id, s, t, pk, signal) { return request(`/connections/${enc(id)}/schemas/${enc(s)}/tables/${enc(t)}/rows/${enc(pk)}`, { method: 'DELETE', signal }) }

  // Queries (PR #13)
  /** @param {string} id @param {string} sql @param {unknown[]} [params] */
  runQuery(id, sql, params)      { return request(`/connections/${enc(id)}/query`, { method: 'POST', body: { statement: sql, parameters: params } }) }
  /**
   * Issue an EXPLAIN. Returns `{ plan: Plan }`. Pass `{ analyze: true }`
   * for EXPLAIN ANALYZE; the server re-classifies and rejects analyze on
   * non-read statements with 422 forbidden_statement.
   * @param {string} id @param {string} sql @param {{analyze?:boolean}} [opts]
   */
  explain(id, sql, opts = {}) {
    /** @type {{statement:string, analyze?:boolean}} */
    const body = { statement: sql }
    if (opts && opts.analyze) body.analyze = true
    return request(`/connections/${enc(id)}/explain`, { method: 'POST', body })
  }
  /**
   * Server-side classifier preview (UX only — NEVER a security boundary,
   * the actual security re-classify happens inside handleQuery before
   * dispatch). Pass connId when you have one — the connection's engine is
   * used; else pass an explicit engine for the connection-less form.
   * @param {string|null} connId @param {string} sql @param {'mariadb'|'postgres'} [engine]
   */
  classifySql(connId, sql, engine) {
    if (connId) {
      return request(`/connections/${enc(connId)}/classify`, { method: 'POST', body: { statement: sql } })
    }
    return request('/sql/classify', { method: 'POST', body: { statement: sql, engine } })
  }

  // History / saved
  /** @param {string} id */
  connHistory(id)                { return request(`/connections/${enc(id)}/history`) }
  /**
   * WIRE-13: history search is per-connection on the server, so the
   * client method takes a connection id and passes it in the URL path.
   * @param {string} id @param {Record<string,string>} q
   */
  searchHistory(id, q)           { return request(`/connections/${enc(id)}/history/search?${new URLSearchParams(q).toString()}`) }
  /**
   * WIRE-13: patch is connection-scoped on the server. Callers MUST
   * pass connId.
   * @param {string} id @param {string} eid @param {object} patch
   */
  updateHistory(id, eid, patch)  { return request(`/connections/${enc(id)}/history/${enc(eid)}`, { method: 'PATCH', body: patch }) }
  /**
   * WIRE-13: delete is connection-scoped on the server. Callers MUST
   * pass connId.
   * @param {string} id @param {string} eid
   */
  deleteHistory(id, eid)         { return request(`/connections/${enc(id)}/history/${enc(eid)}`, { method: 'DELETE' }) }
  /** @param {string} id */
  listSaved(id)                  { return request(`/connections/${enc(id)}/saved-queries`) }
  /** @param {string} id @param {object} body */
  saveQuery(id, body)            { return request(`/connections/${enc(id)}/saved-queries`, { method: 'POST', body }) }
  /** @param {string} id @param {string} sid */
  deleteSaved(id, sid)           { return request(`/connections/${enc(id)}/saved-queries/${enc(sid)}`, { method: 'DELETE' }) }

  // Step-up
  stepUpInitiate()               { return request('/step-up/initiate', { method: 'POST' }) }
  /** @param {object} body */
  stepUpVerify(body)             { return request('/step-up/verify', { method: 'POST', body }) }

  // Audit
  /** @param {string} id */
  audit(id)                      { return request(`/connections/${enc(id)}/audit`) }
}

export const api = new AuraDBClient()

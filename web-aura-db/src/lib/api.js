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
 * @param {{method?:string, headers?:Record<string,string>, body?:unknown}} [opts]
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
    let env = /** @type {{code?:string, message?:string, detail?:unknown, request_id?:string}} */ ({})
    try { env = await res.json() } catch { /* non-JSON error body */ }
    throw new AuraDBError(
      res.status,
      env.code || 'unknown',
      env.message || res.statusText || 'request failed',
      env.detail,
      env.request_id,
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

  // Rows (used by PR #12)
  /** @param {string} id @param {string} s @param {string} t @param {Record<string,string>} q */
  listRows(id, s, t, q)          { return request(`/connections/${enc(id)}/schemas/${enc(s)}/tables/${enc(t)}/rows?${new URLSearchParams(q).toString()}`) }
  /** @param {string} id @param {string} s @param {string} t @param {object} row */
  insertRow(id, s, t, row)       { return request(`/connections/${enc(id)}/schemas/${enc(s)}/tables/${enc(t)}/rows`, { method: 'POST', body: row }) }
  /** @param {string} id @param {string} s @param {string} t @param {string} pk @param {object} patch */
  updateRow(id, s, t, pk, patch) { return request(`/connections/${enc(id)}/schemas/${enc(s)}/tables/${enc(t)}/rows/${enc(pk)}`, { method: 'PATCH', body: patch }) }
  /** @param {string} id @param {string} s @param {string} t @param {string} pk */
  deleteRow(id, s, t, pk)        { return request(`/connections/${enc(id)}/schemas/${enc(s)}/tables/${enc(t)}/rows/${enc(pk)}`, { method: 'DELETE' }) }

  // Queries (PR #13)
  /** @param {string} id @param {string} sql @param {unknown[]} [params] */
  runQuery(id, sql, params)      { return request(`/connections/${enc(id)}/query`, { method: 'POST', body: { sql, params } }) }
  /** @param {string} id @param {string} sql */
  explain(id, sql)               { return request(`/connections/${enc(id)}/explain`, { method: 'POST', body: { sql } }) }

  // History / saved
  /** @param {string} id */
  connHistory(id)                { return request(`/connections/${enc(id)}/history`) }
  /** @param {Record<string,string>} q */
  searchHistory(q)               { return request(`/history/search?${new URLSearchParams(q).toString()}`) }
  /** @param {string} eid @param {object} patch */
  updateHistory(eid, patch)      { return request(`/history/${enc(eid)}`, { method: 'PATCH', body: patch }) }
  /** @param {string} eid */
  deleteHistory(eid)             { return request(`/history/${enc(eid)}`, { method: 'DELETE' }) }
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

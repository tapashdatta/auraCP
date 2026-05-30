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
  //
  // WIRE-14 (PR #12.5): the previous AuraDBClient.listRows wrapper was
  // dead code — it accepted a Record<string,string> query bag, which
  // cannot represent multi-sort or multi-filter (URLSearchParams(record)
  // dedupes repeat keys). The real call path is
  // useRowGrid.svelte.js → listRowsRaw(), which threads a hand-built
  // query string with repeated `sort` / `filter` params straight to
  // request(). Keeping the dead wrapper invited future callers to use
  // it and silently drop their filters / sort keys — removed.
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
   *
   * FIX INT-7 (PR #14.5): accepts an optional `signal` so the inspector
   * can cancel an in-flight EXPLAIN when the user hits the abort button
   * or navigates away mid-flight. Without this, a 60s EXPLAIN locked
   * the loading branch until the server response arrived.
   *
   * @param {string} id @param {string} sql @param {{analyze?:boolean, signal?:AbortSignal}} [opts]
   */
  explain(id, sql, opts = {}) {
    /** @type {{statement:string, analyze?:boolean}} */
    const body = { statement: sql }
    if (opts && opts.analyze) body.analyze = true
    return request(`/connections/${enc(id)}/explain`, { method: 'POST', body, signal: opts?.signal })
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

  /**
   * Stream a table export and trigger a file download via a Blob URL.
   * Sends a POST with the structured export request — the backend
   * builds the SELECT server-side from validated identifiers, never
   * accepts raw SQL. The response body is the file contents
   * (Content-Disposition: attachment); we read it as a ReadableStream
   * so progress can be observed without buffering the full file in
   * memory before the user sees activity.
   *
   * @param {string} connId
   * @param {{
   *   schema: string,
   *   table: string,
   *   format: 'csv'|'ndjson'|'sql',
   *   columns?: string[],
   *   filter?: Array<{column:string, op:string, value?:any}>,
   *   sort?: Array<{column:string, descending?:boolean}>,
   *   limit?: number,
   *   includeHeader?: boolean,
   *   filename?: string,
   *   signal?: AbortSignal,
   *   onProgress?: (bytes:number)=>void,
   * }} opts
   * @returns {Promise<{filename:string, bytes:number, rowCap:number|null, jobId:string|null, truncated:boolean, serverError:string}>}
   */
  async exportTable(connId, opts) {
    if (!connId || !opts || !opts.schema || !opts.table || !opts.format) {
      throw new AuraDBError(0, 'invalid-input', 'connId, schema, table, format required')
    }
    /** @type {Record<string,any>} */
    const body = {
      schema: opts.schema,
      table: opts.table,
      format: opts.format,
    }
    if (opts.columns) body.columns = opts.columns
    if (opts.filter) body.filter = opts.filter
    if (opts.sort) body.sort = opts.sort
    if (typeof opts.limit === 'number' && opts.limit > 0) body.limit = opts.limit
    if (typeof opts.includeHeader === 'boolean') body.includeHeader = opts.includeHeader
    if (opts.filename) body.filename = opts.filename

    const res = await fetch(`${BASE}/connections/${enc(connId)}/export`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-CSRF-Token': csrfToken(),
      },
      body: JSON.stringify(body),
      credentials: 'same-origin',
      signal: opts.signal,
    })
    if (res.status === 401) {
      if (typeof location !== 'undefined') {
        const ret = '/dbadmin/' + (location.hash || '')
        location.href = '/login?next=' + encodeURIComponent(ret)
      }
      throw new AuraDBError(401, 'unauthenticated', 'session expired')
    }
    if (!res.ok) {
      let env = /** @type {any} */ ({})
      try { env = await res.json() } catch { /* non-JSON */ }
      const e = (env && typeof env === 'object' && env.error && typeof env.error === 'object') ? env.error : env
      throw new AuraDBError(
        res.status,
        e.code || env.code || 'unknown',
        e.message || env.message || res.statusText || 'export failed',
        e.details !== undefined ? e.details : (e.detail !== undefined ? e.detail : env.detail),
        e.request_id || env.request_id,
      )
    }

    const filename = parseContentDispositionFilename(res.headers.get('content-disposition') || '')
      || `${opts.table}.${opts.format}`
    const rowCapHdr = res.headers.get('x-aura-export-rowcap')
    const rowCap = rowCapHdr ? Number(rowCapHdr) : null
    const jobId = res.headers.get('x-aura-export-jobid')

    // Read the stream so the UI can show byte progress; chunk into a Blob.
    /** @type {Uint8Array[]} */
    const chunks = []
    let bytes = 0
    if (res.body && typeof res.body.getReader === 'function') {
      const reader = res.body.getReader()
      // eslint-disable-next-line no-constant-condition
      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        if (value) {
          chunks.push(value)
          bytes += value.byteLength
          if (opts.onProgress) opts.onProgress(bytes)
        }
      }
    } else {
      const ab = await res.arrayBuffer()
      chunks.push(new Uint8Array(ab))
      bytes = ab.byteLength
      if (opts.onProgress) opts.onProgress(bytes)
    }

    const ct = res.headers.get('content-type') || 'application/octet-stream'
    triggerBlobDownload(chunks, ct, filename)

    // ux-3 + C2/C3 (PR #16): the server surfaces truncation + mid-stream
    // errors via response trailers (X-Truncated, X-Export-Error). Modern
    // browsers expose trailers via Response.headers AFTER the body is
    // fully consumed — so reading them here is well-defined. The values
    // are surfaced to callers so the UI can render a "file may be
    // incomplete" warning even when the HTTP status was 200.
    const truncated = (res.headers.get('x-truncated') || '').toLowerCase() === 'true'
    const serverError = res.headers.get('x-export-error') || ''
    return { filename, bytes, rowCap, jobId, truncated, serverError }
  }
}

/**
 * Parse the filename* (RFC 5987) or filename parameter out of a
 * Content-Disposition header value. Returns '' when neither is set.
 * @param {string} header
 * @returns {string}
 */
function parseContentDispositionFilename(header) {
  // filename*= takes priority (handles non-ASCII via UTF-8 encoding).
  const utf = header.match(/filename\*\s*=\s*UTF-8''([^;]+)/i)
  if (utf) {
    try { return decodeURIComponent(utf[1]) } catch { /* fall through */ }
  }
  const plain = header.match(/filename\s*=\s*"?([^";]+)"?/i)
  if (plain) return plain[1]
  return ''
}

/**
 * Trigger a browser download for the assembled chunks. Uses a Blob URL
 * + anchor click; revokes the URL on the next tick.
 * @param {Uint8Array[]} chunks
 * @param {string} contentType
 * @param {string} filename
 */
function triggerBlobDownload(chunks, contentType, filename) {
  if (typeof document === 'undefined') return
  const blob = new Blob(chunks, { type: contentType })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  setTimeout(() => { try { URL.revokeObjectURL(url) } catch { /* ignore */ } }, 0)
}

export const api = new AuraDBClient()

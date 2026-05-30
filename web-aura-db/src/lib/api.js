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
   * Open a WebSocket against /connections/{id}/slow-log/stream and
   * stream slow-query rows. Mirrors sqlStream.exec's handle pattern —
   * returns an object whose chained .onMeta / .onRow / .onProgress /
   * .onDone / .onError methods install per-stream callbacks.
   *
   * The CSRF token piggybacks on the WS upgrade via the
   * `aura.csrf.<token>` subprotocol; browsers can't set custom
   * headers on the WebSocket constructor. The first message sent is
   * an "open" frame carrying the slow-log knobs; the server replies
   * with one meta frame (carrying mode + hint), zero-or-more row
   * frames, a final done frame, then a close. The client may send a
   * "cancel" frame at any time to terminate the stream early.
   *
   * Backend frame protocol (mirrors aura.slowlog.v1):
   *   client → server  {type:"open",   connectionId, csrf, slowLog:{sinceMs,minDurationMs,maxRows,follow}, limits?}
   *   client → server  {type:"cancel", csrf?}
   *   server → client  {type:"meta",   mode:"table"|"snapshot", hint?, pollIntervalMs?, effectiveLimits}
   *   server → client  {type:"row",    timestampMs, userHost, database?, queryTimeMs, lockTimeMs?, meanTimeMs, calls, rowsExamined?, rowsSent?, sqlExcerpt}
   *   server → client  {type:"progress", rowsEmitted, elapsedMs}
   *   server → client  {type:"done",   totalRows, durationMs, truncated}
   *   server → client  {type:"error",  code, message, request_id}
   *
   * @param {string} connId
   * @param {{
   *   sinceMs?: number,
   *   minDurationMs?: number,
   *   maxRows?: number,
   *   follow?: boolean,
   *   timeoutMs?: number,
   * }} [opts]
   */
  slowLogStream(connId, opts = {}) {
    if (!connId) throw new AuraDBError(0, 'invalid-input', 'connId required')
    const csrf = csrfToken()
    if (!csrf) throw new AuraDBError(0, 'unauthenticated', 'CSRF cookie missing')

    const scheme = (typeof location !== 'undefined' && location.protocol === 'https:') ? 'wss' : 'ws'
    const host = typeof location !== 'undefined' ? location.host : 'localhost'
    const protos = ['aura.slowlog.v1', 'aura.csrf.' + csrf]
    const url = `${scheme}://${host}${BASE}/connections/${enc(connId)}/slow-log/stream`
    const ws = new WebSocket(url, protos)

    /** @type {{onMeta?:Function, onRow?:Function, onProgress?:Function, onDone?:Function, onError?:Function}} */
    const h = {}

    const slowLog = {}
    if (typeof opts.sinceMs === 'number')       slowLog.sinceMs       = opts.sinceMs
    if (typeof opts.minDurationMs === 'number') slowLog.minDurationMs = opts.minDurationMs
    if (typeof opts.maxRows === 'number')       slowLog.maxRows       = opts.maxRows
    if (opts.follow === true)                   slowLog.follow        = true

    const openFrame = {
      type: 'open',
      connectionId: connId,
      csrf,
      slowLog,
    }
    if (typeof opts.timeoutMs === 'number' && opts.timeoutMs > 0) {
      openFrame.limits = { maxRows: 0, maxBytes: 0, timeoutMs: opts.timeoutMs }
    }

    ws.addEventListener('open', () => {
      try { ws.send(JSON.stringify(openFrame)) }
      catch (e) {
        const msg = (e && /** @type {any} */ (e).message) || 'send failed'
        h.onError?.({ code: 'stream_unavailable', message: msg })
      }
    })
    ws.addEventListener('error', () => {
      h.onError?.({ code: 'stream_unavailable', message: 'WebSocket error' })
    })
    ws.addEventListener('message', (e) => {
      let f
      try { f = JSON.parse(/** @type {string} */ (e.data)) }
      catch { return }
      if (!f || typeof f !== 'object') return
      switch (f.type) {
        case 'meta':     h.onMeta?.(f); break
        case 'row':      h.onRow?.(f); break
        case 'progress': h.onProgress?.(f); break
        case 'done':     h.onDone?.(f); break
        case 'error':    h.onError?.({ code: f.code, message: f.message, requestId: f.request_id }); break
      }
    })

    const handle = {
      ws,
      cancel: () => {
        try { ws.send(JSON.stringify({ type: 'cancel', csrf })) } catch { /* ignore */ }
        try { ws.close() } catch { /* ignore */ }
      },
      /** @param {(meta:{mode:string,hint?:string,pollIntervalMs?:number,effectiveLimits:any})=>void} fn */
      onMeta:     (fn) => { h.onMeta = fn; return handle },
      /** @param {(row:any)=>void} fn */
      onRow:      (fn) => { h.onRow = fn; return handle },
      /** @param {(p:{rowsEmitted:number,elapsedMs:number})=>void} fn */
      onProgress: (fn) => { h.onProgress = fn; return handle },
      /** @param {(d:{totalRows:number,durationMs:number,truncated:boolean})=>void} fn */
      onDone:     (fn) => { h.onDone = fn; return handle },
      /** @param {(e:{code:string,message:string,requestId?:string})=>void} fn */
      onError:    (fn) => { h.onError = fn; return handle },
    }
    return handle
  }

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
   *   onProgress?: (bytes:number, info?:{rowsWritten?:number, elapsedMs:number})=>void,
   * }} opts
   * @returns {Promise<{filename:string, bytes:number, rows:number|null, rowCap:number|null, jobId:string|null, truncated:boolean, serverError:string}>}
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

    const startedAt = (typeof performance !== 'undefined' ? performance.now() : Date.now())
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
      // ux-5 (PR #16.5): surface Retry-After (seconds, RFC 7231) on
      // 409 export-in-progress so the modal can render a countdown
      // instead of the raw error text. We pass it via AuraDBError.detail
      // so callers don't have to reach into res.headers.
      let detail = e.details !== undefined ? e.details : (e.detail !== undefined ? e.detail : env.detail)
      if (res.status === 409) {
        const ra = parseRetryAfterSeconds(res.headers.get('retry-after'))
        if (ra != null) {
          detail = (detail && typeof detail === 'object') ? { ...detail, retryAfter: ra } : { retryAfter: ra }
        }
      }
      throw new AuraDBError(
        res.status,
        e.code || env.code || 'unknown',
        e.message || env.message || res.statusText || 'export failed',
        detail,
        e.request_id || env.request_id,
      )
    }

    const filename = parseContentDispositionFilename(res.headers.get('content-disposition') || '')
      || `${opts.table}.${opts.format}`
    const rowCapHdr = res.headers.get('x-aura-export-rowcap')
    const rowCap = rowCapHdr ? Number(rowCapHdr) : null
    const jobId = res.headers.get('x-aura-export-jobid')

    // Read the stream so the UI can show byte progress; chunk into a Blob.
    //
    // ux-7 (PR #16.5): the progress callback now receives an info object
    // with elapsedMs + a rowsWritten estimate. rowsWritten is not on the
    // wire mid-stream (the server only knows the final count); we
    // estimate it linearly from the average bytes-per-row across the
    // life of the stream so the modal can show "N rows" mid-flight.
    /** @type {Uint8Array[]} */
    const chunks = []
    let bytes = 0
    let rowsHint = 0
    const tickProgress = () => {
      const elapsedMs = (typeof performance !== 'undefined' ? performance.now() : Date.now()) - startedAt
      if (opts.onProgress) opts.onProgress(bytes, { rowsWritten: rowsHint || undefined, elapsedMs })
    }
    if (res.body && typeof res.body.getReader === 'function') {
      const reader = res.body.getReader()
      // eslint-disable-next-line no-constant-condition
      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        if (value) {
          chunks.push(value)
          bytes += value.byteLength
          // Rough mid-stream row estimate: count newline bytes in the
          // chunk. CSV / NDJSON / SQL all delimit records by LF so the
          // count is a reasonable lower bound (CSV uses CRLF so each row
          // contributes one LF; SQL has one LF per INSERT). The exact
          // value lands in the X-Aura-Export-Rows trailer after EOF.
          for (let i = 0; i < value.length; i++) {
            if (value[i] === 0x0A) rowsHint++
          }
          tickProgress()
        }
      }
    } else {
      const ab = await res.arrayBuffer()
      chunks.push(new Uint8Array(ab))
      bytes = ab.byteLength
      const u8 = new Uint8Array(ab)
      for (let i = 0; i < u8.length; i++) {
        if (u8[i] === 0x0A) rowsHint++
      }
      tickProgress()
    }

    const ct = res.headers.get('content-type') || 'application/octet-stream'
    triggerBlobDownload(chunks, ct, filename)

    // ux-3 + C2/C3 (PR #16): the server surfaces truncation + mid-stream
    // errors via response trailers (X-Truncated, X-Export-Error). Modern
    // browsers expose trailers via Response.headers AFTER the body is
    // fully consumed — so reading them here is well-defined. The values
    // are surfaced to callers so the UI can render a "file may be
    // incomplete" warning even when the HTTP status was 200.
    //
    // ux-7 (PR #16.5): also read X-Aura-Export-Rows for the authoritative
    // row count once the body is drained (rowsHint above is a heuristic).
    const truncated = (res.headers.get('x-truncated') || '').toLowerCase() === 'true'
    const serverError = res.headers.get('x-export-error') || ''
    const rowsHdr = res.headers.get('x-aura-export-rows')
    const rows = rowsHdr != null && rowsHdr !== '' ? Number(rowsHdr) : null
    return { filename, bytes, rows, rowCap, jobId, truncated, serverError }
  }

  /**
   * Pre-flight row count for the export modal (DC-5 + ux-6, PR #16.5).
   * Calls the rows endpoint with limit=0 + total=1; the server's Count()
   * is bounded by the same query path the export will use.
   *
   * @param {string} connId
   * @param {{schema:string, table:string, filter?:Array<{column:string, op:string, value?:any}>, signal?:AbortSignal}} opts
   * @returns {Promise<{total:number|null}>}
   */
  async countRowsPreflight(connId, opts) {
    const qs = new URLSearchParams()
    qs.set('limit', '0')
    qs.set('total', '1')
    if (opts.filter) {
      for (const p of opts.filter) {
        if (!p || !p.column || !p.op) continue
        qs.append('filter', `${p.column}:${p.op}:${p.value == null ? '' : String(p.value)}`)
      }
    }
    const path = `/connections/${enc(connId)}/schemas/${enc(opts.schema)}/tables/${enc(opts.table)}/rows?${qs.toString()}`
    try {
      const r = await request(path, { signal: opts.signal })
      const total = (r && typeof r.total === 'number') ? r.total : null
      return { total }
    } catch {
      // Count() can fail (driver lacks support, permission, etc.).
      // The modal degrades gracefully — no banner, no warning — so the
      // pre-flight failure is silent. The real export still runs.
      return { total: null }
    }
  }

  /**
   * importTable streams a CSV / NDJSON upload into a target table via
   * the multipart /connections/{id}/import endpoint (v0.3.2-E).
   * Mirrors exportTable's shape — the modal owns the AbortController +
   * the onProgress callback; the api method handles CSRF, multipart
   * assembly, and the canonical error envelope.
   *
   * @param {string} connId
   * @param {{
   *   schema: string,
   *   table: string,
   *   format: 'csv'|'ndjson',
   *   onConflict?: 'error'|'skip'|'update',
   *   file: File|Blob,
   *   signal?: AbortSignal,
   *   onProgress?: (uploadedBytes:number)=>void,
   * }} opts
   * @returns {Promise<{
   *   rowsImported: number,
   *   skipped: number,
   *   errors: Array<{rowIndex:number, code:string, message:string}>,
   *   totalErrors: number,
   *   bytes: number,
   *   truncated: boolean,
   *   format: string,
   *   jobId: string,
   * }>}
   */
  async importTable(connId, opts) {
    if (!connId || !opts || !opts.schema || !opts.table || !opts.format || !opts.file) {
      throw new AuraDBError(0, 'invalid-input', 'connId, schema, table, format, file required')
    }
    const fd = new FormData()
    fd.append('schema', opts.schema)
    fd.append('table', opts.table)
    fd.append('format', opts.format)
    if (opts.onConflict) fd.append('onConflict', opts.onConflict)
    fd.append('file', opts.file)

    // FormData + fetch does not surface upload progress directly. The
    // onProgress callback is invoked once with the final size after
    // submit completes so the modal can render a "uploaded N MB" pill;
    // a future XHR-based path could surface per-chunk progress.
    const res = await fetch(`${BASE}/connections/${enc(connId)}/import`, {
      method: 'POST',
      headers: {
        'X-CSRF-Token': csrfToken(),
        Accept: 'application/json',
      },
      body: fd,
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
      let detail = e.details !== undefined ? e.details : (e.detail !== undefined ? e.detail : env.detail)
      if (res.status === 409) {
        const ra = parseRetryAfterSeconds(res.headers.get('retry-after'))
        if (ra != null) {
          detail = (detail && typeof detail === 'object') ? { ...detail, retryAfter: ra } : { retryAfter: ra }
        }
      }
      throw new AuraDBError(
        res.status,
        e.code || env.code || 'unknown',
        e.message || env.message || res.statusText || 'import failed',
        detail,
        e.request_id || env.request_id,
      )
    }
    /** @type {any} */
    const body = await res.json()
    if (opts.onProgress && body && typeof body.bytes === 'number') {
      try { opts.onProgress(body.bytes) } catch { /* ignore */ }
    }
    return {
      rowsImported: Number(body.rowsImported) || 0,
      skipped: Number(body.skipped) || 0,
      errors: Array.isArray(body.errors) ? body.errors : [],
      totalErrors: Number(body.totalErrors) || 0,
      bytes: Number(body.bytes) || 0,
      truncated: Boolean(body.truncated),
      format: String(body.format || ''),
      jobId: String(body.jobId || res.headers.get('x-aura-import-jobid') || ''),
    }
  }
}

/**
 * sanitizeExportFilename mirrors SanitizeFilename in
 * pkg/dbadmin/export/encoder.go so the modal's preview matches what
 * the server will actually emit (ux-9, PR #16.5). Drops path
 * separators / quotes / control chars; collapses whitespace runs to
 * underscores; trims surrounding `._-`; falls back to "export" on
 * empty.
 *
 * @param {string} name
 * @returns {string}
 */
export function sanitizeExportFilename(name) {
  if (typeof name !== 'string' || name === '') return 'export'
  const maxLen = 200
  let out = ''
  let prevSpace = false
  for (let i = 0; i < name.length && out.length < maxLen; i++) {
    const ch = name[i]
    const code = ch.charCodeAt(0)
    if (ch === '/' || ch === '\\' || code === 0 || ch === '"' || ch === "'") continue
    if (code < 0x20) continue
    if (ch === ' ' || ch === '\t') {
      if (!prevSpace) { out += '_'; prevSpace = true }
    } else {
      out += ch
      prevSpace = false
    }
  }
  out = out.replace(/^[._-]+|[._-]+$/g, '')
  return out || 'export'
}

/**
 * parseRetryAfterSeconds parses an RFC 7231 Retry-After header. Returns
 * the wait in seconds (integer), or null when the header is missing /
 * unparseable. The server emits the delta-seconds form ("5"); we also
 * accept HTTP-date form for robustness.
 *
 * @param {string|null} v
 * @returns {number|null}
 */
function parseRetryAfterSeconds(v) {
  if (!v) return null
  const n = Number(v)
  if (Number.isFinite(n) && n >= 0) return Math.ceil(n)
  const date = Date.parse(v)
  if (!Number.isNaN(date)) {
    return Math.max(0, Math.ceil((date - Date.now()) / 1000))
  }
  return null
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

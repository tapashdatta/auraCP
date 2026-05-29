// Cell renderers — pure functions that classify a column by DatabaseTypeName
// and produce a display string for a value. Returning a string keeps the
// caller in charge of escaping (Svelte handles that automatically) so we
// never directly inject HTML. Where the design called for HTML markup the
// row template renders a wrapping element conditionally, e.g. a <span
// class="rg-null">NULL</span>, so renderers stay pure.
//
// Exported helpers are intentionally string-in/string-out; the column-kind
// classification is cached at the screen level so each cell pays O(1).

/**
 * @typedef {'number'|'boolean'|'json'|'datetime'|'binary'|'uuid'|'text'|'array'|'unknown'} CellKind
 */

/**
 * Classify a column by its DatabaseTypeName.
 * @param {string} typeName
 * @returns {CellKind}
 */
export function classifyKind(typeName) {
  const u = String(typeName || '').toUpperCase()
  // Boolean must come before number because TINYINT(1) and BIT(1) start
  // with type names that the number regex also accepts.
  if (/^(BOOL|BOOLEAN|BIT\(1\)|TINYINT\(1\))/.test(u)) return 'boolean'
  if (/^(INT|BIGINT|SMALLINT|TINYINT|MEDIUMINT|DECIMAL|NUMERIC|FLOAT|DOUBLE|REAL|MONEY|SERIAL|BIGSERIAL)/.test(u)) return 'number'
  if (/^(JSON|JSONB)/.test(u)) return 'json'
  if (/^(DATE|TIME|TIMESTAMP|DATETIME|TIMESTAMPTZ)/.test(u)) return 'datetime'
  if (/^(BYTEA|BLOB|VARBINARY|BINARY|RAW)/.test(u)) return 'binary'
  if (/^UUID/.test(u)) return 'uuid'
  if (/^(VARCHAR|CHAR|TEXT|CLOB|STRING|ENUM|NVARCHAR|NCHAR)/.test(u)) return 'text'
  if (/^ARRAY/.test(u) || /\[\]$/.test(u)) return 'array'
  return 'unknown'
}

/**
 * Format a number for display. Adds thousands separators when |n| >= 1000.
 * @param {number|string} v
 * @returns {string}
 */
export function formatNumber(v) {
  if (v === null || v === undefined || v === '') return ''
  const n = typeof v === 'number' ? v : Number(v)
  if (Number.isNaN(n)) return String(v)
  if (Math.abs(n) >= 1000) {
    try { return new Intl.NumberFormat('en-US').format(n) } catch { /* fall through */ }
  }
  return String(n)
}

/**
 * Format a datetime ISO string (UTC) → "YYYY-MM-DD HH:mm:ss". We do NOT
 * shift to local — DBAs expect what's in the table.
 * @param {string|Date|number} v
 * @returns {string}
 */
export function formatDateUTC(v) {
  if (v === null || v === undefined || v === '') return ''
  const d = v instanceof Date ? v : new Date(v)
  if (Number.isNaN(d.getTime())) return String(v)
  const Y = d.getUTCFullYear()
  const M = String(d.getUTCMonth() + 1).padStart(2, '0')
  const D = String(d.getUTCDate()).padStart(2, '0')
  const h = String(d.getUTCHours()).padStart(2, '0')
  const m = String(d.getUTCMinutes()).padStart(2, '0')
  const s = String(d.getUTCSeconds()).padStart(2, '0')
  return `${Y}-${M}-${D} ${h}:${m}:${s}`
}

/**
 * Decode a base64-string (Go-marshaled []byte) and report its byte length.
 * Returns 0 for null/empty.
 * @param {string} v
 */
export function binaryByteLen(v) {
  if (!v) return 0
  // Conservative: 4 chars → 3 bytes, minus padding
  const padding = (v.match(/=+$/) || [''])[0].length
  return Math.max(0, Math.floor((v.length / 4) * 3) - padding)
}

/**
 * Render a value to a display object the cell template can consume directly.
 * Distinguishes NULL (value===null|undefined) from empty string ("").
 *
 * @param {unknown} value
 * @param {{kind: CellKind, name?: string, typeName?: string}} colMeta
 * @returns {{
 *   text: string,
 *   className: string,
 *   title: string,
 *   isNull: boolean,
 *   isEmpty: boolean,
 *   align?: 'right'|'left'
 * }}
 */
export function renderCell(value, colMeta) {
  const kind = colMeta.kind
  if (value === null || value === undefined) {
    return { text: 'NULL', className: 'rg-cell rg-cell--null', title: 'NULL', isNull: true, isEmpty: false }
  }
  if (value === '') {
    return { text: '·', className: 'rg-cell rg-cell--empty', title: '(empty string)', isNull: false, isEmpty: true }
  }
  switch (kind) {
    case 'number': {
      const display = formatNumber(/** @type {any} */(value))
      const neg = typeof value === 'number' ? value < 0 : String(value).trim().startsWith('-')
      return {
        text: display,
        className: 'rg-cell rg-cell--number num' + (neg ? ' rg-cell--neg' : ''),
        title: String(value),
        isNull: false, isEmpty: false,
        align: 'right',
      }
    }
    case 'boolean': {
      const b = value === true || value === 'true' || value === 1 || value === '1'
      return {
        text: b ? 'true' : 'false',
        className: 'rg-cell rg-cell--bool ' + (b ? 'rg-bool--true' : 'rg-bool--false'),
        title: b ? 'true' : 'false',
        isNull: false, isEmpty: false,
      }
    }
    case 'json': {
      const s = typeof value === 'string' ? value : JSON.stringify(value)
      const t = s.trim()
      const lead = t.charAt(0)
      const summary = lead === '[' ? '[…]' : lead === '{' ? '{…}' : t.length > 24 ? t.slice(0, 24) + '…' : t
      return {
        text: summary,
        className: 'rg-cell rg-cell--json',
        title: s.length > 120 ? s.slice(0, 120) + '…' : s,
        isNull: false, isEmpty: false,
      }
    }
    case 'datetime': {
      const formatted = formatDateUTC(/** @type {any} */(value))
      return {
        text: formatted,
        className: 'rg-cell rg-cell--date num',
        title: String(value) + ' (UTC)',
        isNull: false, isEmpty: false,
      }
    }
    case 'binary': {
      const n = typeof value === 'string' ? binaryByteLen(value) : 0
      return {
        text: `<${n} bytes>`,
        className: 'rg-cell rg-cell--bin',
        title: `binary, ${n} bytes`,
        isNull: false, isEmpty: false,
      }
    }
    case 'uuid': {
      const s = String(value)
      return {
        text: s,
        className: 'rg-cell rg-cell--uuid',
        title: s,
        isNull: false, isEmpty: false,
      }
    }
    case 'array': {
      const arr = Array.isArray(value) ? value : (() => { try { return JSON.parse(String(value)) } catch { return [value] } })()
      const joined = (Array.isArray(arr) ? arr : []).map((x) => String(x)).join(', ')
      const display = joined.length > 60 ? `[${joined.slice(0, 60)}…]` : `[${joined}]`
      return {
        text: display,
        className: 'rg-cell rg-cell--array',
        title: JSON.stringify(arr),
        isNull: false, isEmpty: false,
      }
    }
    case 'text': {
      const s = String(value)
      return {
        text: s,
        className: 'rg-cell rg-cell--text',
        title: s,
        isNull: false, isEmpty: false,
      }
    }
    default: {
      const s = String(value)
      return {
        text: s,
        className: 'rg-cell rg-cell--unknown',
        title: 'type: ' + (colMeta.typeName || 'unknown'),
        isNull: false, isEmpty: false,
      }
    }
  }
}

/**
 * Validate + coerce a user-typed edit value against a column kind.
 * Returns { ok, value, error } so callers can keep the editor open on error.
 *
 * edit-5 NULL semantics:
 *   - The literal magic token `\N` always means SQL NULL (any kind).
 *   - An empty string ('') means NULL when the column is nullable; for
 *     non-nullable text columns the caller should set `allowEmptyString: true`
 *     so the user can clear a value to an empty string deliberately.
 *   - For text kinds with `allowEmptyString: true`, '' is kept as the empty
 *     string instead of being coerced to null. (The TableScreen surfaces a
 *     "NULL" toggle UI affordance that controls this from the keyboard.)
 *
 * @param {string} raw  the input's `value` string
 * @param {CellKind} kind
 * @param {{nullable?: boolean, allowEmptyString?: boolean}} [opts]
 * @returns {{ok: true, value: unknown} | {ok: false, error: string}}
 */
export function parseEditValue(raw, kind, opts = {}) {
  // \N is always NULL (magic token, edit-5).
  if (raw === '\\N') {
    if (opts.nullable === false) return { ok: false, error: 'required (not nullable)' }
    return { ok: true, value: null }
  }
  // Empty input → null by default, unless the caller explicitly wants
  // the literal empty string (only meaningful for text-ish kinds).
  if (raw === '') {
    if (opts.allowEmptyString && (kind === 'text' || kind === 'uuid' || kind === 'unknown')) {
      return { ok: true, value: '' }
    }
    if (opts.nullable === false) return { ok: false, error: 'required (not nullable)' }
    return { ok: true, value: null }
  }
  switch (kind) {
    case 'number': {
      const n = Number(raw)
      if (Number.isNaN(n)) return { ok: false, error: 'not a number' }
      return { ok: true, value: n }
    }
    case 'boolean': {
      const t = raw.trim().toLowerCase()
      if (t === 'true' || t === '1' || t === 'yes') return { ok: true, value: true }
      if (t === 'false' || t === '0' || t === 'no') return { ok: true, value: false }
      if (t === 'null') return { ok: true, value: null }
      return { ok: false, error: 'must be true / false / null' }
    }
    case 'json': {
      try { return { ok: true, value: JSON.parse(raw) } }
      catch (e) { return { ok: false, error: 'invalid JSON: ' + (/** @type {Error} */(e).message) } }
    }
    case 'datetime': {
      const d = new Date(raw)
      if (Number.isNaN(d.getTime())) return { ok: false, error: 'invalid date' }
      return { ok: true, value: d.toISOString() }
    }
    case 'binary':
      return { ok: false, error: 'binary not editable inline' }
    default:
      return { ok: true, value: raw }
  }
}

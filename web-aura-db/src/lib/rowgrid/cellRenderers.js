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
 * @typedef {'integer'|'number'|'boolean'|'json'|'datetime'|'binary'|'uuid'|'text'|'array'|'unknown'} CellKind
 */

/**
 * Classify a column by its DatabaseTypeName.
 *
 * edit-15 (PR #12.5): integer columns are split out of the catch-all
 * 'number' kind so parseEditValue can reject decimal input ("42.7") on
 * an INT column before it hits the wire — drivers that silently floor
 * the value are MySQL-specific; Postgres rejects with 22P02, SQLite
 * happily stores the float. Floats stay under 'number' (DECIMAL /
 * NUMERIC / FLOAT / DOUBLE / REAL / MONEY).
 *
 * @param {string} typeName
 * @returns {CellKind}
 */
export function classifyKind(typeName) {
  const u = String(typeName || '').toUpperCase()
  // Boolean must come before integer because TINYINT(1) and BIT(1) start
  // with type names that the number regex also accepts.
  if (/^(BOOL|BOOLEAN|BIT\(1\)|TINYINT\(1\))/.test(u)) return 'boolean'
  if (/^(INT|BIGINT|SMALLINT|TINYINT|MEDIUMINT|SERIAL|BIGSERIAL)/.test(u)) return 'integer'
  if (/^(DECIMAL|NUMERIC|FLOAT|DOUBLE|REAL|MONEY)/.test(u)) return 'number'
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

// perf-7 (PR #12.5): module-scoped LRU-ish cache for parsed array values.
// renderCell is called for every visible cell on every scroll tick, so
// caching the JSON.parse output by the raw input string is a meaningful
// win on tables that have wide array/jsonb columns. Cap at 256 entries
// to bound memory; oldest entry evicted on overflow.
/** @type {Map<string, unknown>} */
const arrayParseCache = new Map()
const ARRAY_CACHE_CAP = 256
function parseArrayCached(s) {
  const hit = arrayParseCache.get(s)
  if (hit !== undefined) return hit
  /** @type {unknown} */
  let parsed
  try { parsed = JSON.parse(s) } catch { parsed = [s] }
  arrayParseCache.set(s, parsed)
  if (arrayParseCache.size > ARRAY_CACHE_CAP) {
    // Map preserves insertion order; deleting the first key evicts the
    // oldest. Cheap enough that we don't need a true LRU.
    const oldest = arrayParseCache.keys().next().value
    if (oldest !== undefined) arrayParseCache.delete(oldest)
  }
  return parsed
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
    case 'integer':
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
      // perf-7 (PR #12.5): renderCell runs every scroll tick for every
      // visible cell — JSON.parse on a string-encoded array column was
      // measurable in profiles on tables with arr<<jsonb>> columns. The
      // module-level WeakMap-ish cache below keys parsed results by the
      // raw string so we pay JSON.parse once per unique value across
      // every render, not once per render. Cache is intentionally a
      // bounded LRU-ish Map (cleared above 256 entries) so it never
      // grows unbounded on long-lived sessions.
      const arr = Array.isArray(value) ? value : parseArrayCached(String(value))
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
    case 'integer': {
      // edit-15 (PR #12.5): INT-family columns reject decimal input
      // before it hits the wire. MySQL silently floors "42.7" to 42;
      // Postgres returns 22P02 invalid_text_representation. Surfacing
      // the error at parse time is consistent across drivers.
      const t = raw.trim()
      // Allow optional sign + digits only — no decimal point, no
      // scientific notation, no thousand separators (the user can
      // paste a comma-separated number and we'd silently NaN below
      // otherwise; explicit early reject is clearer).
      if (!/^-?\d+$/.test(t)) return { ok: false, error: 'integer required (no decimals)' }
      const n = Number(t)
      if (Number.isNaN(n) || !Number.isFinite(n)) return { ok: false, error: 'not an integer' }
      return { ok: true, value: n }
    }
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

// Filter input parser. Maps a user-typed filter string into the operator
// shape the rows-endpoint expects (col:op:value). The grammar mirrors the
// handler-side parseFilter contract but is forgiving: a bare query against
// a text column becomes ILIKE %text%, against a numeric column becomes =.
//
// Operators recognized (case-insensitive on prefixes):
//   is null | null                        → IS NULL
//   is not null | not null                → IS NOT NULL
//   = / ==  <value>                       → =
//   != <value>                            → !=
//   > <value>     >= <value>              → > / >=
//   < <value>     <= <value>              → < / <=
//   like <pattern>     ilike <pattern>    → LIKE / ILIKE
//   in (a,b,c)                            → IN (joined comma)
//   default text col                      → ILIKE %raw%
//   default numeric col                   → =

/**
 * @typedef {import('./cellRenderers.js').CellKind} CellKind
 *
 * @typedef {{
 *   ok: true,
 *   op: '='|'!='|'<'|'<='|'>'|'>='|'LIKE'|'ILIKE'|'IS NULL'|'IS NOT NULL'|'IN',
 *   value: string | string[] | null,
 *   raw: string,
 * } | {
 *   ok: false,
 *   error: string,
 *   raw: string,
 * }} ParsedFilter
 */

/**
 * @param {string} raw
 * @param {CellKind} [kind]
 * @returns {ParsedFilter | null}    null when raw is blank → no filter
 */
export function parseFilterInput(raw, kind = 'text') {
  const trimmed = String(raw ?? '').trim()
  if (trimmed === '') return null

  // is [not] null
  if (/^is\s+null$|^null$/i.test(trimmed)) return { ok: true, op: 'IS NULL', value: null, raw: trimmed }
  if (/^is\s+not\s+null$|^not\s+null$/i.test(trimmed)) return { ok: true, op: 'IS NOT NULL', value: null, raw: trimmed }

  // Comparison operators
  let m
  m = /^(==|=)\s*(.+)$/.exec(trimmed)
  if (m) return { ok: true, op: '=', value: m[2].trim(), raw: trimmed }
  m = /^!=\s*(.+)$/.exec(trimmed)
  if (m) return { ok: true, op: '!=', value: m[1].trim(), raw: trimmed }
  m = /^(>=|<=|>|<)\s*(.+)$/.exec(trimmed)
  if (m) return { ok: true, op: /** @type {any} */(m[1]), value: m[2].trim(), raw: trimmed }
  m = /^(i?like)\s+(.+)$/i.exec(trimmed)
  if (m) return { ok: true, op: /** @type {any} */(m[1].toUpperCase()), value: m[2].trim(), raw: trimmed }
  m = /^in\s*\(\s*(.+?)\s*\)$/i.exec(trimmed)
  if (m) {
    const parts = m[1].split(',').map((s) => s.trim()).filter((s) => s !== '')
    if (parts.length === 0) return { ok: false, error: 'IN requires at least one value', raw: trimmed }
    return { ok: true, op: 'IN', value: parts, raw: trimmed }
  }

  // Default: numeric col → equality; everything else → substring ILIKE.
  if (kind === 'number') {
    if (Number.isNaN(Number(trimmed))) {
      return { ok: false, error: 'numeric column expects a number or comparison', raw: trimmed }
    }
    return { ok: true, op: '=', value: trimmed, raw: trimmed }
  }
  if (kind === 'boolean') {
    const t = trimmed.toLowerCase()
    if (t === 'true' || t === 'false') return { ok: true, op: '=', value: t, raw: trimmed }
    return { ok: false, error: 'boolean column expects true/false', raw: trimmed }
  }
  return { ok: true, op: 'ILIKE', value: '%' + trimmed + '%', raw: trimmed }
}

/**
 * Serialize a parsed filter to the wire format col:op:value the rows
 * handler decodes.
 *
 * WIRE-03: for IN / NOT IN the value MUST be a JSON-encoded array so the
 * backend's parseFilter can rebuild the slice that rows.Predicate expects.
 * Sending a comma-joined string would land in rows.Predicate.Value as a
 * single string and the rows package would reject IN with a non-slice
 * value (see rows.Predicate doc / ErrInvalidPredicate path). The backend
 * handler (parseFilter in pkg/dbadmin/httpapi/handlers_rows.go) decodes
 * IN values via json.Unmarshal when present.
 *
 * @param {string} col
 * @param {ParsedFilter} parsed
 * @returns {string}
 */
export function serializeFilter(col, parsed) {
  if (!parsed || !parsed.ok) return ''
  if (parsed.op === 'IS NULL' || parsed.op === 'IS NOT NULL') {
    return `${col}:${parsed.op}:`
  }
  if ((parsed.op === 'IN' || parsed.op === 'NOT IN') && Array.isArray(parsed.value)) {
    // JSON-encode the array so the backend can json.Unmarshal it into a
    // []any slice. Each element is sent verbatim as a string token (the
    // backend driver coerces to the column's actual type).
    return `${col}:${parsed.op}:${JSON.stringify(parsed.value)}`
  }
  const value = Array.isArray(parsed.value) ? parsed.value.join(',') : String(parsed.value ?? '')
  return `${col}:${parsed.op}:${value}`
}

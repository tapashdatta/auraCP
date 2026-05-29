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
//   in (a,b,c)                            → IN (JSON-encoded array on wire)
//   not in (a,b,c)                        → NOT IN (JSON-encoded array on wire)
//   default text col                      → ILIKE %raw%
//   default numeric col                   → =
//
// edit-12 (PR #12.5): the IS NULL / IS NOT NULL / NULL / NOT NULL forms are
// strict — trailing text is rejected so `is null xyz` no longer silently
// downgrades to ILIKE '%is null xyz%'. The user gets a clear error toast
// surfaced via the rg-filter__input--err style + tooltip.
//
// WIRE-10 (PR #12.5): NOT IN is now reachable from the filter input. The
// wire serialization (serializeFilter) was already JSON-encoding NOT IN
// when given one — this just exposes the operator on the client parser.
//
// WIRE-15 (PR #12.5): filter wire format is `col:op:value`. Operators with
// spaces (IS NULL, IS NOT NULL, NOT IN) are preserved verbatim in the op
// slot; the value slot is JSON for IN/NOT IN and an empty string for
// IS NULL / IS NOT NULL. Colons inside the value are NOT escaped — the
// backend parser splits on the FIRST two colons only and treats the
// remainder as the literal value. Bare colons in filter values are
// therefore safe; this is documented here for parity with backend
// pkg/dbadmin/httpapi/handlers_rows.go parseFilter.

/**
 * @typedef {import('./cellRenderers.js').CellKind} CellKind
 *
 * @typedef {{
 *   ok: true,
 *   op: '='|'!='|'<'|'<='|'>'|'>='|'LIKE'|'ILIKE'|'IS NULL'|'IS NOT NULL'|'IN'|'NOT IN',
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

  // is [not] null — strict, anchored to end of string (edit-12).
  // Detect the malformed `is null xyz` / `not null xyz` forms early and
  // surface a parse error instead of silently downgrading to ILIKE.
  if (/^is\s+null$|^null$/i.test(trimmed)) return { ok: true, op: 'IS NULL', value: null, raw: trimmed }
  if (/^is\s+not\s+null$|^not\s+null$/i.test(trimmed)) return { ok: true, op: 'IS NOT NULL', value: null, raw: trimmed }
  if (/^(is\s+)?(not\s+)?null\b/i.test(trimmed)) {
    return { ok: false, error: 'use "is null" or "is not null" with no trailing text', raw: trimmed }
  }

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
  // WIRE-10: NOT IN must come before IN — same-prefix match would otherwise
  // route `not in (a,b)` to the bare IN branch and accept the literal
  // `not in (a,b)` parens contents (wrong shape).
  m = /^not\s+in\s*\(\s*(.+?)\s*\)$/i.exec(trimmed)
  if (m) {
    const parts = m[1].split(',').map((s) => s.trim()).filter((s) => s !== '')
    if (parts.length === 0) return { ok: false, error: 'NOT IN requires at least one value', raw: trimmed }
    return { ok: true, op: 'NOT IN', value: parts, raw: trimmed }
  }
  m = /^in\s*\(\s*(.+?)\s*\)$/i.exec(trimmed)
  if (m) {
    const parts = m[1].split(',').map((s) => s.trim()).filter((s) => s !== '')
    if (parts.length === 0) return { ok: false, error: 'IN requires at least one value', raw: trimmed }
    return { ok: true, op: 'IN', value: parts, raw: trimmed }
  }

  // Default: numeric col → equality; everything else → substring ILIKE.
  // edit-15 / WIRE-10: both 'integer' and 'number' kinds route through
  // the numeric branch — the backend treats them identically once the
  // value is on the wire, so the filter contract stays a string.
  if (kind === 'number' || kind === 'integer') {
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

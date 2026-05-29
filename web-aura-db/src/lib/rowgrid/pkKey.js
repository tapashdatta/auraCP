// Primary-key encoder / decoder. We always emit the `col=val` form even
// for single-column PKs to avoid ambiguity (and the handler accepts both
// the bare-string and col=val forms — sticking with col=val keeps composite
// keys correct).
//
// Encoding rules (WIRE-06):
//   - Each segment: encodeURIComponent(col) + '=' + encodeURIComponent(val)
//   - We then explicitly escape the bytes that have structural meaning in
//     our own format — ',' (segment delimiter) and '=' (kv delimiter) —
//     because encodeURIComponent leaves them alone (both are RFC-3986
//     sub-delims / equals that are legal-but-unreserved). Without this,
//     a value containing a literal "," or "=" would collapse the split.
//   - Composite key segments are joined with ','. round-trip via parsePKKey
//     decodes both layers in reverse.
//   - The full PK string then gets URL-encoded ONCE more by the api.js
//     templating (enc()) — net effect, the server's parsePK sees col=val.

/**
 * Escape the two bytes that are structurally meaningful in our PK format
 * but left unescaped by encodeURIComponent: ',' and '='. encodeURIComponent
 * already produces a fully percent-encoded string for any byte outside the
 * URI-unreserved set, so the only remaining literal bytes that could
 * collide with our format are ',' and '='. We rewrite them to their
 * percent-encoded forms; the inverse decodeURIComponent in parsePKKey then
 * recovers the original characters automatically. (We deliberately do NOT
 * touch '%' here because encodeURIComponent already escaped any genuine
 * literal '%' in the source string as '%25'.)
 *
 * @param {string} s
 * @returns {string}
 */
function escapeStruct(s) {
  return s.replace(/,/g, '%2C').replace(/=/g, '%3D')
}

/**
 * Build a PK key string from a row and the ordered list of PK column names.
 *
 * @param {any[]} row          row values, in column order
 * @param {string[]} pkColumns names of PK columns
 * @param {string[]} columnOrder names of every column in the current grid, in row[] order
 * @returns {string}
 */
export function buildPKKey(row, pkColumns, columnOrder) {
  if (!Array.isArray(pkColumns) || pkColumns.length === 0) return ''
  const parts = []
  for (const c of pkColumns) {
    const idx = columnOrder.indexOf(c)
    if (idx < 0) continue
    const v = row[idx]
    const colEnc = escapeStruct(encodeURIComponent(c))
    const valEnc = escapeStruct(encodeURIComponent(v === null || v === undefined ? '' : String(v)))
    parts.push(colEnc + '=' + valEnc)
  }
  return parts.join(',')
}

/**
 * Inverse — parses col=val[,col=val…] into { col: val } map. Used by tests
 * and by future inline-PK editors.
 *
 * @param {string} key
 * @returns {Record<string, string>}
 */
export function parsePKKey(key) {
  /** @type {Record<string,string>} */
  const out = {}
  if (!key) return out
  for (const seg of key.split(',')) {
    const eq = seg.indexOf('=')
    if (eq < 0) continue
    const k = decodeURIComponent(seg.slice(0, eq))
    const v = decodeURIComponent(seg.slice(eq + 1))
    out[k] = v
  }
  return out
}

// Sort-cycle helpers. Single click cycles asc → desc → none; shift-click
// appends/cycles without clearing other keys. Kept as a pure module so the
// behavior is easy to unit-test.

/**
 * @typedef {{ col: string, dir: 'asc'|'desc' }} SortKey
 */

/**
 * @param {SortKey[]} keys
 * @param {string} col
 * @param {boolean} append   true for shift-click → multi-sort
 * @returns {SortKey[]}
 */
export function cycleSort(keys, col, append = false) {
  const list = (keys || []).slice()
  const idx = list.findIndex((k) => k.col === col)

  if (!append) {
    // Single-column sort: replace entire list.
    if (idx < 0) return [{ col, dir: 'asc' }]
    const cur = list[idx]
    if (cur.dir === 'asc') return [{ col, dir: 'desc' }]
    return [] // cycle off
  }
  // Multi-sort: leave others alone.
  if (idx < 0) {
    list.push({ col, dir: 'asc' })
    return list
  }
  const cur = list[idx]
  if (cur.dir === 'asc') {
    list[idx] = { col, dir: 'desc' }
    return list
  }
  // desc → remove
  list.splice(idx, 1)
  return list
}

/**
 * Serialize sortKeys to the ?sort=... query-string list (with `-` prefix for desc).
 * @param {SortKey[]} keys
 * @returns {string[]}
 */
export function serializeSort(keys) {
  return (keys || []).map((k) => (k.dir === 'desc' ? '-' : '') + k.col)
}

// localStorage-backed layout persistence for the row grid. Keys are scoped
// by connection-id/schema/table so different tables remember their own
// widths, sort, density, etc. Filters are intentionally NOT persisted
// (they're query intent, not layout).
//
// Schema is versioned via the `v` field so a future change can wipe stale
// data cleanly.

const VERSION = 1
const PREFIX = 'auracp.rowgrid'

/**
 * @typedef {{
 *   v: number,
 *   columnWidths?: Record<string, number>,
 *   columnOrder?: string[],
 *   hiddenCols?: string[],
 *   frozenLeftCount?: number,
 *   pageSize?: number,
 *   density?: 'compact'|'cozy'|'comfortable',
 *   sortKeys?: Array<{col:string,dir:'asc'|'desc'}>,
 * }} GridLayout
 */

/**
 * @param {string} connId
 * @param {string} schema
 * @param {string} table
 */
export function layoutKey(connId, schema, table) {
  return `${PREFIX}.${connId}.${schema}.${table}.layout`
}

/**
 * @param {string} connId @param {string} schema @param {string} table
 * @returns {GridLayout}
 */
export function loadLayout(connId, schema, table) {
  /** @type {GridLayout} */
  const fallback = { v: VERSION }
  if (typeof localStorage === 'undefined') return fallback
  try {
    const raw = localStorage.getItem(layoutKey(connId, schema, table))
    if (!raw) return fallback
    const parsed = JSON.parse(raw)
    if (!parsed || typeof parsed !== 'object' || parsed.v !== VERSION) return fallback
    return parsed
  } catch {
    return fallback
  }
}

/**
 * @param {string} connId @param {string} schema @param {string} table
 * @param {GridLayout} layout
 */
export function saveLayout(connId, schema, table, layout) {
  if (typeof localStorage === 'undefined') return
  try {
    localStorage.setItem(layoutKey(connId, schema, table), JSON.stringify({ ...layout, v: VERSION }))
  } catch {
    /* quota/private mode — ignore */
  }
}

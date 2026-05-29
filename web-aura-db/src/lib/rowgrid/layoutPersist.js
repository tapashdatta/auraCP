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

// PR #12.5: debounced layout save. Column resize drags fire pointermove
// at ~60 Hz; each move synchronously serializes the layout to JSON and
// writes to localStorage on the main thread, blocking the next frame
// for 1-2 ms on large layouts. Coalescing to a single trailing-edge
// write per 150 ms cuts the per-drag cost by ~50× without changing the
// final persisted state (the last write still wins when the drag ends).
/** @type {Map<string, ReturnType<typeof setTimeout>>} */
const pendingSaves = new Map()
const SAVE_DEBOUNCE_MS = 150

/**
 * Save layout with trailing-edge debounce. Keyed by (connId, schema,
 * table) so different tables don't clobber each other's pending saves.
 *
 * @param {string} connId @param {string} schema @param {string} table
 * @param {GridLayout} layout
 */
export function saveLayoutDebounced(connId, schema, table, layout) {
  if (typeof localStorage === 'undefined') return
  const key = layoutKey(connId, schema, table)
  const pending = pendingSaves.get(key)
  if (pending) clearTimeout(pending)
  pendingSaves.set(key, setTimeout(() => {
    pendingSaves.delete(key)
    saveLayout(connId, schema, table, layout)
  }, SAVE_DEBOUNCE_MS))
}

/**
 * Synchronously flush any pending debounced save for a (connId, schema,
 * table) tuple. Bound to grid teardown (perf-9) so a tab close doesn't
 * drop the last in-flight write.
 *
 * @param {string} connId @param {string} schema @param {string} table
 * @param {GridLayout} layout
 */
export function flushLayoutSave(connId, schema, table, layout) {
  const key = layoutKey(connId, schema, table)
  const pending = pendingSaves.get(key)
  if (pending) {
    clearTimeout(pending)
    pendingSaves.delete(key)
    saveLayout(connId, schema, table, layout)
  }
}

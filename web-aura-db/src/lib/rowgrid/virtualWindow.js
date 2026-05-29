// Hand-rolled virtualization-window computation. Pure function for ease of
// testing — the actual scroll-listening lives in the Svelte composable that
// wraps it.
//
// Algorithm (see design doc): given the scroll position, row height, and
// the total row count, compute the slice [start, end) of rows to render and
// the y-offset where that slice begins in the viewport's coordinate space.

/**
 * @param {object} opts
 * @param {number} opts.scrollTop    body element scrollTop
 * @param {number} opts.viewportH    body clientHeight (excluding sticky header)
 * @param {number} opts.rowH         row height in px (after density)
 * @param {number} opts.total        total rows in the current data slice
 * @param {number} [opts.buffer]     overscan rows above + below (default 4)
 * @returns {{startIdx:number, endIdx:number, visCount:number, yOffset:number}}
 */
export function virtualWindow({ scrollTop, viewportH, rowH, total, buffer = 4 }) {
  if (!rowH || rowH <= 0 || total <= 0) {
    return { startIdx: 0, endIdx: 0, visCount: 0, yOffset: 0 }
  }
  const first = Math.max(0, Math.floor(scrollTop / rowH))
  const visCount = Math.max(1, Math.ceil(viewportH / rowH))
  const startIdx = Math.max(0, first - buffer)
  const endIdx = Math.min(total, first + visCount + buffer)
  const yOffset = startIdx * rowH
  return { startIdx, endIdx, visCount, yOffset }
}

/**
 * Compute aria-rowindex for a 0-based body row index given the current page.
 *
 * a11y-3: the slot ordering is
 *   1            → column header
 *   2            → filter row
 *   3            → new-row form (only when present)
 *   3 or 4 .. N  → data rows, offset by the global page offset
 *
 * The `hasNewRow` flag shifts data-row indices down by one so the new-row
 * form does not share aria-rowindex=2 with the filter bar.
 *
 * @param {object} opts
 * @param {number} opts.idx     0-based within current page data array
 * @param {number} opts.offset  global offset (page-1)*limit
 * @param {boolean} [opts.hasNewRow]
 * @returns {number}
 */
export function ariaRowIndex({ idx, offset, hasNewRow }) {
  // base = 3 (header=1, filter=2, then either data starts at 3, or new
  // row is 3 and data starts at 4).
  const base = hasNewRow ? 4 : 3
  return base + (offset | 0) + idx
}

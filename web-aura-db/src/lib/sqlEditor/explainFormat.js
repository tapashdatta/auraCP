// Pure formatting + bucket helpers for the EXPLAIN inspector. No DOM, no
// Svelte — easy to unit-test in isolation.
//
// The flame-tree's color scale is a 5-step ordinal quantization keyed on
// the share-of-total of the node's chosen metric (cost or time).
// PR #14: numeric labels (cost-pct chip) are always rendered alongside the
// color so the WCAG 1.4.1 ("color is never the only signal") requirement
// is satisfied.

const NF_INT = (typeof Intl !== 'undefined') ? new Intl.NumberFormat('en-US') : null

/**
 * Format a millisecond duration. Sub-millisecond is "0.0ms"; everything
 * else is `.toFixed(2)` plus the "ms" suffix. NaN / Infinity / null
 * collapse to "—" (the inspector renders unavailable metrics as em-dash).
 *
 * @param {number} v
 * @returns {string}
 */
export function fmtMs(v) {
  if (v == null || !Number.isFinite(v)) return '—'
  if (v === 0) return '0.00ms'
  if (v < 0.01) return '<0.01ms'
  return v.toFixed(2) + 'ms'
}

/**
 * Format an integer row count. Uses thousands separators and a "k/M/B"
 * suffix for big numbers so the ribbon stays compact.
 *
 * @param {number|bigint} v
 * @returns {string}
 */
export function fmtRows(v) {
  if (v == null) return '—'
  const n = (typeof v === 'bigint') ? Number(v) : v
  if (!Number.isFinite(n)) return '—'
  if (n === 0) return '0'
  if (Math.abs(n) >= 1e9) return (n / 1e9).toFixed(1).replace(/\.0$/, '') + 'B'
  if (Math.abs(n) >= 1e6) return (n / 1e6).toFixed(1).replace(/\.0$/, '') + 'M'
  if (Math.abs(n) >= 1e4) return (n / 1e3).toFixed(1).replace(/\.0$/, '') + 'k'
  return NF_INT ? NF_INT.format(n) : String(n)
}

/**
 * Format a cost estimate. PG cost is unit-less; we render two decimals
 * unless the number is huge.
 *
 * @param {number} v
 * @returns {string}
 */
export function fmtCost(v) {
  if (v == null || !Number.isFinite(v)) return '—'
  if (v === 0) return '0.00'
  if (Math.abs(v) >= 1e6) return (v / 1e6).toFixed(2) + 'M'
  if (Math.abs(v) >= 1e4) return (v / 1e3).toFixed(2) + 'k'
  return v.toFixed(2)
}

/**
 * Format a percentage from a 0..1 share. Sub-1% renders "<1%".
 *
 * @param {number} share
 * @returns {string}
 */
export function fmtPct(share) {
  if (!Number.isFinite(share)) return '—'
  const pct = share * 100
  if (pct >= 1) return Math.round(pct) + '%'
  if (pct > 0) return '<1%'
  return '0%'
}

/**
 * Compact "k" formatter for medium-magnitude numbers (used by the
 * ribbon's NODES tile and similar dense slots).
 *
 * @param {number} n
 * @returns {string}
 */
export function kFormat(n) {
  if (n == null || !Number.isFinite(n)) return '—'
  if (Math.abs(n) >= 1e6) return (n / 1e6).toFixed(1).replace(/\.0$/, '') + 'M'
  if (Math.abs(n) >= 1e3) return (n / 1e3).toFixed(1).replace(/\.0$/, '') + 'k'
  return String(n)
}

/**
 * Quantize a share-of-total into one of five ordinal color buckets.
 * Buckets are closed-open intervals on `share`:
 *   step 1 [0.00, 0.05)   cool
 *   step 2 [0.05, 0.15)
 *   step 3 [0.15, 0.35)
 *   step 4 [0.35, 0.65)
 *   step 5 [0.65, 1.00]   hottest
 *
 * @param {number} share
 * @returns {1|2|3|4|5}
 */
export function costStep(share) {
  if (!Number.isFinite(share) || share < 0.05) return 1
  if (share < 0.15) return 2
  if (share < 0.35) return 3
  if (share < 0.65) return 4
  return 5
}

/**
 * Detect a node that ANALYZE planned but never executed (PG only).
 * `loops === 0` means the row never fired at runtime — every "Actual*"
 * metric on that node is misleadingly zero.
 *
 * @param {{loops?:number}|null|undefined} metrics
 * @param {{engine:string, executionTimeMs?:number}|null|undefined} plan
 * @returns {boolean}
 */
export function isNotExecuted(metrics, plan) {
  if (!metrics || !plan) return false
  const analyzed = plan.engine === 'postgres' && (plan.executionTimeMs > 0)
  if (!analyzed) return false
  // MariaDB doesn't carry loops.
  if (plan.engine !== 'postgres') return false
  return metrics.loops === 0
}

/**
 * Compute the share-of-total to use for color + bar width. Uses time when
 * the plan was analyzed (PG only); else uses cost.
 *
 * @param {{costTotal?:number, timeTotalMs?:number, loops?:number}} metrics
 * @param {{engine:string, executionTimeMs?:number, total:{costTotal?:number, timeTotalMs?:number}}} plan
 * @returns {number} clamped to [0.02, 1.0]
 */
export function shareFor(metrics, plan) {
  if (!metrics || !plan || !plan.total) return 0.02
  // FIX CORR-3 (PR #14): nodes that ANALYZE planned but didn't execute
  // contribute zero to the share-of-total — their "actual" metrics are
  // misleadingly zero and would otherwise paint as a min-share sliver.
  if (isNotExecuted(metrics, plan)) return 0.02
  const analyzed = plan.engine === 'postgres' && (plan.executionTimeMs > 0)
  let share
  if (analyzed && plan.total.timeTotalMs > 0) {
    share = (metrics.timeTotalMs || 0) / plan.total.timeTotalMs
  } else if (plan.total.costTotal > 0) {
    share = (metrics.costTotal || 0) / plan.total.costTotal
  } else {
    share = 0
  }
  if (!Number.isFinite(share) || share < 0) share = 0
  if (share > 1) share = 1
  // Floor to 2% so every bar has a touchable sliver.
  return Math.max(0.02, share)
}

/**
 * Safely coerce a value to a finite number; NaN / Infinity / null become 0.
 *
 * @param {unknown} v
 * @returns {number}
 */
export function safeFloat(v) {
  if (v == null) return 0
  const n = Number(v)
  return Number.isFinite(n) ? n : 0
}

/**
 * Detect rows-actual vs rows-expected mismatch hotspots (>10x in either
 * direction) — used for the hotspot overlay glyph.
 *
 * @param {{rowsExpected?:number, rowsActual?:number, loops?:number}} m
 * @returns {{estimate:boolean, loops:boolean}}
 */
export function hotspotFlags(m) {
  if (!m) return { estimate: false, loops: false }
  const exp = Math.max(1, m.rowsExpected || 0)
  const act = m.rowsActual || 0
  const ratio = act / exp
  const estimate = act > 0 && (ratio > 10 || ratio < 0.1)
  const loops = (m.loops || 0) > 1000
  return { estimate, loops }
}

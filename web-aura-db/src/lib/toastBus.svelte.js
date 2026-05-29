// Toast bus — a tiny global publish/subscribe for non-blocking notifications.
//
// FIX (PR #11 a11y-10): Errors used to flash as plain text inside the
// status bar with no role=alert, no dismissal, and no per-error history.
// This module exposes a single `toasts` $state ring buffer that any
// component can push to, plus a render-time region in <ToastRegion>.
//
// API surface — keep small on purpose:
//   pushToast({ message, tone })  → returns id
//   dismissToast(id)
//   clearToasts()
//
// `tone` defaults to 'info'. 'danger' / 'warning' / 'success' map to the
// existing pill colour ramp. The renderer sets role="alert" on danger
// toasts (assertive) and role="status" elsewhere (polite) so AT users
// get appropriate interrupt behaviour.

/** @typedef {{ id:number, message:string, tone:'info'|'success'|'warning'|'danger', timeoutMs?:number }} Toast */

/** @type {{ list: Toast[] }} */
export const toasts = $state({ list: [] })

let _seq = 1
/** @type {Map<number, ReturnType<typeof setTimeout>>} */
const _timers = new Map()

/**
 * Push a toast. Returns its id so callers can dismiss programmatically.
 * The default timeout is 5s for non-danger toasts; danger toasts stick
 * until the user dismisses them.
 *
 * @param {{ message: string, tone?: 'info'|'success'|'warning'|'danger', timeoutMs?: number }} opts
 * @returns {number}
 */
export function pushToast(opts) {
  const id = _seq++
  const tone = opts.tone || 'info'
  const timeoutMs = opts.timeoutMs ?? (tone === 'danger' ? 0 : 5000)
  toasts.list = [...toasts.list, { id, message: String(opts.message ?? ''), tone, timeoutMs }]
  if (timeoutMs > 0 && typeof setTimeout !== 'undefined') {
    const tk = setTimeout(() => dismissToast(id), timeoutMs)
    _timers.set(id, tk)
  }
  return id
}

/** @param {number} id */
export function dismissToast(id) {
  toasts.list = toasts.list.filter((t) => t.id !== id)
  const tk = _timers.get(id)
  if (tk) { clearTimeout(tk); _timers.delete(id) }
}

export function clearToasts() {
  toasts.list = []
  for (const tk of _timers.values()) clearTimeout(tk)
  _timers.clear()
}

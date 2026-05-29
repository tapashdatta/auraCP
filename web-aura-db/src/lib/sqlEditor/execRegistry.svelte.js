// Registry of in-flight ExecHandles keyed by editor tab id. Lets the
// ExecuteToolbar, ResultsPane, and global Cmd+. shortcut all drive
// cancellation through a single source of truth.

/**
 * @typedef {{ ref:string, cancel:()=>void } & Record<string, any>} ExecHandle
 */

/** @type {Map<string, ExecHandle>} */
const handles = new Map()

/** @type {Set<(tabId:string)=>void>} */
const completionListeners = new Set()

/** @param {string} tabId @param {ExecHandle} h */
export function register(tabId, h) {
  // EXEC-3: if there's already a handle on this tab, cancel it BEFORE
  // storing the new one so the server has a chance to ack the cancel
  // frame before the next exec ships. `cancel` is synchronous wire-
  // wise; the WS frame is enqueued before we return.
  const prior = handles.get(tabId)
  if (prior && prior !== h) {
    try { prior.cancel() } catch { /* ignore */ }
  }
  handles.set(tabId, h)
}

/** @param {string} tabId */
export function complete(tabId) {
  handles.delete(tabId)
  for (const fn of completionListeners) {
    try { fn(tabId) } catch { /* ignore */ }
  }
}

/** @param {string} tabId */
export function cancel(tabId) {
  const h = handles.get(tabId)
  if (!h) return false
  try { h.cancel() } catch { /* ignore */ }
  handles.delete(tabId)
  return true
}

export function cancelAll() {
  for (const h of handles.values()) {
    try { h.cancel() } catch { /* ignore */ }
  }
  handles.clear()
}

/** @param {string} tabId @returns {boolean} */
export function isExecuting(tabId) {
  return handles.has(tabId)
}

export function activeCount() { return handles.size }

/** @param {(tabId:string)=>void} fn */
export function onCompletion(fn) {
  completionListeners.add(fn)
  return () => completionListeners.delete(fn)
}

/** Test seam — reset state between tests. */
export function _resetForTests() {
  handles.clear()
  completionListeners.clear()
}

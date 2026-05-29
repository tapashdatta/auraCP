// Lightweight toast bus for the row grid. Exposes an append-only $state
// list of { id, level, text } items. The TableScreen renders them in a
// fixed-positioned stack at bottom-right of the route-outlet; each toast
// auto-dismisses after 4 seconds.
//
// a11y-10: each toast carries a severity-derived role + aria-live hint.
// `danger` toasts surface as role=alert / aria-live=assertive so screen
// readers preempt; everything else is role=status / polite.

let nextId = 1

/**
 * @typedef {{
 *   id:number,
 *   level:'info'|'success'|'warning'|'danger',
 *   text:string,
 *   role:'alert'|'status',
 *   ariaLive:'assertive'|'polite',
 * }} Toast
 */

export const toastBus = $state({
  /** @type {Toast[]} */
  items: [],
})

/**
 * @param {'info'|'success'|'warning'|'danger'} level
 * @param {string} text
 * @param {number} [ttl] ms
 */
export function pushToast(level, text, ttl = 4000) {
  const id = nextId++
  const isError = level === 'danger'
  /** @type {Toast} */
  const toast = {
    id, level, text,
    role: isError ? 'alert' : 'status',
    ariaLive: isError ? 'assertive' : 'polite',
  }
  toastBus.items = [...toastBus.items, toast]
  if (typeof setTimeout !== 'undefined') {
    setTimeout(() => {
      toastBus.items = toastBus.items.filter((t) => t.id !== id)
    }, ttl)
  }
  return id
}

export function dismissToast(id) {
  toastBus.items = toastBus.items.filter((t) => t.id !== id)
}

/**
 * Map an AuraDBError to a friendly toast text + level.
 *
 * WIRE-02: the backend ships kebab-case codes (see pkg/dbadmin/errors.go +
 * httpapi/errors.go). We also accept the legacy SCREAMING_SNAKE / snake_case
 * forms because PR #12's existing tests + some pre-WIRE-02 callers still
 * emit them. Normalization is a single pass to kebab-case before the lookup.
 *
 * @param {any} err
 * @returns {{level:'warning'|'danger'|'info', text:string}}
 */
export function toastFromError(err) {
  if (!err) return { level: 'danger', text: 'Unknown error' }
  const rawCode = err.code || ''
  const code = String(rawCode).toLowerCase().replace(/_/g, '-')
  const msg = err.message || 'Request failed'
  switch (code) {
    case 'no-primary-key':         return { level: 'warning', text: 'Table has no primary key — read-only' }
    case 'pk-mismatch':            return { level: 'warning', text: 'Row changed elsewhere — refresh to see the latest' }
    case 'conflict':               return { level: 'warning', text: 'Row changed elsewhere — refresh to see the latest' }
    case 'invalid-predicate':      return { level: 'warning', text: 'Filter expression is not valid for this column' }
    case 'row-cap-exceeded':       return { level: 'info',    text: 'Row cap exceeded — narrow filters or page through' }
    case 'empty-update':           return { level: 'info',    text: 'Nothing to update' }
    case 'unauthenticated':        return { level: 'danger',  text: 'Session expired' }
    case 'forbidden':              return { level: 'warning', text: 'You do not have permission for this action' }
    case 'not-found':              return { level: 'warning', text: 'Resource not found' }
    case 'backend-permission-denied': return { level: 'warning', text: 'Database user lacks permission for this action' }
    case 'invalid-input':          return { level: 'warning', text: msg || 'Invalid input' }
    default: return { level: 'danger', text: msg }
  }
}

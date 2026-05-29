// Connection list cache + tree expand-state. The tree component subscribes
// directly. Fixture is injected in tests; production calls api.listConnections.

import { api, AuraDBError } from './api.js'

/**
 * @typedef {object} Connection
 * @property {string} id
 * @property {string} name
 * @property {'postgres'|'mysql'|'sqlite'|'mssql'|'oracle'} engine
 * @property {string} [host]
 * @property {number} [port]
 * @property {string} [database]
 * @property {string} [username]
 * @property {boolean} [readOnly]
 * @property {string} [lastUsed]
 * @property {'ok'|'warn'|'down'|'idle'} [status]
 */

export const connections = $state({
  /** @type {Connection[]} */
  list: [],
  loading: false,
  /** @type {string|null} */
  error: null,
  /** @type {string|null} */
  selectedId: null,
  /** Map<connId, boolean> — true means the user has expanded that node. */
  /** @type {Record<string, boolean>} */
  expanded: {},
})

/** Load (or reload) connections from the API. */
export async function loadConnections() {
  connections.loading = true
  connections.error = null
  try {
    const data = await api.listConnections()
    connections.list = Array.isArray(data) ? data : (data?.items || [])
  } catch (err) {
    if (err instanceof AuraDBError) connections.error = err.message
    else connections.error = 'failed to load connections'
  } finally {
    connections.loading = false
  }
}

/**
 * Inject a fixture list (used by tests + the dev-no-backend path).
 * @param {Connection[]} list
 */
export function setConnections(list) {
  connections.list = list
}

/** @param {string} id */
export function toggleExpanded(id) {
  connections.expanded[id] = !connections.expanded[id]
}

/** @param {string} id */
export function isExpanded(id) {
  return !!connections.expanded[id]
}

/** @param {string} id */
export function selectConnection(id) {
  connections.selectedId = id
}

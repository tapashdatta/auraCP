// Per-connection schema cache used by the SQL editor's autocomplete
// provider. Lazily fetches listSchemas / listObjects / getTable on
// demand and memoizes the results for the session. No expiry: same
// connId reuses the cache; a manual `invalidate(connId)` purges it.

import { api } from '../api.js'

/**
 * @typedef {object} ConnCache
 * @property {string} engine
 * @property {string[]} schemas
 * @property {Map<string, any>} objectsBySchema  schema -> {tables,views,functions,procedures}
 * @property {Map<string, any>} columnsByTable   "schema.table" -> tableDTO
 * @property {Map<string, Promise<any>>} inflight
 * @property {number} fetchedAt
 */

/** @type {Map<string, ConnCache>} */
const cache = new Map()

/** @param {string} connId @returns {ConnCache} */
function ensure(connId) {
  let c = cache.get(connId)
  if (!c) {
    c = {
      engine: '',
      schemas: [],
      objectsBySchema: new Map(),
      columnsByTable: new Map(),
      inflight: new Map(),
      fetchedAt: 0,
    }
    cache.set(connId, c)
  }
  return c
}

/** @param {string} connId */
export function invalidate(connId) {
  cache.delete(connId)
}

/** @param {string} connId @param {string} engine */
export function setEngine(connId, engine) {
  ensure(connId).engine = engine
}

/** @param {string} connId */
export function getEngine(connId) {
  return ensure(connId).engine
}

/** @param {string} connId @returns {string[]} */
export function getSchemas(connId) {
  return ensure(connId).schemas.slice()
}

/** @param {string} connId */
export async function loadSchemas(connId) {
  const c = ensure(connId)
  const key = 'schemas'
  if (c.schemas.length > 0) return c.schemas
  if (c.inflight.has(key)) return c.inflight.get(key)
  const p = api.listSchemas(connId).then((r) => {
    const list = (r && Array.isArray(r.schemas)) ? r.schemas : (Array.isArray(r) ? r : [])
    c.schemas = list
    c.fetchedAt = Date.now()
    c.inflight.delete(key)
    return list
  }).catch((e) => { c.inflight.delete(key); throw e })
  c.inflight.set(key, p)
  return p
}

/** @param {string} connId @param {string} schema */
export async function loadObjects(connId, schema) {
  const c = ensure(connId)
  const key = 'objects:' + schema
  if (c.objectsBySchema.has(schema)) return c.objectsBySchema.get(schema)
  if (c.inflight.has(key)) return c.inflight.get(key)
  const p = api.listObjects(connId, schema).then((r) => {
    c.objectsBySchema.set(schema, r || { tables: [], views: [], functions: [], procedures: [] })
    c.inflight.delete(key)
    return c.objectsBySchema.get(schema)
  }).catch((e) => { c.inflight.delete(key); throw e })
  c.inflight.set(key, p)
  return p
}

/** @param {string} connId @param {string} schema @param {string} table */
export async function loadTable(connId, schema, table) {
  const c = ensure(connId)
  const key = schema + '.' + table
  const ikey = 'table:' + key
  if (c.columnsByTable.has(key)) return c.columnsByTable.get(key)
  if (c.inflight.has(ikey)) return c.inflight.get(ikey)
  const p = api.getTable(connId, schema, table).then((r) => {
    c.columnsByTable.set(key, r)
    c.inflight.delete(ikey)
    return r
  }).catch((e) => { c.inflight.delete(ikey); throw e })
  c.inflight.set(ikey, p)
  return p
}

/** Synchronous accessor (returns null if not yet loaded). */
export function peekObjects(connId, schema) {
  return ensure(connId).objectsBySchema.get(schema) || null
}

export function peekTable(connId, schema, table) {
  return ensure(connId).columnsByTable.get(schema + '.' + table) || null
}

/** Test seam — clear all entries. */
export function _resetForTests() {
  cache.clear()
}

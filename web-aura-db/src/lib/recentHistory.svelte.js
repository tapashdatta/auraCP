// Lazy per-session cache for the command-palette's "Recent history" and
// "Saved queries" sections. Primed on palette-open against the active
// connection; refreshed at most once per 30s to avoid spamming the API
// while the user re-opens the palette to fuzzy-search.

import { api } from './api.js'

export const historyCache = $state({
  /** @type {string} */
  connId: '',
  /** @type {Array<{id:any, sql:string, class?:string, durationMs?:number, executed?:string, connectionId?:string}>} */
  entries: [],
  /** @type {Array<{id:string, name:string, statement:string}>} */
  saved: [],
  loading: false,
  /** @type {number} */
  fetchedAt: 0,
})

const REFRESH_MS = 30_000

/**
 * Prime the cache for the given connection if it is stale (or the
 * connection changed since last prime).
 * @param {string} connId
 */
export async function primeHistoryCache(connId) {
  if (!connId) return
  const now = Date.now()
  if (historyCache.connId === connId && (now - historyCache.fetchedAt) < REFRESH_MS) return
  historyCache.connId = connId
  historyCache.loading = true
  try {
    const [hist, saved] = await Promise.allSettled([
      api.connHistory(connId),
      api.listSaved(connId),
    ])
    if (hist.status === 'fulfilled') {
      const r = hist.value
      const list = (r && Array.isArray(r.entries)) ? r.entries : []
      // Annotate with the conn id so palette commands replay into the
      // right editor (server-side connectionId is the truth, but we
      // fall back to the request conn id when missing).
      historyCache.entries = list.slice(0, 50).map((e) => ({
        ...e,
        connectionId: e.connectionId || connId,
      }))
    } else {
      historyCache.entries = []
    }
    if (saved.status === 'fulfilled') {
      historyCache.saved = Array.isArray(saved.value) ? saved.value : []
    } else {
      historyCache.saved = []
    }
    historyCache.fetchedAt = now
  } finally {
    historyCache.loading = false
  }
}

/** Test seam. */
export function _resetForTests() {
  historyCache.connId = ''
  historyCache.entries = []
  historyCache.saved = []
  historyCache.loading = false
  historyCache.fetchedAt = 0
}

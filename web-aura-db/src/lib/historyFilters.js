// Pure client-side filter pipeline for the history screen. Lives in its
// own module so vitest can exercise it without rendering the screen, and
// so the palette / future surfaces can reuse the same logic.

/**
 * @param {Array<any>} entries
 * @param {{search:string, dateRange:string, statusFilter:string, classFilter:string, starredOnly:boolean}} opts
 * @returns {Array<any>}
 */
export function applyFilters(entries, opts) {
  if (!Array.isArray(entries)) return []
  const cutoff = rangeCutoff(opts.dateRange)
  const q = (opts.search || '').trim().toLowerCase()
  const out = entries.filter((e) => {
    if (cutoff > 0) {
      const tt = Date.parse(String(e.executed || ''))
      if (!tt || tt < cutoff) return false
    }
    if (opts.classFilter && opts.classFilter !== 'all' && e.class !== opts.classFilter) return false
    if (opts.statusFilter === 'success' && e.error) return false
    if (opts.statusFilter === 'error' && !e.error) return false
    if (opts.starredOnly && !e.starred) return false
    if (q && !(e.sql || '').toLowerCase().includes(q)) return false
    return true
  })
  out.sort((a, b) => {
    const ta = Date.parse(String(a.executed || '')) || 0
    const tb = Date.parse(String(b.executed || '')) || 0
    return tb - ta
  })
  return out
}

/** @param {string} r */
export function rangeCutoff(r) {
  switch (r) {
    case '1h':  return Date.now() - 60 * 60 * 1000
    case '24h': return Date.now() - 24 * 60 * 60 * 1000
    case '7d':  return Date.now() - 7 * 24 * 60 * 60 * 1000
    case '30d': return Date.now() - 30 * 24 * 60 * 60 * 1000
    default:    return 0
  }
}

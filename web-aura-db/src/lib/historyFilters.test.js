import { describe, it, expect } from 'vitest'
import { applyFilters, rangeCutoff } from './historyFilters.js'

function entry(o = {}) {
  return {
    id: o.id ?? Math.random(),
    sql: 'SELECT 1',
    class: 'read',
    durationMs: 10,
    executed: new Date().toISOString(),
    connectionId: 'a',
    starred: false,
    error: '',
    ...o,
  }
}

describe('rangeCutoff', () => {
  it('returns 0 for "all"', () => {
    expect(rangeCutoff('all')).toBe(0)
  })
  it('returns a Unix ms in the past for 1h/24h/7d/30d', () => {
    const now = Date.now()
    expect(rangeCutoff('1h')).toBeLessThan(now)
    expect(rangeCutoff('24h')).toBeLessThan(rangeCutoff('1h'))
    expect(rangeCutoff('7d')).toBeLessThan(rangeCutoff('24h'))
    expect(rangeCutoff('30d')).toBeLessThan(rangeCutoff('7d'))
  })
})

describe('applyFilters', () => {
  const defaults = { search: '', dateRange: 'all', statusFilter: 'all', classFilter: 'all', starredOnly: false }

  it('returns [] for non-array input', () => {
    expect(applyFilters(null, defaults)).toEqual([])
    expect(applyFilters(undefined, defaults)).toEqual([])
  })

  it('preserves entries when no filters narrow', () => {
    const list = [entry({ id: 1 }), entry({ id: 2 })]
    const out = applyFilters(list, defaults)
    expect(out).toHaveLength(2)
  })

  it('filters by class', () => {
    const list = [entry({ id: 1, class: 'read' }), entry({ id: 2, class: 'write' })]
    const out = applyFilters(list, { ...defaults, classFilter: 'write' })
    expect(out).toHaveLength(1)
    expect(out[0].id).toBe(2)
  })

  it('filters by status success vs error', () => {
    const list = [entry({ id: 1, error: '' }), entry({ id: 2, error: 'boom' })]
    expect(applyFilters(list, { ...defaults, statusFilter: 'error' })).toHaveLength(1)
    expect(applyFilters(list, { ...defaults, statusFilter: 'success' })).toHaveLength(1)
  })

  it('filters starredOnly', () => {
    const list = [entry({ id: 1, starred: false }), entry({ id: 2, starred: true })]
    const out = applyFilters(list, { ...defaults, starredOnly: true })
    expect(out).toHaveLength(1)
    expect(out[0].id).toBe(2)
  })

  it('case-insensitive substring search across sql', () => {
    const list = [
      entry({ id: 1, sql: 'SELECT * FROM users' }),
      entry({ id: 2, sql: 'UPDATE orders' }),
    ]
    const out = applyFilters(list, { ...defaults, search: 'user' })
    expect(out).toHaveLength(1)
    expect(out[0].id).toBe(1)
  })

  it('respects dateRange cutoff', () => {
    const list = [
      entry({ id: 1, executed: new Date(Date.now() - 30 * 60 * 1000).toISOString() }),     // 30m old
      entry({ id: 2, executed: new Date(Date.now() - 5 * 60 * 60 * 1000).toISOString() }), // 5h old
    ]
    const out = applyFilters(list, { ...defaults, dateRange: '1h' })
    expect(out).toHaveLength(1)
    expect(out[0].id).toBe(1)
  })

  it('sorts entries by executed time descending', () => {
    const list = [
      entry({ id: 'older', executed: new Date(Date.now() - 60_000).toISOString() }),
      entry({ id: 'newer', executed: new Date().toISOString() }),
    ]
    const out = applyFilters(list, defaults)
    expect(out[0].id).toBe('newer')
    expect(out[1].id).toBe('older')
  })

  it('composes filters together', () => {
    const list = [
      entry({ id: 1, class: 'read', starred: true, error: '', sql: 'SELECT a' }),
      entry({ id: 2, class: 'read', starred: false, error: '', sql: 'SELECT b' }),
      entry({ id: 3, class: 'write', starred: true, error: 'x', sql: 'SELECT c' }),
    ]
    const out = applyFilters(list, { ...defaults, classFilter: 'read', starredOnly: true, statusFilter: 'success', search: 'a' })
    expect(out).toHaveLength(1)
    expect(out[0].id).toBe(1)
  })
})

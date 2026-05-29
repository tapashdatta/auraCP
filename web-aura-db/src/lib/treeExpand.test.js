import { describe, it, expect } from 'vitest'
import { toggle, isOpen, collapseMany, filterConnections, treeKeyAction } from './treeExpand.js'

describe('tree expand-state helpers', () => {
  it('toggle flips an entry without mutating the source', () => {
    const a = {}
    const b = toggle(a, 'c1')
    expect(a).toEqual({})              // input untouched
    expect(b).toEqual({ c1: true })
    const c = toggle(b, 'c1')
    expect(c).toEqual({ c1: false })
    expect(isOpen(b, 'c1')).toBe(true)
    expect(isOpen(c, 'c1')).toBe(false)
  })

  it('collapseMany removes every listed id', () => {
    const m = { c1: true, c2: true, c3: false }
    const out = collapseMany(m, ['c1', 'c2'])
    expect(out).toEqual({ c3: false })
  })

  it('filterConnections matches name, host, and engine case-insensitively', () => {
    const list = [
      { name: 'Prod', host: 'db.internal', engine: 'postgres' },
      { name: 'Staging', host: 'stg.db', engine: 'mysql' },
      { name: 'Local', host: '127.0.0.1', engine: 'sqlite' },
    ]
    expect(filterConnections(list, '').map((c) => c.name)).toEqual(['Prod', 'Staging', 'Local'])
    expect(filterConnections(list, 'PROD').map((c) => c.name)).toEqual(['Prod'])
    expect(filterConnections(list, 'mysql').map((c) => c.name)).toEqual(['Staging'])
    expect(filterConnections(list, 'db').map((c) => c.name)).toEqual(['Prod', 'Staging'])
    expect(filterConnections(list, '127').map((c) => c.name)).toEqual(['Local'])
  })
})

// ------------------------------------------------------------------------
// FIX-10 — WAI-ARIA tree keyboard pattern.
// ------------------------------------------------------------------------
describe('treeKeyAction (FIX-10 LeftTree keyboard navigation)', () => {
  const rows = [
    { id: 'c1', kind: 'connection', expanded: true },
    { id: 'c1:s1', kind: 'schema' },
    { id: 'c2', kind: 'connection', expanded: false },
    { id: 'c3', kind: 'connection', expanded: false },
  ]

  it('ArrowDown moves focus forward', () => {
    expect(treeKeyAction(rows, 0, 'ArrowDown')).toEqual({ focus: 1 })
  })

  it('ArrowDown stops at the last row', () => {
    expect(treeKeyAction(rows, rows.length - 1, 'ArrowDown')).toEqual({ focus: rows.length - 1 })
  })

  it('ArrowUp moves focus backward', () => {
    expect(treeKeyAction(rows, 2, 'ArrowUp')).toEqual({ focus: 1 })
  })

  it('Home/End jump to first/last', () => {
    expect(treeKeyAction(rows, 2, 'Home')).toEqual({ focus: 0 })
    expect(treeKeyAction(rows, 0, 'End')).toEqual({ focus: rows.length - 1 })
  })

  it('ArrowRight on a collapsed connection toggles it open', () => {
    expect(treeKeyAction(rows, 2, 'ArrowRight')).toEqual({ toggle: 'c2' })
  })

  it('ArrowRight on an expanded connection moves into its first child', () => {
    expect(treeKeyAction(rows, 0, 'ArrowRight')).toEqual({ focus: 1 })
  })

  it('ArrowLeft on an expanded connection collapses it', () => {
    expect(treeKeyAction(rows, 0, 'ArrowLeft')).toEqual({ toggle: 'c1' })
  })

  it('ArrowLeft on a schema row jumps to the parent connection', () => {
    expect(treeKeyAction(rows, 1, 'ArrowLeft')).toEqual({ focus: 0 })
  })

  it('unhandled keys return null', () => {
    expect(treeKeyAction(rows, 0, 'PageDown')).toBeNull()
    expect(treeKeyAction(rows, 0, 'x')).toBeNull()
  })

  it('out-of-range indices return null', () => {
    expect(treeKeyAction(rows, -1, 'ArrowDown')).toBeNull()
    expect(treeKeyAction(rows, 99, 'ArrowDown')).toBeNull()
    expect(treeKeyAction([], 0, 'ArrowDown')).toBeNull()
  })
})

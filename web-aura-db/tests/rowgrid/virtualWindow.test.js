import { describe, it, expect } from 'vitest'
import { virtualWindow, ariaRowIndex } from '../../src/lib/rowgrid/virtualWindow.js'

describe('virtualWindow', () => {
  it('returns empty window when total is 0', () => {
    const w = virtualWindow({ scrollTop: 0, viewportH: 400, rowH: 24, total: 0 })
    expect(w.startIdx).toBe(0); expect(w.endIdx).toBe(0); expect(w.yOffset).toBe(0)
  })
  it('renders rows starting at scrollTop / rowH', () => {
    // viewport 400px, rowH 24 → visCount = 17
    const w = virtualWindow({ scrollTop: 240, viewportH: 400, rowH: 24, total: 10000, buffer: 4 })
    expect(w.startIdx).toBe(10 - 4) // first = 10, buffer 4
    expect(w.endIdx).toBe(10 + 17 + 4)
    expect(w.yOffset).toBe((10 - 4) * 24)
  })
  it('clamps endIdx at total', () => {
    const w = virtualWindow({ scrollTop: 99 * 24, viewportH: 400, rowH: 24, total: 100, buffer: 4 })
    expect(w.endIdx).toBe(100)
  })
  it('clamps startIdx at 0', () => {
    const w = virtualWindow({ scrollTop: 0, viewportH: 400, rowH: 24, total: 100, buffer: 8 })
    expect(w.startIdx).toBe(0)
    expect(w.yOffset).toBe(0)
  })
  it('handles bogus rowH gracefully', () => {
    const w = virtualWindow({ scrollTop: 0, viewportH: 400, rowH: 0, total: 100 })
    expect(w.endIdx).toBe(0)
  })
})

describe('ariaRowIndex', () => {
  it('first row on first page is row 3 (filter=1, header=2, data=3+)', () => {
    expect(ariaRowIndex({ idx: 0, offset: 0 })).toBe(3)
  })
  it('respects global page offset', () => {
    expect(ariaRowIndex({ idx: 0, offset: 100 })).toBe(103)
    expect(ariaRowIndex({ idx: 5, offset: 1000 })).toBe(1008)
  })
})

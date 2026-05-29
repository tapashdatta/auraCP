import { describe, it, expect } from 'vitest'
import { cycleSort, serializeSort } from '../../src/lib/rowgrid/sortCycle.js'

describe('cycleSort (single sort)', () => {
  it('empty → asc on first click', () => {
    expect(cycleSort([], 'id')).toEqual([{ col: 'id', dir: 'asc' }])
  })
  it('asc → desc on second click', () => {
    const next = cycleSort([{ col: 'id', dir: 'asc' }], 'id')
    expect(next).toEqual([{ col: 'id', dir: 'desc' }])
  })
  it('desc → none on third click', () => {
    const next = cycleSort([{ col: 'id', dir: 'desc' }], 'id')
    expect(next).toEqual([])
  })
  it('clicking a different col replaces existing single sort', () => {
    const next = cycleSort([{ col: 'id', dir: 'desc' }], 'name')
    expect(next).toEqual([{ col: 'name', dir: 'asc' }])
  })
})

describe('cycleSort (multi-sort via shift)', () => {
  it('appends a new column with asc', () => {
    const next = cycleSort([{ col: 'a', dir: 'asc' }], 'b', true)
    expect(next).toEqual([{ col: 'a', dir: 'asc' }, { col: 'b', dir: 'asc' }])
  })
  it('cycles an already-present column without disturbing others', () => {
    const start = [{ col: 'a', dir: 'asc' }, { col: 'b', dir: 'asc' }]
    const next1 = cycleSort(start, 'b', true)
    expect(next1).toEqual([{ col: 'a', dir: 'asc' }, { col: 'b', dir: 'desc' }])
    const next2 = cycleSort(next1, 'b', true)
    expect(next2).toEqual([{ col: 'a', dir: 'asc' }])
  })
})

describe('serializeSort', () => {
  it('encodes desc keys with a leading dash', () => {
    expect(serializeSort([{ col: 'a', dir: 'asc' }, { col: 'b', dir: 'desc' }])).toEqual(['a', '-b'])
  })
})

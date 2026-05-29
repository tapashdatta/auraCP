import { describe, it, expect } from 'vitest'
import { runPool } from '../../src/lib/rowgrid/promisePool.js'

describe('runPool — bounded concurrency', () => {
  it('processes every item in input order', async () => {
    const out = await runPool([1, 2, 3, 4], async (n) => n * 10, 2)
    expect(out.map((r) => r.ok && r.value)).toEqual([10, 20, 30, 40])
  })
  it('captures rejections per-item without aborting siblings', async () => {
    const out = await runPool([1, 2, 3], async (n) => {
      if (n === 2) throw new Error('boom')
      return n
    }, 2)
    expect(out[0]).toEqual({ ok: true, value: 1 })
    expect(out[1].ok).toBe(false)
    expect(out[2]).toEqual({ ok: true, value: 3 })
  })
  it('respects the concurrency cap', async () => {
    let active = 0, maxActive = 0
    await runPool([1, 2, 3, 4, 5, 6, 7, 8], async () => {
      active++
      if (active > maxActive) maxActive = active
      await new Promise((r) => setTimeout(r, 10))
      active--
    }, 3)
    expect(maxActive).toBeLessThanOrEqual(3)
  })
})

// Tiny bounded-concurrency promise pool used by bulk delete. ~30 LOC.
// Resolves with an array of { ok, value? , error? } results in input order.

/**
 * @template T, R
 * @param {T[]} items
 * @param {(item:T, idx:number)=>Promise<R>} worker
 * @param {number} [concurrency]
 * @returns {Promise<Array<{ok:true, value:R} | {ok:false, error:any}>>}
 */
export async function runPool(items, worker, concurrency = 8) {
  /** @type {Array<{ok:true,value:R}|{ok:false,error:any}>} */
  const out = new Array(items.length)
  let next = 0
  async function loop() {
    while (true) {
      const i = next++
      if (i >= items.length) return
      try {
        const r = await worker(items[i], i)
        out[i] = { ok: true, value: r }
      } catch (e) {
        out[i] = { ok: false, error: e }
      }
    }
  }
  const n = Math.max(1, Math.min(concurrency, items.length))
  await Promise.all(Array.from({ length: n }, () => loop()))
  return out
}

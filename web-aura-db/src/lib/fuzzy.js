// Hand-rolled subsequence matcher with positional bonuses. Modeled on
// the VSCode/Sublime simple matchers — intentionally NOT fzf-grade.
//
//   match(query, target) -> { score, positions } | null
//
// Scoring (additive):
//   +1 per matched character
//   +8 if the match is at index 0 of target
//   +6 if the match follows a non-alpha boundary (_-./ space)
//   +4 if the matched character is uppercase in the original target
//   +2 if the match is consecutive with the previous match
//   -1 per gap char between consecutive matches (encourages tight clusters)
// then a length-penalty of floor(targetLen / 20) is subtracted so shorter
// targets float to the top on ties.
//
// An empty query returns {score: 0, positions: []} for every target —
// callers handle the "no filter" case by sorting on recencyKey alone.

/**
 * @param {string} query
 * @param {string} target
 * @returns {{score:number, positions:number[]} | null}
 */
export function match(query, target) {
  if (typeof query !== 'string') return null
  if (typeof target !== 'string') return null
  if (query.length === 0) return { score: 0, positions: [] }
  const q = query.toLowerCase()
  const t = target.toLowerCase()
  /** @type {number[]} */
  const positions = []
  let score = 0
  let ti = 0
  let lastMatch = -2
  for (let qi = 0; qi < q.length; qi++) {
    const qc = q.charCodeAt(qi)
    let found = -1
    while (ti < t.length) {
      if (t.charCodeAt(ti) === qc) { found = ti; break }
      ti++
    }
    if (found < 0) return null
    positions.push(found)
    score += 1
    if (found === 0) score += 8
    else {
      const prev = target.charCodeAt(found - 1)
      // word-boundary chars
      if (prev === 32 /* space */ || prev === 95 /* _ */ || prev === 45 /* - */ || prev === 46 /* . */ || prev === 47 /* / */) {
        score += 6
      }
    }
    const original = target.charCodeAt(found)
    if (original >= 65 && original <= 90) score += 4
    if (found === lastMatch + 1) score += 2
    else if (lastMatch >= 0) score -= (found - lastMatch - 1)
    lastMatch = found
    ti = found + 1
  }
  score -= Math.floor(target.length / 20)
  return { score, positions }
}

/**
 * Highlight helper. Returns an array of {text, hi} chunks where `hi=true`
 * marks the slices that should be wrapped in <mark> by the renderer.
 *
 * @param {string} target
 * @param {number[]} positions
 * @returns {{text:string, hi:boolean}[]}
 */
export function highlight(target, positions) {
  if (!positions || positions.length === 0) return [{ text: target, hi: false }]
  /** @type {{text:string, hi:boolean}[]} */
  const out = []
  let cursor = 0
  for (let i = 0; i < positions.length; i++) {
    const p = positions[i]
    if (p > cursor) out.push({ text: target.slice(cursor, p), hi: false })
    // Coalesce consecutive positions into a single highlight run.
    let end = p + 1
    while (i + 1 < positions.length && positions[i + 1] === end) {
      end++
      i++
    }
    out.push({ text: target.slice(p, end), hi: true })
    cursor = end
  }
  if (cursor < target.length) out.push({ text: target.slice(cursor), hi: false })
  return out
}

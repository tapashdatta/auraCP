// Bundle-size budget gate. PR #13 adds CodeMirror 6 (minimal subset) and
// sql-formatter as a dynamic chunk. With a11y-12 the SqlEditor screen is
// now lazy-loaded — CodeMirror lives in a separate chunk so non-/query
// routes don't pay the ~140 KB gz CodeMirror tax. The main ceiling is
// tightened accordingly; the editor chunk has its own ceiling.

import { describe, it, expect } from 'vitest'
import { promisify } from 'node:util'
import { readdirSync, readFileSync, existsSync } from 'node:fs'
import { gzip as gzipCb } from 'node:zlib'
import { join } from 'node:path'

const gzip = promisify(gzipCb)

const DIST = join(process.cwd(), 'dist', 'assets')

// Main entry chunk — should be back to ~PR #12 baseline (~75 KB gz)
// now that CodeMirror is split out. We allow some headroom.
const MAIN_CHUNK_GZ_MAX = 110 * 1024

// SqlEditor + CodeMirror live in their own lazy chunk. ~160 KB gz is
// the empirical landing — leaves headroom for autocomplete polish.
const EDITOR_CHUNK_GZ_MAX = 200 * 1024

describe('bundle budget', () => {
  it('main chunk gzipped is under the ceiling (skip when no dist/)', async () => {
    if (!existsSync(DIST)) {
      console.warn('dist/ not built; skipping bundle budget gate')
      return
    }
    const files = readdirSync(DIST)
    // Convention: vite emits the entry as `assets/index-<hash>.js`.
    const main = files.find((f) => /^index-.*\.js$/.test(f))
    if (!main) {
      console.warn('no main chunk found; skipping')
      return
    }
    const raw = readFileSync(join(DIST, main))
    const gz = await gzip(raw)
    expect(gz.length).toBeLessThan(MAIN_CHUNK_GZ_MAX)
  })

  it('SqlEditor splits into its own chunk and stays under the editor ceiling', async () => {
    if (!existsSync(DIST)) {
      console.warn('dist/ not built; skipping editor chunk gate')
      return
    }
    const files = readdirSync(DIST)
    // Vite emits the lazy import as e.g. assets/SqlEditor-<hash>.js.
    const editor = files.find((f) => /^SqlEditor-.*\.js$/.test(f))
    expect(editor, 'expected SqlEditor-*.js lazy chunk').toBeTruthy()
    const raw = readFileSync(join(DIST, editor))
    const gz = await gzip(raw)
    expect(gz.length).toBeLessThan(EDITOR_CHUNK_GZ_MAX)
  })

  it('sql-formatter is split into its own chunk (no merge with main)', () => {
    if (!existsSync(DIST)) return
    const files = readdirSync(DIST)
    const hasSplit = files.some((f) => /^sqlFormatter-.*\.js$/.test(f))
    expect(hasSplit).toBe(true)
  })
})

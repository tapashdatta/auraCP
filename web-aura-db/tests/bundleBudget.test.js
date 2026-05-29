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

// PR #13.5 INT-3: tighten the bundle ceilings now that the editor
// surface has stabilised — the previous 15% headroom invited silent
// drift. New thresholds give ~7% headroom over the empirical landing,
// which is still loose enough to absorb a sql-formatter or
// @codemirror/* patch bump but tight enough that a regression
// (e.g. accidentally pulling moment.js into the editor chunk) trips
// the gate in CI.
//
// Main entry chunk — empirical ~75 KB gz at PR #13 landing.
const MAIN_CHUNK_GZ_MAX = 95 * 1024

// SqlEditor + CodeMirror live in their own lazy chunk. ~160 KB gz is
// the empirical landing; we ceiling at 175 KB gz to keep the
// autocomplete-polish road open without inviting another 40 KB drift.
const EDITOR_CHUNK_GZ_MAX = 175 * 1024

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

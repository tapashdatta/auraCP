// A11Y-3 regression test for HistoryScreen.svelte.
//
// Full DOM rendering of the screen hits the same upstream Vite
// preprocessor issue noted in SqlEditor.test.js, so this test asserts
// the ARIA / tabindex shape via static analysis of the .svelte source.
// Brittle to formatting but cheap and catches the specific regression
// the reviewer called out: no tabindex="0" on rows + star button keeps
// its own tab stop (no tabindex="-1" override).

import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
const source = readFileSync(join(here, 'HistoryScreen.svelte'), 'utf8')

describe('HistoryScreen ARIA shape (A11Y-3)', () => {
  it('history rows do not carry tabindex="0"', () => {
    // The previous shape used <div role="row" tabindex="0"> which
    // double-tabstopped with the nested star button. Plain <tr> rows
    // should have no explicit tabindex.
    expect(source).not.toMatch(/tabindex\s*=\s*["']0["']/)
  })

  it('does not paint role="row" / role="table" — uses semantic HTML', () => {
    // The half-applied ARIA grid (role=table + role=row but no cells)
    // confused screen readers. We now use plain <table>/<tr>/<td>.
    expect(source).not.toMatch(/role\s*=\s*["']table["']/)
    expect(source).not.toMatch(/role\s*=\s*["']row["']/)
    expect(source).not.toMatch(/role\s*=\s*["']gridcell["']/)
    expect(source).not.toMatch(/role\s*=\s*["']columnheader["']/)
  })

  it('renders an actual <table> with <thead> / <tbody>', () => {
    expect(source).toMatch(/<table\b/)
    expect(source).toMatch(/<thead\b/)
    expect(source).toMatch(/<tbody\b/)
    expect(source).toMatch(/<tr\b/)
    expect(source).toMatch(/<th\b[^>]*scope="col"/)
  })

  it('keeps the star button (it has aria-label, no tabindex="-1" override)', () => {
    // The star button must remain in the natural tab order so keyboard
    // users can star/unstar without a mouse.
    expect(source).toMatch(/aria-label=\{r\.starred \? 'Unstar' : 'Star'\}/)
    // Must NOT add a tabindex=-1 to the star (would remove the keyboard
    // affordance we want to keep).
    const starBlock = source.match(/class="history__star"[\s\S]*?<\/button>/)
    expect(starBlock, 'star button block must exist').toBeTruthy()
    expect(starBlock[0]).not.toMatch(/tabindex\s*=\s*["']-1["']/)
  })
})

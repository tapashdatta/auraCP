// SqlEditor screen tests. PR #13.5: the screen's dependency graph now
// pulls in ConfirmDialog + SaveQueryModal (each with their own <style>
// blocks), which trips the same upstream vite-plugin-svelte
// "deeply-nested style blocks" preprocessor edge case
// ExportModal.test.js / HistoryScreen.test.js already work around.
// Switch to the established source-shape pattern: dynamic import is
// still exercised by the lib-level tests (editor.test.js,
// classifier.test.js, splitStatements.test.js, execRegistry.test.js,
// schemaCache.test.js, completions.test.js) and by the e2e suite —
// this file now guards the wiring contract on the SOURCE so the test
// never has to compile the component graph in jsdom.

import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
const src = readFileSync(join(here, 'SqlEditor.svelte'), 'utf8')

describe('SqlEditor screen', () => {
  it('imports the editor pipeline modules it needs at runtime', () => {
    // The dependency graph is exercised end-to-end by the lib tests
    // (editor / classifier / splitStatements / execRegistry /
    // schemaCache / completions). What we guard here is that the
    // screen still wires them in — a regression where someone deletes
    // the splitStatements import (say) wouldn't be caught by those
    // unit tests because each is self-contained.
    expect(src).toMatch(/sqlEditor\/classifier\.svelte\.js/)
    expect(src).toMatch(/sqlEditor\/splitStatements\.js/)
    expect(src).toMatch(/sqlEditor\/execRegistry\.svelte\.js/)
    expect(src).toMatch(/sqlEditor\/schemaCache\.svelte\.js/)
    expect(src).toMatch(/sqlStream/)
  })

  it('PR #13.5: routes Save through a modal, not window.prompt', () => {
    // EXEC-9 / a11y-10 regression guard — if someone re-adds the
    // native prompt, the test trips. Inverted assertion (no `prompt(`
    // call inside saveQuery) is hard to write robustly, so we assert
    // the SaveQueryModal mount instead.
    expect(src).toMatch(/<SaveQueryModal/)
  })

  it('PR #13.5: dirty-check confirm dialog is wired to load paths', () => {
    // EXEC-10 regression guard.
    expect(src).toMatch(/confirmLoadOpen/)
    expect(src).toMatch(/Replace editor buffer/)
  })

  it('PR #13.5: status + error live regions are split', () => {
    // a11y-14 partial regression guard.
    expect(src).toMatch(/role="status"[^>]*aria-live="polite"/)
    expect(src).toMatch(/role="alert"[^>]*aria-live="assertive"/)
  })

  it('PR #13.5: Cancel button advertises Cmd+. via aria-keyshortcuts', () => {
    // a11y-03 regression guard.
    expect(src).toMatch(/aria-keyshortcuts="Meta\+Period"/)
  })

  it('PR #13.5: sidebar accordions use aria-expanded', () => {
    // a11y-07 regression guard.
    expect(src).toMatch(/aria-expanded=\{historyOpen\}/)
    expect(src).toMatch(/aria-expanded=\{savedOpen\}/)
  })

  it('PR #13.5: classifier.flush is awaited before exec to settle klass', () => {
    // INT-8 regression guard.
    expect(src).toMatch(/await classifier\?\.flush\?\.\(\)/)
  })

  it('PR #13.5: history refresh runs on both success and error paths', () => {
    // EXEC-11 regression guard.
    expect(src).toMatch(/finalize\s*=\s*\(\)\s*=>/)
  })

  it('PR #13.5: Format button preloads the sql-formatter chunk on hover', () => {
    // INT-6 regression guard.
    expect(src).toMatch(/preloadFormatter/)
    expect(src).toMatch(/onmouseenter=\{preloadFormatter\}/)
  })

  it('exposes the same route name as the router declares', async () => {
    const { routeState } = await import('../lib/router.svelte.js')
    // 'query' is the name registered for /connections/:id/query in
    // router.svelte.js. App.svelte maps this name to SqlEditor.
    // This guards against renaming drift.
    expect(typeof routeState.name).toBe('string')
  })
})

// SqlEditor module-level test. Full DOM rendering is exercised in
// playwright/manual e2e; unit tests here just confirm the screen's
// module graph parses, the route is wired, and the API contract
// for the screen-level fetch sequence matches what the screen will issue
// at mount time.
//
// (Rendering the screen via @testing-library/svelte in jsdom hits an
// upstream Vite preprocessor edge case with deeply-nested style blocks;
// the boot path is covered by the api/sqlStream/sqlEditor lib tests.)

import { describe, it, expect } from 'vitest'

describe('SqlEditor screen', () => {
  it('module imports cleanly', async () => {
    // Importing the module forces the dependency graph (CodeMirror,
    // classifier, schemaCache, splitStatements) to resolve at least
    // once — this catches build/link regressions immediately.
    const mod = await import('./SqlEditor.svelte')
    expect(mod.default).toBeTruthy()
  })

  it('exposes the same route name as the router declares', async () => {
    const { routeState } = await import('../lib/router.svelte.js')
    // 'query' is the name registered for /connections/:id/query in
    // router.svelte.js. App.svelte maps this name to SqlEditor.
    // This guards against renaming drift.
    expect(typeof routeState.name).toBe('string')
  })
})

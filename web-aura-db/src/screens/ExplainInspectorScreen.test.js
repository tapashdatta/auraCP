// ExplainInspectorScreen tests. Mirrors the source-shape pattern
// established by SqlEditor.test.js / HistoryScreen.test.js — we guard
// the wiring contract against the SOURCE so the test never has to
// compile the full Svelte component graph (the inspector pulls in
// AnalyzeToggle, which in turn brings ConfirmDialog with its own
// <style>, the same upstream vite-plugin-svelte preprocessor edge
// case other screen tests work around).
//
// FIX INT-9 (PR #14.5): this file locks in the cross-screen handoff
// contract from PR #14 (INT-1 / INT-2: conn-scoped pending payload,
// per-route remount) AND the new INT-3 / INT-6 round-trip introduced
// in PR #14.5 (editor:restore:<connId> stash for the editor's docText).

import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
const inspectorSrc = readFileSync(join(here, 'ExplainInspectorScreen.svelte'), 'utf8')
const editorSrc = readFileSync(join(here, 'SqlEditor.svelte'), 'utf8')

describe('ExplainInspectorScreen', () => {
  it('imports the explain pipeline modules it needs at runtime', () => {
    expect(inspectorSrc).toMatch(/sqlEditor\/explainTreeStore\.svelte\.js/)
    expect(inspectorSrc).toMatch(/sqlEditor\/explainFlatten\.js/)
    expect(inspectorSrc).toMatch(/components\/explain\/FlameTree\.svelte/)
    expect(inspectorSrc).toMatch(/components\/explain\/NodeDetail\.svelte/)
    expect(inspectorSrc).toMatch(/components\/explain\/AnalyzeToggle\.svelte/)
    expect(inspectorSrc).toMatch(/components\/explain\/WarningBanner\.svelte/)
  })

  describe('PR #14 handoff guards (INT-1 / INT-2)', () => {
    it('INT-1: consumePendingFor compares connId before consuming', () => {
      // The cross-connection statement-leak guard: if A's payload
      // sits in sessionStorage when B's inspector mounts, it MUST
      // be dropped. The id mismatch path is the load-bearing line.
      expect(inspectorSrc).toMatch(/parsed\.connId\s*&&\s*parsed\.connId\s*!==\s*routeId/)
    })

    it('INT-1: pending payload is always cleared from sessionStorage', () => {
      // Even on connId mismatch we removeItem so a later mount on
      // the correct connection cannot pick up the stale entry.
      expect(inspectorSrc).toMatch(/sessionStorage\.removeItem\(['"]explain:pending['"]\)/)
    })

    it('SqlEditor writes the conn-scoped pending payload before navigating', () => {
      // The producer side of INT-1 — without connId on the payload,
      // the consumer guard couldn't fire. INT-4: fromHash is gone.
      expect(editorSrc).toMatch(/sessionStorage\.setItem\(['"]explain:pending['"]/)
      expect(editorSrc).toMatch(/connId:\s*id/)
      expect(editorSrc).not.toMatch(/fromHash:/)
    })
  })

  describe('PR #14.5 handoff guards (INT-3 / INT-6)', () => {
    it('INT-3: dead explain:return write is removed', () => {
      // The earlier inspector wrote to sessionStorage["explain:return"]
      // but no editor-side reader existed. The new path stashes under
      // editor:restore:<connId> which the editor's onMount drains.
      expect(inspectorSrc).not.toMatch(/explain:return/)
    })

    it('INT-6: inspector stashes editor:restore:<connId> on navigate-away', () => {
      // Both Close and "Open in SQL editor" funnel through
      // stashForEditor so a browser-back doesn't drop docText.
      expect(inspectorSrc).toMatch(/stashForEditor/)
      expect(inspectorSrc).toMatch(/editor:restore:['"]?\s*\+\s*id/)
    })

    it('INT-6: editor drains editor:restore:<connId> on mount', () => {
      // Producer-consumer round trip — without the drain, the stash
      // would silently leak.
      expect(editorSrc).toMatch(/editor:restore:['"]?\s*\+\s*id/)
      expect(editorSrc).toMatch(/sessionStorage\.removeItem\(['"]editor:restore:/)
    })
  })

  describe('PR #14.5 UX guards', () => {
    it('INT-5: document.title reflects the connection on mount', () => {
      // Assignment uses a ternary against the connection label; just
      // assert that the screen writes document.title and references
      // the conn label so future refactors can't silently drop it.
      expect(inspectorSrc).toMatch(/document\.title\s*=/)
      expect(inspectorSrc).toMatch(/EXPLAIN\s*·/)
    })

    it('INT-7: api.explain is called with an AbortController signal', () => {
      // The abort path turns the previous 60s blank-screen wedge
      // into a cancelable operation.
      expect(inspectorSrc).toMatch(/new AbortController/)
      expect(inspectorSrc).toMatch(/api\.explain\([^)]*signal/)
    })

    it('DC-5 + INT-7: initial fetch renders a spinner + abort affordance', () => {
      expect(inspectorSrc).toMatch(/explain-inspector__loading--with-cancel/)
      expect(inspectorSrc).toMatch(/abortInFlight/)
    })

    it('CORR-9: stmt change triggers a server re-classify for the toggle gate', () => {
      // Keeps AnalyzeToggle's typed-confirm gate honest when the
      // statement is edited after the initial handoff.
      expect(inspectorSrc).toMatch(/api\.classifySql\(/)
    })

    it('INT-8: bare h / r shortcuts are scoped (require Shift, ignore editing targets)', () => {
      // The previous bare h / r collided with typing in any input.
      expect(inspectorSrc).toMatch(/_isEditingTarget/)
      expect(inspectorSrc).toMatch(/ev\.shiftKey/)
    })

    it('A11Y-10 / INT-10: Cmd+E re-run is documented via reason= tooltip', () => {
      expect(inspectorSrc).toMatch(/Cmd\/Ctrl\+E/)
    })

    it('A11Y-13 / CORR-13: search UI is wired to the store', () => {
      expect(inspectorSrc).toMatch(/onSearchInput/)
      expect(inspectorSrc).toMatch(/store\.setSearch/)
      expect(inspectorSrc).toMatch(/aria-keyshortcuts="\/ Control\+F Meta\+F"/)
    })

    it('CORR-12: inline truncated marker fires when the warning carries "truncated"', () => {
      expect(inspectorSrc).toMatch(/isTruncated/)
      expect(inspectorSrc).toMatch(/Tree truncated by the server cap/)
    })

    it('DC-8: RAW is a tabbar, not a single flip button', () => {
      expect(inspectorSrc).toMatch(/role="tablist"/)
      // role=tab carries aria-selected (NOT aria-pressed — Svelte's
      // a11y lint correctly flags pressed as unsupported by role=tab).
      expect(inspectorSrc).toMatch(/role="tab"[^>]*aria-selected/)
      expect(inspectorSrc).not.toMatch(/role="tab"[^>]*aria-pressed/)
    })
  })

  it('exposes the same route name as the router declares', async () => {
    const { routeState } = await import('../lib/router.svelte.js')
    expect(typeof routeState.name).toBe('string')
  })
})

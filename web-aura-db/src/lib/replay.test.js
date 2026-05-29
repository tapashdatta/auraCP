import { describe, it, expect, beforeEach, vi } from 'vitest'

// We have to stub the router import before importing replay.js (it does a
// side-effect-free import of navigate, but we want to assert it gets the
// right path).
vi.mock('./router.svelte.js', () => ({
  navigate: vi.fn(),
}))

import { replayInEditor, consumePending, _internals } from './replay.js'
import { navigate } from './router.svelte.js'

beforeEach(() => {
  sessionStorage.clear()
  vi.clearAllMocks()
})

describe('replayInEditor', () => {
  it('writes the editor:pending slot with connId, statement, and ts', () => {
    replayInEditor('conn-a', 'SELECT 1')
    const raw = sessionStorage.getItem(_internals.KEY)
    expect(raw).toBeTruthy()
    const parsed = JSON.parse(raw)
    expect(parsed.connId).toBe('conn-a')
    expect(parsed.statement).toBe('SELECT 1')
    expect(typeof parsed.ts).toBe('number')
    expect(Date.now() - parsed.ts).toBeLessThan(1000)
  })

  it('navigates to /connections/{id}/query', () => {
    replayInEditor('conn-a', 'SELECT 1')
    expect(navigate).toHaveBeenCalledWith('/connections/conn-a/query')
  })

  it('url-encodes the connection id', () => {
    replayInEditor('a b/c', 'SELECT 1')
    expect(navigate).toHaveBeenCalledWith('/connections/a%20b%2Fc/query')
  })

  it('is a no-op for missing connId or statement', () => {
    replayInEditor('', 'SELECT 1')
    replayInEditor('conn-a', '')
    expect(sessionStorage.getItem(_internals.KEY)).toBeNull()
    expect(navigate).not.toHaveBeenCalled()
  })
})

describe('editor pending tick (C1)', () => {
  it('replayInEditor bumps the registered tick function', async () => {
    // Importing palette.svelte.js wires its bumpEditorPendingTick into
    // replay.js via setEditorPendingBumper(...) at module-load time.
    const palette = await import('./palette.svelte.js')
    const before = palette.editorPending.tick
    replayInEditor('conn-a', 'SELECT 1')
    expect(palette.editorPending.tick).toBe(before + 1)
    replayInEditor('conn-a', 'SELECT 2')
    expect(palette.editorPending.tick).toBe(before + 2)
  })

  it('no-op replayInEditor calls do not bump the tick', async () => {
    const palette = await import('./palette.svelte.js')
    const before = palette.editorPending.tick
    replayInEditor('', 'SELECT 1')
    replayInEditor('conn-a', '')
    expect(palette.editorPending.tick).toBe(before)
  })
})

describe('consumePending', () => {
  it('returns the payload when conn matches and not stale', () => {
    replayInEditor('conn-a', 'SELECT 1')
    const got = consumePending('conn-a')
    expect(got).not.toBeNull()
    expect(got.statement).toBe('SELECT 1')
    // and clears the slot
    expect(sessionStorage.getItem(_internals.KEY)).toBeNull()
  })

  it('returns null and drops the slot when conn mismatches', () => {
    replayInEditor('conn-a', 'SELECT 1')
    const got = consumePending('conn-b')
    expect(got).toBeNull()
    // slot is cleared even on rejection (avoid stale entries firing later)
    expect(sessionStorage.getItem(_internals.KEY)).toBeNull()
  })

  it('returns null when payload is older than MAX_AGE_MS', () => {
    const stale = { connId: 'conn-a', statement: 'X', ts: Date.now() - _internals.MAX_AGE_MS - 1 }
    sessionStorage.setItem(_internals.KEY, JSON.stringify(stale))
    expect(consumePending('conn-a')).toBeNull()
  })

  it('returns null when slot is malformed JSON', () => {
    sessionStorage.setItem(_internals.KEY, '{this is not json')
    expect(consumePending('conn-a')).toBeNull()
  })

  it('returns null when slot is missing', () => {
    expect(consumePending('conn-a')).toBeNull()
  })
})

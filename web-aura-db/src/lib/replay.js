// Cross-route handoff for "replay this SQL in the editor". Mirrors PR #14's
// `explain:pending` pattern so the convention is consistent.
//
//   replayInEditor(connId, sql, {newTab})
//     → writes { connId, statement, ts } into sessionStorage['editor:pending']
//     → navigates to /connections/{id}/query (or opens a new tab when
//       newTab=true so Cmd+Enter from the palette can fan out without
//       blowing away the current editor's buffer)
//
// SqlEditor.svelte's onMount reads the slot, validates ts (< 30s) and
// connId match, then funnels through its existing loadIntoEditor() path
// before clearing the slot. Stale/mismatched entries are dropped silently.

import { navigate } from './router.svelte.js'

const KEY = 'editor:pending'
const MAX_AGE_MS = 30_000

// PR #15 follow-up (C1): same-tab same-connection replay from the palette
// doesn't trigger SqlEditor.onMount (the screen is already mounted), so
// sessionStorage alone can't reach the live editor. A small reactive bus
// (a tick counter incremented on each replayInEditor call) lets the
// SqlEditor consume the pending payload via a $effect that watches the
// tick. Cross-tab / cross-mount cases still go through onMount +
// consumePending(); the tick path is purely additive.
//
// The tick cell lives in palette.svelte.js (runes can only be used in
// .svelte.js files). To avoid circular imports we let palette.svelte.js
// register its bump function here at module-load time.
/** @type {() => void} */
let _bumpEditorPendingTick = () => {}
/** @param {() => void} fn */
export function setEditorPendingBumper(fn) {
  if (typeof fn === 'function') _bumpEditorPendingTick = fn
}

/**
 * @param {string} connId
 * @param {string} statement
 * @param {{newTab?: boolean}} [opts]
 */
export function replayInEditor(connId, statement, opts = {}) {
  if (!connId || !statement) return
  try {
    sessionStorage.setItem(KEY, JSON.stringify({
      connId,
      statement,
      ts: Date.now(),
    }))
  } catch { /* private mode / quota — fall through */ }
  // C1: bump the reactive tick so an already-mounted SqlEditor for the
  // same connection picks up the new payload (onMount won't fire again).
  try { _bumpEditorPendingTick() } catch { /* ignore */ }
  const path = `/connections/${encodeURIComponent(connId)}/query`
  if (opts.newTab && typeof window !== 'undefined') {
    window.open(`#${path}`, '_blank')
  } else {
    navigate(path)
  }
}

/**
 * Consume the pending handoff. Returns the payload when it is valid for
 * the given target connection and not stale; otherwise null. The slot is
 * cleared on every call (success or rejection) so a stale value can't
 * fire on a subsequent navigation.
 *
 * @param {string} expectedConnId
 * @returns {{connId:string, statement:string, ts:number} | null}
 */
export function consumePending(expectedConnId) {
  if (typeof sessionStorage === 'undefined') return null
  let raw = ''
  try { raw = sessionStorage.getItem(KEY) || '' } catch { return null }
  if (!raw) return null
  try { sessionStorage.removeItem(KEY) } catch { /* ignore */ }
  let parsed = null
  try { parsed = JSON.parse(raw) } catch { return null }
  if (!parsed || typeof parsed !== 'object') return null
  if (typeof parsed.connId !== 'string' || typeof parsed.statement !== 'string') return null
  if (typeof parsed.ts !== 'number') return null
  if (Date.now() - parsed.ts > MAX_AGE_MS) return null
  if (expectedConnId && parsed.connId !== expectedConnId) return null
  return parsed
}

// Exported for tests.
export const _internals = { KEY, MAX_AGE_MS }

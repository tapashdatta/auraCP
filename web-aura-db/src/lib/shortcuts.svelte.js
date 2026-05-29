// Global keyboard shortcuts. PR #11 wires Cmd-W (close tab); PR #15 adds:
//   Cmd/Ctrl-K        → toggle the command palette (Raycast/Linear convention)
//   Cmd/Ctrl-Shift-K  → focus the tree filter input (relocated from Cmd-K)
//   Cmd/Ctrl-G        → /history
//   /                  → open palette pre-filtered to actions
//   ?                  → open keyboard cheatsheet (PR #13)
// In-editor CodeMirror chords (Cmd-Enter, Cmd-., Cmd-S, …) live inside
// CodeMirrorPane and are NOT routed through this file.

import { workspaces, closeTab } from './workspaces.svelte.js'
import { palette, openPalette, togglePalette, closePalette } from './palette.svelte.js'
import { navigate } from './router.svelte.js'

/** @type {Set<(ev:KeyboardEvent)=>void>} */
const listeners = new Set()

/** @param {(ev:KeyboardEvent)=>void} fn */
export function onShortcut(fn) {
  listeners.add(fn)
  return () => listeners.delete(fn)
}

function isMac() {
  return typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform)
}

function isTextEditingTarget(ev) {
  const t = /** @type {HTMLElement|null} */ (ev.target)
  if (!t) return false
  const tag = (t.tagName || '').toLowerCase()
  if (tag === 'input' || tag === 'textarea' || tag === 'select') return true
  if (t.isContentEditable) return true
  // CodeMirror sets contenteditable on its inner editor; covered above.
  return false
}

function handler(ev) {
  const mod = isMac() ? ev.metaKey : ev.ctrlKey

  // Cmd/Ctrl-K → toggle the command palette. Wins over the old tree-
  // filter focus shortcut (which moved to Cmd-Shift-K).
  if (mod && !ev.shiftKey && (ev.key === 'k' || ev.key === 'K')) {
    ev.preventDefault()
    togglePalette()
    return
  }
  // Cmd/Ctrl-Shift-K → focus the tree filter (relocated from Cmd-K).
  if (mod && ev.shiftKey && (ev.key === 'k' || ev.key === 'K')) {
    ev.preventDefault()
    const el = document.querySelector('[data-tree-filter]')
    if (el && el instanceof HTMLInputElement) el.focus()
    return
  }
  // Cmd/Ctrl-G → /history. Skip when typing into an input so cmd-g for
  // browser-find doesn't get hijacked inside CodeMirror.
  if (mod && (ev.key === 'g' || ev.key === 'G')) {
    if (!isTextEditingTarget(ev)) {
      ev.preventDefault()
      navigate('/history')
      return
    }
  }
  // "/" → open palette in actions-only mode (GitHub convention).
  if (!mod && ev.key === '/' && !isTextEditingTarget(ev) && !palette.open) {
    ev.preventDefault()
    openPalette('/')
    return
  }
  if (mod && (ev.key === 'w' || ev.key === 'W')) {
    if (workspaces.activeId) {
      ev.preventDefault()
      closeTab(workspaces.activeId)
    }
    return
  }
  for (const fn of listeners) fn(ev)
}

if (typeof window !== 'undefined') {
  window.addEventListener('keydown', handler)
}

// Exported solely for testing.
export const _testing = { handler, closePalette }

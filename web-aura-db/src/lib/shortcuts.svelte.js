// Global keyboard shortcuts. PR #11 wires:
//   Cmd/Ctrl-K  → focus the tree filter input
//   Cmd/Ctrl-W  → close the active tab
//   ?            → open keyboard cheatsheet (PR #13 makes this useful)

import { workspaces, closeTab } from './workspaces.svelte.js'

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

function handler(ev) {
  const mod = isMac() ? ev.metaKey : ev.ctrlKey
  if (mod && (ev.key === 'k' || ev.key === 'K')) {
    ev.preventDefault()
    const el = document.querySelector('[data-tree-filter]')
    if (el && el instanceof HTMLInputElement) el.focus()
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

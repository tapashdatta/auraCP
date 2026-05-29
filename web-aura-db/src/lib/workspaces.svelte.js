// Open workspace tabs. Persisted to sessionStorage so a refresh restores them.

const KEY = 'auracp_db_tabs'

/**
 * @typedef {object} Tab
 * @property {string} id      stable identifier (uuid-ish)
 * @property {string} title   visible label
 * @property {string} path    hash-route the tab represents
 * @property {string} [icon]  optional inline-SVG name (rendered by TabBar)
 */

function loadInitial() {
  if (typeof sessionStorage === 'undefined') return { tabs: [], activeId: null }
  try {
    const raw = sessionStorage.getItem(KEY)
    if (!raw) return { tabs: [], activeId: null }
    const parsed = JSON.parse(raw)
    return {
      tabs: Array.isArray(parsed.tabs) ? parsed.tabs : [],
      activeId: parsed.activeId ?? null,
    }
  } catch {
    return { tabs: [], activeId: null }
  }
}

const initial = loadInitial()
export const workspaces = $state({
  /** @type {Tab[]} */
  tabs: initial.tabs,
  /** @type {string|null} */
  activeId: initial.activeId,
})

function persist() {
  if (typeof sessionStorage === 'undefined') return
  sessionStorage.setItem(KEY, JSON.stringify({ tabs: workspaces.tabs, activeId: workspaces.activeId }))
}

function newId() {
  return 'tab_' + Math.random().toString(36).slice(2, 10)
}

/**
 * @param {{title:string, path:string, icon?:string}} t
 * @returns {string} the new tab's id
 */
export function openTab(t) {
  // De-dup by path — if the same path is already open, just activate it.
  const existing = workspaces.tabs.find((x) => x.path === t.path)
  if (existing) {
    workspaces.activeId = existing.id
    persist()
    return existing.id
  }
  const id = newId()
  workspaces.tabs = [...workspaces.tabs, { id, title: t.title, path: t.path, icon: t.icon }]
  workspaces.activeId = id
  persist()
  return id
}

/** @param {string} id */
export function closeTab(id) {
  const idx = workspaces.tabs.findIndex((t) => t.id === id)
  if (idx < 0) return
  const tabs = workspaces.tabs.filter((t) => t.id !== id)
  workspaces.tabs = tabs
  if (workspaces.activeId === id) {
    const next = tabs[Math.max(0, idx - 1)] || null
    workspaces.activeId = next?.id ?? null
  }
  persist()
}

/** @param {string} id */
export function activateTab(id) {
  workspaces.activeId = id
  persist()
}

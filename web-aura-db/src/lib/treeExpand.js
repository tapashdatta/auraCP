// Pure helpers for the tree's expand/collapse state. Kept separate from
// connections.svelte.js so they're trivially unit-testable without runes.

/**
 * @param {Record<string, boolean>} map
 * @param {string} id
 * @returns {Record<string, boolean>} a NEW map with id toggled
 */
export function toggle(map, id) {
  return { ...map, [id]: !map[id] }
}

/** @param {Record<string, boolean>} map @param {string} id */
export function isOpen(map, id) {
  return !!map[id]
}

/**
 * Collapse every entry below a depth threshold (used when a parent node
 * collapses — its children must collapse too).
 * @param {Record<string, boolean>} map
 * @param {string[]} ids ids to clear from open-state
 */
export function collapseMany(map, ids) {
  /** @type {Record<string, boolean>} */
  const next = { ...map }
  for (const id of ids) delete next[id]
  return next
}

/**
 * Filter a connection list by a case-insensitive substring across name,
 * host, and engine columns.
 *
 * @param {Array<{name:string, host?:string, engine:string}>} list
 * @param {string} q
 */
export function filterConnections(list, q) {
  const needle = q.trim().toLowerCase()
  if (!needle) return list.slice()
  return list.filter((c) =>
    c.name.toLowerCase().includes(needle)
    || (c.host || '').toLowerCase().includes(needle)
    || c.engine.toLowerCase().includes(needle),
  )
}

/**
 * FIX-10 (PR #11 a11y-02): pure keyboard-navigation reducer for the
 * WAI-ARIA tree pattern. Given a flat list of visible rows (objects with
 * an id, kind, and expanded? flag) and the current focus index + key,
 * returns the action to take: { focus: number } | { toggle: string } | null.
 *
 * Splitting this out of LeftTree.svelte lets vitest exercise the full
 * keymap deterministically without a DOM or Svelte render.
 *
 * @typedef {{id:string, kind:string, expanded?:boolean}} TreeRow
 * @param {TreeRow[]} rows
 * @param {number} here index of the focused row
 * @param {string} key e.key
 * @returns {{focus?:number, toggle?:string}|null}
 */
export function treeKeyAction(rows, here, key) {
  if (!Array.isArray(rows) || rows.length === 0) return null
  if (here < 0 || here >= rows.length) return null
  const row = rows[here]
  switch (key) {
    case 'ArrowDown':
      return { focus: Math.min(rows.length - 1, here + 1) }
    case 'ArrowUp':
      return { focus: Math.max(0, here - 1) }
    case 'Home':
      return { focus: 0 }
    case 'End':
      return { focus: rows.length - 1 }
    case 'ArrowRight':
      if (row.kind === 'connection') {
        if (!row.expanded) return { toggle: row.id }
        // Already expanded → move into first child.
        return { focus: Math.min(rows.length - 1, here + 1) }
      }
      return null
    case 'ArrowLeft':
      if (row.kind === 'connection' && row.expanded) {
        return { toggle: row.id }
      }
      if (row.kind !== 'connection') {
        // Find the nearest preceding connection row.
        for (let i = here - 1; i >= 0; i--) {
          if (rows[i].kind === 'connection') return { focus: i }
        }
      }
      return null
    default:
      return null
  }
}

// Per-screen reactive store for the EXPLAIN inspector. Owns the
// selection cursor, the expanded-set, the search term, and the hotspot
// overlay flag. One factory per route mount — torn down on unmount so
// the state doesn't bleed between connections.
//
// The data model intentionally mirrors the SqlEditor classifier store
// pattern (factory returns an object with imperative .set/.toggle methods
// and a `state` $state proxy).

/**
 * @returns {{
 *   state: { selectedId: string, expanded: Set<string>, hotspotMode: boolean, searchTerm: string, showRaw: boolean },
 *   select: (id: string) => void,
 *   toggleExpand: (id: string) => void,
 *   expandAll: (ids: string[]) => void,
 *   collapseAll: () => void,
 *   setSearch: (q: string) => void,
 *   toggleHotspot: () => void,
 *   setRaw: (b: boolean) => void,
 * }}
 */
export function createExplainStore() {
  const state = $state({
    /** stable id of the currently selected node ("" = nothing selected) */
    selectedId: '0',
    /** ids that are expanded (open). Root "0" is implicit unless `0!collapsed` is in the set. */
    expanded: new Set(['0']),
    hotspotMode: false,
    searchTerm: '',
    showRaw: false,
  })

  function select(id) {
    if (typeof id !== 'string') return
    state.selectedId = id
  }

  function toggleExpand(id) {
    if (!id) return
    const next = new Set(state.expanded)
    let collapsing = false
    if (id === '0') {
      // Root uses an inverted flag so default-expanded survives Set serialization.
      if (next.has('0!collapsed')) {
        next.delete('0!collapsed')
      } else {
        next.add('0!collapsed')
        collapsing = true
      }
    } else if (next.has(id)) {
      next.delete(id)
      collapsing = true
    } else {
      next.add(id)
    }
    state.expanded = next

    // FIX CORR-2 (PR #14): if we just collapsed a parent and the current
    // selection is a descendant, the rendered DOM no longer contains the
    // selected row — aria-activedescendant would dangle. Hoist the
    // selection up to the collapsed parent.
    if (collapsing && state.selectedId && state.selectedId !== id) {
      if (isDescendantId(state.selectedId, id)) {
        state.selectedId = id
      }
    }
  }

  /**
   * Path-id descendant check. Ids are dotted ("0", "0.1", "0.1.2"); a
   * descendant id strictly starts with `parentId + "."`.
   *
   * @param {string} childId
   * @param {string} parentId
   * @returns {boolean}
   */
  function isDescendantId(childId, parentId) {
    if (!childId || !parentId) return false
    if (childId === parentId) return false
    return childId.startsWith(parentId + '.')
  }

  function expandAll(ids) {
    const next = new Set(ids)
    next.delete('0!collapsed')
    state.expanded = next
  }

  function collapseAll() {
    state.expanded = new Set(['0!collapsed'])
  }

  function setSearch(q) {
    state.searchTerm = (q == null) ? '' : String(q)
  }

  function toggleHotspot() {
    state.hotspotMode = !state.hotspotMode
  }

  function setRaw(b) {
    state.showRaw = !!b
  }

  return { state, select, toggleExpand, expandAll, collapseAll, setSearch, toggleHotspot, setRaw }
}

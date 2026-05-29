// Pure tree → flat-list transform for the flame-tree renderer.
//
// `flattenPlan(root, expanded, searchTerm)` walks the Plan.root depth-
// first and produces a `FlatEntry[]` array where each entry carries
// enough info to render a single row without re-traversing the tree.
//
// Path-based ids ("0", "0.1", "0.1.0") give us stable keys across
// re-renders even when collapsed sets change.

/**
 * @typedef {object} PlanNode
 * @property {string} kind
 * @property {string} [relation]
 * @property {string} [schema]
 * @property {string} [alias]
 * @property {string} [index]
 * @property {string} [joinType]
 * @property {string} [filter]
 * @property {PlanNode[]} [children]
 * @property {object} [metrics]
 */

/**
 * @typedef {object} FlatEntry
 * @property {string} id            stable path id ("0", "0.1", ...)
 * @property {string} parentId      "" for root
 * @property {number} depth         0 = root
 * @property {number} childCount    number of direct children
 * @property {boolean} hasChildren
 * @property {boolean} expanded
 * @property {boolean} matchesSearch  true when no search OR node matches
 * @property {PlanNode} node
 */

/**
 * Walks the tree and returns an array of visible rows (top-down,
 * depth-first). Collapsed subtrees are skipped; matched nodes (when
 * searchTerm is non-empty) are still rendered but unmatched siblings can
 * be dimmed by the renderer via `entry.matchesSearch`.
 *
 * @param {PlanNode|null|undefined} root
 * @param {Set<string>} expanded set of ids that are open
 * @param {string} [searchTerm]
 * @returns {FlatEntry[]}
 */
export function flattenPlan(root, expanded, searchTerm) {
  /** @type {FlatEntry[]} */
  const out = []
  if (!root) return out
  const needle = (searchTerm || '').trim().toLowerCase()
  walk(root, '0', '', 0, out, expanded || new Set(), needle)
  return out
}

/**
 * @param {PlanNode} node
 * @param {string} id
 * @param {string} parentId
 * @param {number} depth
 * @param {FlatEntry[]} out
 * @param {Set<string>} expanded
 * @param {string} needle
 */
function walk(node, id, parentId, depth, out, expanded, needle) {
  const children = Array.isArray(node.children) ? node.children : []
  const hasChildren = children.length > 0
  // Roots default-expanded; otherwise honor the expanded set.
  const isExpanded = depth === 0 ? !expanded.has(id + '!collapsed') : expanded.has(id)
  out.push({
    id,
    parentId,
    depth,
    childCount: children.length,
    hasChildren,
    expanded: isExpanded,
    matchesSearch: nodeMatches(node, needle),
    node,
  })
  if (hasChildren && isExpanded) {
    for (let i = 0; i < children.length; i++) {
      walk(children[i], id + '.' + i, id, depth + 1, out, expanded, needle)
    }
  }
}

/**
 * Substring match across kind / relation / index / filter.
 *
 * @param {PlanNode} node
 * @param {string} needle  already lower-cased and trimmed
 * @returns {boolean}
 */
function nodeMatches(node, needle) {
  if (!needle) return true
  const hay = [
    node.kind || '',
    node.relation || '',
    node.schema || '',
    node.alias || '',
    node.index || '',
    node.filter || '',
  ].join(' ').toLowerCase()
  return hay.includes(needle)
}

/**
 * Returns a flat array of every id in the tree (used to "expand all").
 *
 * @param {PlanNode|null|undefined} root
 * @returns {string[]}
 */
export function allIds(root) {
  /** @type {string[]} */
  const ids = []
  if (!root) return ids
  visitAll(root, '0', ids)
  return ids
}

/**
 * @param {PlanNode} node
 * @param {string} id
 * @param {string[]} acc
 */
function visitAll(node, id, acc) {
  acc.push(id)
  const ch = Array.isArray(node.children) ? node.children : []
  for (let i = 0; i < ch.length; i++) {
    visitAll(ch[i], id + '.' + i, acc)
  }
}

/**
 * Count every node in the tree (used by the NODES ribbon tile).
 *
 * @param {PlanNode|null|undefined} root
 * @returns {number}
 */
export function nodeCount(root) {
  if (!root) return 0
  let n = 1
  const ch = Array.isArray(root.children) ? root.children : []
  for (const c of ch) n += nodeCount(c)
  return n
}

/**
 * Look up a node by its path id.
 *
 * @param {PlanNode|null|undefined} root
 * @param {string} id  e.g. "0.1.2"
 * @returns {PlanNode|null}
 */
export function nodeAt(root, id) {
  if (!root || !id) return null
  const parts = id.split('.')
  if (parts[0] !== '0') return null
  let cur = root
  for (let i = 1; i < parts.length; i++) {
    const idx = Number(parts[i])
    if (!cur || !Array.isArray(cur.children) || idx < 0 || idx >= cur.children.length) return null
    cur = cur.children[idx]
  }
  return cur
}

<script>
  import { connections, toggleExpanded, isExpanded, selectConnection } from '../connections.svelte.js'
  import { navigate } from '../router.svelte.js'
  import { t } from '../strings.js'
  import { filterConnections, treeKeyAction } from '../treeExpand.js'
  import TreeNode from './TreeNode.svelte'

  let filter = $state('')

  const filtered = $derived(filterConnections(connections.list, filter))

  function onSelect(c) {
    selectConnection(c.id)
    navigate(`/connections/${c.id}`)
  }

  // FIX-10 (PR #11 a11y-02): WAI-ARIA tree keyboard pattern.
  // ArrowDown/ArrowUp traverse the visible item list (flattened to
  // account for expanded schemas). ArrowRight expands a collapsed
  // connection or moves into its first child if already expanded.
  // ArrowLeft collapses an expanded connection or focuses the parent
  // connection if the focus is on a schema row. Home/End jump to the
  // first/last visible row. Enter activates (select). Space toggles
  // selection — for connections that's identical to Enter.
  /** @type {HTMLElement|undefined} */
  let treeEl = $state(undefined)

  function visibleRows() {
    if (!treeEl) return /** @type {HTMLElement[]} */([])
    return /** @type {HTMLElement[]} */(
      Array.from(treeEl.querySelectorAll('[role="treeitem"]'))
    )
  }

  function focusIndex(rows, idx) {
    const i = Math.max(0, Math.min(rows.length - 1, idx))
    rows[i]?.focus()
  }

  function onTreeKey(e) {
    const rows = visibleRows()
    if (rows.length === 0) return
    const here = rows.indexOf(/** @type {HTMLElement} */(document.activeElement))
    if (here < 0) return
    // Build a plain-object view that treeKeyAction can reason about.
    const view = rows.map((r) => {
      const id = r.getAttribute('data-conn-id') || ''
      const kind = r.getAttribute('data-kind') || ''
      return { id, kind, expanded: kind === 'connection' && isExpanded(id) }
    })
    const action = treeKeyAction(view, here, e.key)
    if (!action) return
    e.preventDefault()
    if (action.toggle != null) toggleExpanded(action.toggle)
    if (action.focus != null) focusIndex(rows, action.focus)
  }
</script>

<aside class="tree" aria-label="connections">
  <!-- FIX (PR #11 a11y-18): the filter input had no <label> and no
       aria-label. Add an aria-label so AT users hear what the textbox
       is for; the placeholder also doubles as a visible label. -->
  <div class="tree__filter">
    <input
      data-tree-filter
      type="text"
      class="input input--flat input--mono"
      placeholder={t('tree.search.placeholder')}
      aria-label={t('tree.search.placeholder')}
      bind:value={filter}
    />
  </div>
  <div
    class="tree__list"
    role="tree"
    tabindex="-1"
    bind:this={treeEl}
    onkeydown={onTreeKey}
  >
    {#if connections.list.length === 0}
      <div class="tree__empty">
        <div>{t('tree.empty.title')}</div>
        <div class="tree__empty-hint">{t('tree.empty.body')}</div>
        <button type="button" onclick={() => navigate('/connections/new')}>{t('tree.empty.action')}</button>
      </div>
    {:else}
      {#each filtered as c (c.id)}
        <TreeNode
          node={{ kind: 'connection', id: c.id, label: c.name, engine: c.engine, readOnly: c.readOnly, status: c.status || 'idle' }}
          depth={0}
          ariaLevel={1}
          selected={connections.selectedId === c.id}
          expanded={isExpanded(c.id)}
          onToggle={() => toggleExpanded(c.id)}
          onSelect={() => onSelect(c)}
        />
        {#if isExpanded(c.id)}
          <!-- Schemas lazy-load in PR #12. For now: a placeholder leaf to prove the
               expand/collapse machinery so the tree renders correctly with at
               least one nested row. -->
          <TreeNode
            node={{ kind: 'schema', id: c.id + ':_pending', label: '(schemas load in PR #12)' }}
            depth={1}
            ariaLevel={2}
            selected={false}
            expanded={false}
          />
        {/if}
      {/each}
    {/if}
  </div>
</aside>

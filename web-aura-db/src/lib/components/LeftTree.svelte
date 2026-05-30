<script>
  import { connections, toggleExpanded, isExpanded, selectConnection } from '../connections.svelte.js'
  import { navigate } from '../router.svelte.js'
  import { t } from '../strings.js'
  import { filterConnections, treeKeyAction } from '../treeExpand.js'
  import { loadSchemas, loadObjects } from '../sqlEditor/schemaCache.svelte.js'
  import TreeNode from './TreeNode.svelte'

  let filter = $state('')

  const filtered = $derived(filterConnections(connections.list, filter))

  // Lazy-loaded schema/table data, keyed for reactive rendering.
  let schemas = $state({})   // connId -> string[]
  let objects = $state({})   // "connId:schema" -> { tables:[], views:[], ... }
  let busy = $state({})      // key -> bool (loading spinner)

  function onSelect(c) {
    selectConnection(c.id)
    navigate(`/connections/${c.id}`)
  }

  // Expand a connection: toggle, and on first open fetch its schemas.
  async function expandConn(c) {
    toggleExpanded(c.id)
    if (!isExpanded(c.id) || schemas[c.id]) return
    busy = { ...busy, [c.id]: true }
    try { schemas = { ...schemas, [c.id]: await loadSchemas(c.id) } }
    catch { schemas = { ...schemas, [c.id]: [] } }
    busy = { ...busy, [c.id]: false }
  }

  // Expand a schema: toggle, and on first open fetch its tables/views.
  async function expandSchema(connId, schema) {
    const key = connId + ':' + schema
    toggleExpanded(key)
    if (!isExpanded(key) || objects[key]) return
    busy = { ...busy, [key]: true }
    try { objects = { ...objects, [key]: await loadObjects(connId, schema) } }
    catch { objects = { ...objects, [key]: { tables: [], views: [] } } }
    busy = { ...busy, [key]: false }
  }

  const openSchema = (connId, s) => navigate(`/connections/${connId}/schemas/${s}`)
  const openTable = (connId, s, tbl) => navigate(`/connections/${connId}/schemas/${s}/tables/${tbl}/rows`)

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
          onToggle={() => expandConn(c)}
          onSelect={() => onSelect(c)}
        />
        {#if isExpanded(c.id)}
          {#if busy[c.id]}
            <TreeNode node={{ kind: 'schema', id: c.id + ':_loading', label: 'Loading…' }} depth={1} ariaLevel={2} />
          {:else if (schemas[c.id] || []).length === 0}
            <TreeNode node={{ kind: 'schema', id: c.id + ':_empty', label: '(no schemas)' }} depth={1} ariaLevel={2} />
          {:else}
            {#each schemas[c.id] as s (s)}
              {@const skey = c.id + ':' + s}
              <TreeNode
                node={{ kind: 'schema', id: skey, label: s }}
                depth={1} ariaLevel={2}
                expanded={isExpanded(skey)}
                onToggle={() => expandSchema(c.id, s)}
                onSelect={() => openSchema(c.id, s)}
              />
              {#if isExpanded(skey)}
                {#if busy[skey]}
                  <TreeNode node={{ kind: 'table', id: skey + ':_loading', label: 'Loading…' }} depth={2} ariaLevel={3} />
                {:else}
                  {@const objs = objects[skey] || { tables: [], views: [] }}
                  {#if (objs.tables || []).length === 0 && (objs.views || []).length === 0}
                    <TreeNode node={{ kind: 'table', id: skey + ':_empty', label: '(no tables)' }} depth={2} ariaLevel={3} />
                  {:else}
                    {#each objs.tables || [] as tbl (tbl.name)}
                      <TreeNode
                        node={{ kind: 'table', id: skey + ':' + tbl.name, label: tbl.name }}
                        depth={2} ariaLevel={3}
                        onSelect={() => openTable(c.id, s, tbl.name)}
                        onActivate={() => openTable(c.id, s, tbl.name)}
                      />
                    {/each}
                    {#each objs.views || [] as v (v.name)}
                      <TreeNode
                        node={{ kind: 'view', id: skey + ':' + v.name, label: v.name }}
                        depth={2} ariaLevel={3}
                        onSelect={() => openTable(c.id, s, v.name)}
                        onActivate={() => openTable(c.id, s, v.name)}
                      />
                    {/each}
                  {/if}
                {/if}
              {/if}
            {/each}
          {/if}
        {/if}
      {/each}
    {/if}
  </div>
</aside>

<script>
  import { connections, selectConnection, toggleExpanded, isExpanded } from '../connections.svelte.js'
  import { routeState, navigate } from '../router.svelte.js'
  import { t } from '../strings.js'
  import { loadSchemas, loadObjects } from '../sqlEditor/schemaCache.svelte.js'
  import EngineGlyph from './EngineGlyph.svelte'
  import Icon from './Icon.svelte'
  import StatusDot from './StatusDot.svelte'
  import DropdownMenu from './DropdownMenu.svelte'
  import TreeNode from './TreeNode.svelte'

  // Prototype "Studio" sidebar: a connection switcher at the top, the
  // active connection's schema tree below, and a connection-status footer.
  // When no connection is active (fresh load / bare /connections) it falls
  // back to a flat connection list so you can pick one.
  const active = $derived(
    connections.list.find((c) => c.id === routeState.params.id) ||
    connections.list.find((c) => c.id === connections.selectedId) ||
    null,
  )

  const engineLabel = (e) => t(`tree.engine.${e}`) || e
  function statusText(c) {
    switch (c.status) {
      case 'ok': return 'Connected'
      case 'down': return 'Disconnected'
      case 'warn': return 'Degraded'
      default: return 'Idle'
    }
  }

  let filter = $state('')
  let schemas = $state({}) // connId -> string[]   (undefined = not yet loaded)
  let objects = $state({}) // "connId:schema" -> { tables, views }
  let busy = $state({}) // key -> bool

  // Lazy-load the active connection's schemas the first time it becomes active.
  $effect(() => {
    const a = active
    if (!a || schemas[a.id] !== undefined || busy[a.id]) return
    busy = { ...busy, [a.id]: true }
    loadSchemas(a.id)
      .then((s) => { schemas = { ...schemas, [a.id]: s } })
      .catch(() => { schemas = { ...schemas, [a.id]: [] } })
      .finally(() => { busy = { ...busy, [a.id]: false } })
  })

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
  function openConn(id) {
    selectConnection(id)
    navigate(`/connections/${id}`)
  }

  // Connection switcher dropdown.
  let switchOpen = $state(false)
  /** @type {HTMLElement|undefined} */
  let switchBtn = $state(undefined)
  const switchItems = $derived([
    ...connections.list
      .filter((c) => !active || c.id !== active.id)
      .map((c) => ({ label: c.name, onSelect: () => openConn(c.id) })),
    { label: t('tree.empty.action'), onSelect: () => navigate('/connections/new') },
  ])

  const activeSchemas = $derived(active ? (schemas[active.id] || []) : [])
  const fSchemas = $derived(
    filter ? activeSchemas.filter((s) => s.toLowerCase().includes(filter.toLowerCase())) : activeSchemas,
  )

  // FIX (PR #11 a11y-02): ArrowUp/Down/Home/End move focus through the
  // visible schema/table rows. Enter/Space (handled inside TreeNode)
  // expand a schema or open a table, so we don't need to thread lazy-load
  // through the key handler.
  /** @type {HTMLElement|undefined} */
  let treeEl = $state(undefined)
  function visibleRows() {
    if (!treeEl) return /** @type {HTMLElement[]} */ ([])
    return /** @type {HTMLElement[]} */ (Array.from(treeEl.querySelectorAll('[role="treeitem"]')))
  }
  function onTreeKey(e) {
    const rows = visibleRows()
    if (!rows.length) return
    const here = rows.indexOf(/** @type {HTMLElement} */ (document.activeElement))
    if (here < 0) return
    let next = here
    if (e.key === 'ArrowDown') next = here + 1
    else if (e.key === 'ArrowUp') next = here - 1
    else if (e.key === 'Home') next = 0
    else if (e.key === 'End') next = rows.length - 1
    else return
    e.preventDefault()
    rows[Math.max(0, Math.min(rows.length - 1, next))]?.focus()
  }
</script>

<aside class="sidebar" aria-label="connections">
  {#if active}
    <!-- Connection switcher -->
    <div class="conn-switch">
      <EngineGlyph engine={active.engine} size={18} />
      <span class="conn-switch__meta">
        <span class="conn-switch__name">{active.name}</span>
        <span class="conn-switch__sub">{engineLabel(active.engine)}{active.database ? ' · ' + active.database : ''}</span>
      </span>
      <button
        bind:this={switchBtn}
        type="button"
        class="conn-switch__btn"
        aria-label="Switch connection"
        aria-haspopup="menu"
        aria-expanded={switchOpen}
        onclick={() => (switchOpen = !switchOpen)}
      >
        <Icon name="chevD" size={16} />
      </button>
      <DropdownMenu bind:open={switchOpen} items={switchItems} anchor={switchBtn} />
    </div>

    <!-- Schemas section -->
    <div class="side-section__label">
      <span>Schemas</span>
      <span class="side-count">{activeSchemas.length}</span>
    </div>
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
    <div class="tree__list" role="tree" tabindex="-1" bind:this={treeEl} onkeydown={onTreeKey}>
      {#if busy[active.id]}
        <div class="tree__hint">Loading…</div>
      {:else if fSchemas.length === 0}
        <div class="tree__hint">{filter ? 'No matches' : '(no schemas)'}</div>
      {:else}
        {#each fSchemas as s (s)}
          {@const skey = active.id + ':' + s}
          <TreeNode
            node={{ kind: 'schema', id: skey, label: s }}
            depth={0}
            ariaLevel={1}
            expanded={isExpanded(skey)}
            onToggle={() => expandSchema(active.id, s)}
            onSelect={() => openSchema(active.id, s)}
          />
          {#if isExpanded(skey)}
            {#if busy[skey]}
              <TreeNode node={{ kind: 'table', id: skey + ':_loading', label: 'Loading…' }} depth={1} ariaLevel={2} />
            {:else}
              {@const objs = objects[skey] || { tables: [], views: [] }}
              {#if (objs.tables || []).length === 0 && (objs.views || []).length === 0}
                <TreeNode node={{ kind: 'table', id: skey + ':_empty', label: '(no tables)' }} depth={1} ariaLevel={2} />
              {:else}
                {#each objs.tables || [] as tbl (tbl.name)}
                  <TreeNode
                    node={{ kind: 'table', id: skey + ':' + tbl.name, label: tbl.name }}
                    depth={1}
                    ariaLevel={2}
                    onSelect={() => openTable(active.id, s, tbl.name)}
                    onActivate={() => openTable(active.id, s, tbl.name)}
                  />
                {/each}
                {#each objs.views || [] as v (v.name)}
                  <TreeNode
                    node={{ kind: 'view', id: skey + ':' + v.name, label: v.name }}
                    depth={1}
                    ariaLevel={2}
                    onSelect={() => openTable(active.id, s, v.name)}
                    onActivate={() => openTable(active.id, s, v.name)}
                  />
                {/each}
              {/if}
            {/if}
          {/if}
        {/each}
      {/if}
    </div>

    <!-- Footer -->
    <div class="conn-foot">
      <StatusDot state={active.status || 'idle'} />
      <span class="truncate">{statusText(active)} · {active.username || engineLabel(active.engine)}</span>
    </div>
  {:else}
    <!-- No active connection → flat connection list -->
    <div class="side-section__label side-section__label--top">
      <span>{t('nav.connections')}</span>
      <span class="side-count">{connections.list.length}</span>
    </div>
    <div class="tree__list" role="list">
      {#if connections.loading}
        <div class="tree__hint">{t('status.loading')}</div>
      {:else if connections.list.length === 0}
        <div class="tree__empty">
          <div>{t('tree.empty.title')}</div>
          <div class="tree__empty-hint">{t('tree.empty.body')}</div>
        </div>
      {:else}
        {#each connections.list as c (c.id)}
          <button class="side-row" type="button" onclick={() => openConn(c.id)}>
            <EngineGlyph engine={c.engine} size={16} />
            <span class="side-row__meta">
              <span class="side-row__name truncate">{c.name}</span>
              <span class="side-row__sub truncate">{engineLabel(c.engine)}{c.database ? ' · ' + c.database : ''}</span>
            </span>
          </button>
        {/each}
      {/if}
    </div>
    <div class="conn-foot conn-foot--btn">
      <button class="btn btn--primary" type="button" onclick={() => navigate('/connections/new')}>
        <Icon name="plus" size={15} /> {t('tree.empty.action')}
      </button>
    </div>
  {/if}
</aside>

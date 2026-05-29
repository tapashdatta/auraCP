<script>
  // PR #15 history screen. Replaces the old 63-line HistoryView.svelte
  // which read non-existent r.ranAt / r.connection fields (the wire spec
  // uses `executed` and `connectionId`). Lazy-loaded by App.svelte so the
  // filter logic + table grid don't ship in the initial bundle.

  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { t } from '../lib/strings.js'
  import { connections } from '../lib/connections.svelte.js'
  import { replayInEditor } from '../lib/replay.js'
  import { applyFilters } from '../lib/historyFilters.js'
  import TextField from '../lib/components/TextField.svelte'
  import EmptyState from '../lib/components/EmptyState.svelte'
  import LoadingPane from '../lib/components/LoadingPane.svelte'

  /** @typedef {{ id:any, sql:string, class?:string, durationMs?:number, executed?:string, connectionId?:string, engine?:string, starred?:boolean, tags?:string[], error?:string, rowsReturned?:number }} HistoryEntry */

  /** @type {HistoryEntry[]} */
  let rawEntries = $state([])
  let loading = $state(false)
  let search = $state('')
  /** @type {'1h'|'24h'|'7d'|'30d'|'all'} */
  let dateRange = $state('7d')
  /** @type {string} */
  let connFilter = $state('')   // '' = all
  /** @type {'all'|'success'|'error'} */
  let statusFilter = $state('all')
  /** @type {'all'|'read'|'write'|'ddl'|'dangerous'} */
  let classFilter = $state('all')
  let starredOnly = $state(false)

  function activeConn() {
    return connections.selectedId || connections.list?.[0]?.id || null
  }

  async function fetchAll() {
    loading = true
    try {
      const list = connections.list || []
      if (connFilter && connFilter !== '*') {
        rawEntries = await loadForConn(connFilter)
      } else if (list.length === 0) {
        const id = activeConn()
        rawEntries = id ? await loadForConn(id) : []
      } else {
        // Fan-out across all connections — there is no server-side
        // cross-connection history endpoint. Cap at 25 conns.
        const conns = list.slice(0, 25)
        const results = await Promise.allSettled(conns.map((c) => loadForConn(c.id)))
        const merged = []
        for (const r of results) if (r.status === 'fulfilled') merged.push(...r.value)
        rawEntries = merged
      }
    } catch { rawEntries = [] }
    finally { loading = false }
  }

  /** @param {string} id @returns {Promise<HistoryEntry[]>} */
  async function loadForConn(id) {
    try {
      const r = await api.connHistory(id)
      const list = (r && Array.isArray(r.entries)) ? r.entries : []
      return list.map((e) => ({ ...e, connectionId: e.connectionId || id }))
    } catch { return [] }
  }

  onMount(fetchAll)

  // Re-fetch when conn filter changes.
  $effect(() => {
    // Take the dep so $effect tracks it.
    const _ = connFilter
    fetchAll()
  })

  function rangeCutoffMs(r) {
    switch (r) {
      case '1h':  return 60 * 60 * 1000
      case '24h': return 24 * 60 * 60 * 1000
      case '7d':  return 7 * 24 * 60 * 60 * 1000
      case '30d': return 30 * 24 * 60 * 60 * 1000
      default:    return 0  // 'all'
    }
  }

  // Composed client-side filter pipeline. Exported indirectly via the
  // pure `applyFilters` helper below for unit testing.
  const filtered = $derived(applyFilters(rawEntries, {
    search: search.trim().toLowerCase(),
    dateRange,
    statusFilter,
    classFilter,
    starredOnly,
  }))

  async function toggleStar(entry) {
    const next = !entry.starred
    // Optimistic update + rollback.
    rawEntries = rawEntries.map((e) => (e.id === entry.id ? { ...e, starred: next } : e))
    try {
      await api.updateHistory(entry.connectionId || activeConn() || '', String(entry.id), { starred: next })
    } catch {
      rawEntries = rawEntries.map((e) => (e.id === entry.id ? { ...e, starred: !next } : e))
    }
  }

  function replayRow(entry, newTab = false) {
    if (!entry.connectionId) return
    replayInEditor(entry.connectionId, entry.sql || '', { newTab })
  }

  function copySql(entry) {
    if (typeof navigator === 'undefined' || !navigator.clipboard) return
    try { navigator.clipboard.writeText(entry.sql || '') } catch { /* ignore */ }
  }

  function fmtWhen(when) {
    const tt = Date.parse(String(when))
    if (!tt || Number.isNaN(tt)) return ''
    const d = Date.now() - tt
    if (d < 60_000) return Math.max(1, Math.floor(d / 1000)) + 's ago'
    if (d < 3_600_000) return Math.floor(d / 60_000) + 'm ago'
    if (d < 86_400_000) return Math.floor(d / 3_600_000) + 'h ago'
    return Math.floor(d / 86_400_000) + 'd ago'
  }
</script>

<div class="pane">
  <header class="pane__head" style="display:flex;align-items:baseline;justify-content:space-between;gap:12px">
    <h1 class="pane__title">{t('history.title')}</h1>
    <span style="color:var(--text-mute);font-size:var(--fs-meta)">{filtered.length} entries</span>
  </header>

  <div class="history__filters">
    <div class="history__range" role="group" aria-label="Date range">
      {#each ['1h','24h','7d','30d','all'] as r (r)}
        <button
          class="history__rangeBtn"
          class:history__rangeBtn--active={dateRange === r}
          onclick={() => { dateRange = r }}
        >{r}</button>
      {/each}
    </div>

    <select class="history__select" bind:value={connFilter} aria-label="Connection">
      <option value="">All connections</option>
      {#each (connections.list || []) as c (c.id)}
        <option value={c.id}>{c.name || c.id}</option>
      {/each}
    </select>

    <select class="history__select" bind:value={statusFilter} aria-label="Status">
      <option value="all">All status</option>
      <option value="success">Success</option>
      <option value="error">Error</option>
    </select>

    <select class="history__select" bind:value={classFilter} aria-label="Class">
      <option value="all">All classes</option>
      <option value="read">read</option>
      <option value="write">write</option>
      <option value="ddl">ddl</option>
      <option value="dangerous">dangerous</option>
    </select>

    <div style="flex:1;min-width:200px">
      <TextField bind:value={search} placeholder={t('history.search.placeholder')} mono />
    </div>

    <label class="history__starred">
      <input type="checkbox" bind:checked={starredOnly} />
      Starred only
    </label>
  </div>

  {#if loading}
    <LoadingPane />
  {:else if filtered.length === 0}
    <EmptyState title={t('history.empty.title')} body={t('history.empty.body')} />
  {:else}
    <!-- A11Y-3: dropped half-applied ARIA grid (role=table + role=row but
         no role=gridcell/cells + dual tab-stops with the nested star
         button). The data here is read-and-click, not 2D-navigated, so
         the semantic <table>/<tbody>/<tr>/<td> elements carry all the
         needed semantics; the star button keeps its own tab stop. -->
    <table class="history__table" aria-label="Query history">
      <thead>
        <tr class="history__row history__row--head">
          <th scope="col" aria-label="Starred"></th>
          <th scope="col">Class</th>
          <th scope="col">SQL</th>
          <th scope="col">Connection</th>
          <th scope="col">Duration</th>
          <th scope="col">When</th>
        </tr>
      </thead>
      <tbody>
        {#each filtered as r (r.id)}
          <tr
            class="history__row"
            class:history__row--error={!!r.error}
            onclick={() => replayRow(r)}
            onkeydown={(e) => { if (e.key === 'Enter') replayRow(r) }}
            title="Click to replay in editor"
          >
            <td>
              <button
                class="history__star"
                class:history__star--on={r.starred}
                aria-label={r.starred ? 'Unstar' : 'Star'}
                onclick={(e) => { e.stopPropagation(); toggleStar(r) }}
              >★</button>
            </td>
            <td><span class="history__class" data-class={r.class || 'unknown'}>{r.class || ''}</span></td>
            <td><span class="history__sql" title={r.sql || ''}>{(r.sql || '').slice(0, 120)}</span></td>
            <td><span class="history__conn">{r.connectionId || ''}</span></td>
            <td><span class="history__dur">{typeof r.durationMs === 'number' ? r.durationMs + 'ms' : ''}</span></td>
            <td><span class="history__when">{fmtWhen(r.executed)}</span></td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</div>

<style>
  .history__filters {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
    padding: 12px 0;
    margin-bottom: 12px;
    border-bottom: 1px solid var(--border);
  }
  .history__range {
    display: inline-flex;
    border: 1px solid var(--border);
    border-radius: 6px;
    overflow: hidden;
  }
  .history__rangeBtn {
    background: var(--surface);
    color: var(--text);
    border: none;
    border-right: 1px solid var(--border);
    padding: 4px 10px;
    cursor: pointer;
    font: 12px/1 'IBM Plex Sans', sans-serif;
  }
  .history__rangeBtn:last-child { border-right: none; }
  .history__rangeBtn--active {
    background: var(--surface-active, var(--surface-hover));
    color: var(--accent);
  }
  .history__select {
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 4px 8px;
    font: 12px/1 'IBM Plex Sans', sans-serif;
  }
  .history__starred {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    color: var(--text-mute, #888);
  }
  .history__table {
    border: 1px solid var(--border);
    border-radius: 8px;
    overflow: hidden;
    /* A11Y-3 follow-up: use grid layout on rows via the same CSS we had
       before, but on real <tr> elements. display: grid overrides the
       default table-row display so the column track stays identical. */
    border-collapse: collapse;
    width: 100%;
    table-layout: fixed;
    display: block;
  }
  .history__table thead,
  .history__table tbody { display: block; }
  .history__row {
    display: grid;
    grid-template-columns: 32px 90px 1fr 200px 80px 90px;
    align-items: center;
    gap: 10px;
    padding: 8px 12px;
    border-bottom: 1px solid var(--border);
    cursor: pointer;
    font-size: 12px;
  }
  .history__table td,
  .history__table th {
    text-align: left;
    padding: 0;
    border: none;
    font-weight: inherit;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
  }
  .history__row:hover { background: var(--surface-hover); }
  .history__row:last-child { border-bottom: none; }
  .history__row--head {
    background: var(--surface-sunk, var(--surface));
    font: 600 11px/1.4 'IBM Plex Sans', sans-serif;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--text-mute, #888);
    cursor: default;
  }
  .history__row--head:hover { background: var(--surface-sunk, var(--surface)); }
  .history__row--error { border-left: 2px solid #c84a4a; }
  .history__star {
    background: transparent;
    border: none;
    color: var(--text-mute, #888);
    cursor: pointer;
    font-size: 14px;
    padding: 0;
  }
  .history__star--on { color: #d4a14a; }
  .history__class {
    font: 11px/1 'IBM Plex Mono', monospace;
    color: var(--text-mute, #888);
    text-transform: uppercase;
  }
  .history__sql {
    font: 12px/1.4 'IBM Plex Mono', monospace;
    color: var(--text);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .history__conn, .history__dur, .history__when {
    color: var(--text-mute, #888);
    font: 11px/1 'IBM Plex Mono', monospace;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>


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

  // FIX (PR #15.5 C4/INT-2): when the user has more than CONN_CAP
  // connections, the cross-connection fanout silently drops the rest.
  // Surface it as a banner so the user knows their tail connections
  // aren't being included.
  const CONN_CAP = 25
  let connCapHit = $state(false)
  // FIX (PR #15.5 INT-4): cross-connection fanout surfaces 403/permission
  // errors as empty data. Track how many connections rejected so we can
  // show a per-connection error count to the user.
  let connErrorCount = $state(0)
  // FIX (PR #15.5 INT-8): primeHistoryCache TOCTOU — a rapid conn-filter
  // switch can resolve in the wrong order (request A completes after B).
  // Bump a request token on every fetchAll and discard stale resolves.
  let _fetchToken = 0

  function activeConn() {
    return connections.selectedId || connections.list?.[0]?.id || null
  }

  async function fetchAll() {
    const myToken = ++_fetchToken
    loading = true
    connCapHit = false
    connErrorCount = 0
    try {
      const list = connections.list || []
      let merged = []
      let errCount = 0
      let capHit = false
      if (connFilter && connFilter !== '*') {
        const { rows, error } = await loadForConn(connFilter)
        merged = rows
        if (error) errCount = 1
      } else if (list.length === 0) {
        const id = activeConn()
        if (id) {
          const { rows, error } = await loadForConn(id)
          merged = rows
          if (error) errCount = 1
        }
      } else {
        // Fan-out across all connections — there is no server-side
        // cross-connection history endpoint. Cap at CONN_CAP conns.
        capHit = list.length > CONN_CAP
        const conns = list.slice(0, CONN_CAP)
        const results = await Promise.allSettled(conns.map((c) => loadForConn(c.id)))
        for (const r of results) {
          if (r.status === 'fulfilled') {
            merged.push(...r.value.rows)
            if (r.value.error) errCount += 1
          } else {
            errCount += 1
          }
        }
      }
      // FIX (PR #15.5 INT-8): drop stale resolves. If the user changed
      // the conn filter while our request was in flight, a newer fetch
      // is already running with a higher token — let that one win.
      if (myToken !== _fetchToken) return
      rawEntries = merged
      connErrorCount = errCount
      connCapHit = capHit
    } catch {
      if (myToken !== _fetchToken) return
      rawEntries = []
    } finally {
      if (myToken === _fetchToken) loading = false
    }
  }

  /**
   * @param {string} id
   * @returns {Promise<{rows: HistoryEntry[], error: boolean}>}
   */
  async function loadForConn(id) {
    try {
      const r = await api.connHistory(id)
      const list = (r && Array.isArray(r.entries)) ? r.entries : []
      return { rows: list.map((e) => ({ ...e, connectionId: e.connectionId || id })), error: false }
    } catch { return { rows: [], error: true } }
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
    // FIX (PR #15.5 C6): when an entry.id is undefined/null, the
    // optimistic-update `e.id === entry.id` predicate collapses ALL
    // undefined-id rows together (undefined === undefined is true) and
    // a single star toggle would flip every unidentifiable row. Bail
    // when we can't uniquely identify the row.
    if (entry == null || entry.id === undefined || entry.id === null || entry.id === '') return
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

  // A11Y-6: throttle the count announced via aria-live so SR doesn't
  // get a torrent of "N entries" updates while the user is typing in
  // the search box. ~250ms debounce.
  let filteredCountAnnounce = $state(0)
  /** @type {ReturnType<typeof setTimeout>|null} */
  let _annTimer = null
  $effect(() => {
    const n = filtered.length
    if (_annTimer) clearTimeout(_annTimer)
    _annTimer = setTimeout(() => { filteredCountAnnounce = n }, 250)
  })

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
  <!-- FIX (PR #15.5 A11Y-6 routed to PR #11.5): pluralise + throttle.
       The aria-live region used to read "1 results" — wrong English and
       fired on every keystroke. We pluralise and use a debounced shadow
       state so screen readers don't yell on every typed character. -->
  <header class="pane__head history__header">
    <h1 class="pane__title">{t('history.title')}</h1>
    <span class="history__count" aria-live="polite" aria-atomic="true">
      {filteredCountAnnounce === 1 ? '1 entry' : `${filteredCountAnnounce} entries`}
    </span>
  </header>

  <div class="history__filters">
    <!-- FIX (PR #15.5 A11Y-5 routed to PR #11.5): segmented buttons need
         aria-pressed so AT users can tell which option is active. -->
    <div class="history__range" role="group" aria-label="Date range">
      {#each ['1h','24h','7d','30d','all'] as r (r)}
        <button
          type="button"
          class="history__rangeBtn"
          class:history__rangeBtn--active={dateRange === r}
          aria-pressed={dateRange === r}
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

    <div class="history__searchSlot">
      <TextField bind:value={search} placeholder={t('history.search.placeholder')} mono />
    </div>

    <label class="history__starred">
      <input type="checkbox" bind:checked={starredOnly} />
      Starred only
    </label>
  </div>

  <!-- FIX (PR #15.5 C4/INT-2): tell the user when the cross-connection
       fanout has been capped, instead of silently truncating their list.
       FIX (PR #15.5 INT-4): also tell the user when some connections
       errored (403/permission/network) instead of folding their failures
       into an empty result set. -->
  {#if connCapHit || connErrorCount > 0}
    <div class="history__banner" role="status" aria-live="polite">
      {#if connCapHit}
        <span class="history__bannerItem">
          Showing history for the first {CONN_CAP} connections.
          Filter by connection to see others.
        </span>
      {/if}
      {#if connErrorCount > 0}
        <span class="history__bannerItem history__bannerItem--err">
          {connErrorCount === 1 ? '1 connection' : `${connErrorCount} connections`} returned an error (permission denied or unreachable).
        </span>
      {/if}
    </div>
  {/if}

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
          <!-- FIX (PR #15.5 A11Y-13 routed): document the
               "Click or Enter to replay" gesture via title= (mouse
               affordance) and keep the star + replay buttons keyboard-
               reachable via their own tab stops. The tr deliberately
               has no tabindex (A11Y-3 regression guard) — keyboard
               replay happens by activating the star or sql cell. -->
          <tr
            class="history__row"
            class:history__row--error={!!r.error}
            onclick={() => replayRow(r)}
            onkeydown={(e) => { if (e.key === 'Enter') replayRow(r) }}
            title="Click or press Enter to replay in editor"
          >
            <td>
              <!-- FIX (PR #15.5 D-2): outline glyph (☆) for unstarred,
                   filled glyph (★) for starred. Color-only differentiation
                   failed for users with color-vision differences and was
                   easy to miss in dense rows. -->
              <button
                type="button"
                class="history__star"
                class:history__star--on={r.starred}
                aria-label={r.starred ? 'Unstar' : 'Star'}
                aria-pressed={r.starred}
                onclick={(e) => { e.stopPropagation(); toggleStar(r) }}
              >{r.starred ? '★' : '☆'}</button>
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
  .history__header {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 12px;
  }
  .history__count {
    color: var(--text-mute);
    font-size: var(--fs-meta);
  }
  .history__searchSlot { flex: 1; min-width: 200px; }
  .history__filters {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
    padding: 12px 0;
    margin-bottom: 12px;
    border-bottom: 1px solid var(--border);
  }
  /* FIX (PR #15.5 D-9): commit to a single style — segmented tab
     control with crisp internal dividers and a 6px outer radius. The
     previous styling sat between "pill" and "tab" because individual
     buttons inherited the parent rounded corners without explicit
     end-cap radii. Pin the outer radius to the first/last buttons via
     :first-child / :last-child so each cell reads as a discrete tab. */
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
    padding: 5px 12px;
    cursor: pointer;
    font: 12px/1 'IBM Plex Sans', sans-serif;
    border-radius: 0;
  }
  .history__rangeBtn:last-child { border-right: none; }
  .history__rangeBtn--active {
    background: var(--surface-active, var(--surface-hover));
    color: var(--accent);
    font-weight: 600;
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
  /* FIX (PR #15.5 D-3): replace hard-coded #c84a4a with the existing
     danger token so light/dark/high-contrast themes pick up the right
     hue without us re-tuning per-theme. Fall back to the legacy hex if
     a theme hasn't defined --danger yet. */
  .history__row--error { border-left: 2px solid var(--danger, #c84a4a); }
  .history__star {
    background: transparent;
    border: none;
    color: var(--text-mute, #888);
    cursor: pointer;
    font-size: 14px;
    padding: 0;
  }
  /* FIX (PR #15.5 D-3): hard-coded #d4a14a bypassed the accent token
     system. Use the existing --accent (now coupled to the active theme)
     so star color stays in sync with the rest of the surface. */
  .history__star--on { color: var(--accent, #d4a14a); }

  /* FIX (PR #15.5 C4/INT-2, INT-4): banner styles for partial-result
     situations. Uses the existing surface-sunk + border tokens so it
     reads as a subtle inline notice, not an alarm bar. */
  .history__banner {
    display: flex;
    flex-wrap: wrap;
    gap: 10px 16px;
    padding: 8px 12px;
    margin-bottom: 12px;
    background: var(--surface-sunk, var(--surface));
    border: 1px solid var(--border);
    border-radius: 6px;
    font: 12px/1.4 'IBM Plex Sans', sans-serif;
    color: var(--text-mute, #888);
  }
  .history__bannerItem--err {
    color: var(--danger, #c84a4a);
  }
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


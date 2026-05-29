<script>
  // ExplainInspectorScreen — the EXPLAIN inspector route. Bound to
  // /connections/:id/explain. Receives its initial statement via the
  // sessionStorage handoff (`explain:pending`) so the URL stays short
  // and the statement is never URL-encoded.
  //
  // Architecture mirrors SqlEditor:
  //   - Lazy-loaded from App.svelte (Vite splits this into its own chunk
  //     so non-/explain routes pay zero bytes for the inspector).
  //   - State lives in $state runes; selection + expand-set in a small
  //     factory store so it tears down cleanly on route change.

  import { routeState, navigate } from '../lib/router.svelte.js'
  import { api } from '../lib/api.js'

  import FlameTree from '../lib/components/explain/FlameTree.svelte'
  import NodeDetail from '../lib/components/explain/NodeDetail.svelte'
  import MetricsRibbon from '../lib/components/explain/MetricsRibbon.svelte'
  import WarningBanner from '../lib/components/explain/WarningBanner.svelte'
  import RawPlanView from '../lib/components/explain/RawPlanView.svelte'
  import AnalyzeToggle from '../lib/components/explain/AnalyzeToggle.svelte'
  import Btn from '../lib/components/Btn.svelte'
  import Spinner from '../lib/components/Spinner.svelte'
  import EmptyState from '../lib/components/EmptyState.svelte'
  import StatusDot from '../lib/components/StatusDot.svelte'

  import { createExplainStore } from '../lib/sqlEditor/explainTreeStore.svelte.js'
  import { nodeAt } from '../lib/sqlEditor/explainFlatten.js'

  const id = $derived(routeState.params.id)

  /** @type {{id:string, engine:'mariadb'|'postgres', name?:string}|null} */
  let conn = $state(null)
  let connLoading = $state(true)

  let stmt = $state('')
  /** @type {string} */
  let initialClass = $state('read')
  /** @type {boolean} */
  let analyze = $state(false)

  /** @type {any} */
  let plan = $state(null)
  let loading = $state(false)
  /** @type {{code:string, message:string}|null} */
  let error = $state(null)
  /** @type {'idle'|'planning'|'running'|'done'|'error'} */
  let status = $state('idle')

  const store = createExplainStore()
  const treeState = $derived(store.state)

  /**
   * FIX INT-1 (PR #14): only consume the sessionStorage handoff when it
   * matches the current route's connection id. Otherwise the inspector
   * loaded for connection B would happily pick up a pending statement
   * left over from connection A.
   *
   * @param {string} routeId
   * @returns {{stmt:string, klass:string, analyze:boolean}|null}
   */
  function consumePendingFor(routeId) {
    try {
      const raw = sessionStorage.getItem('explain:pending')
      if (!raw) return null
      const parsed = JSON.parse(raw)
      // Always clear — either we take it, or it's stale relative to the
      // route and we want to drop it so it can't be picked up later.
      try { sessionStorage.removeItem('explain:pending') } catch { /* ignore */ }
      if (parsed && parsed.connId && parsed.connId !== routeId) {
        // Mismatch — drop.
        return null
      }
      return parsed || null
    } catch {
      try { sessionStorage.removeItem('explain:pending') } catch { /* ignore */ }
      return null
    }
  }

  async function loadForRoute(routeId) {
    if (!routeId) return
    // Reset transient state — we're (re-)mounting for `routeId`.
    plan = null
    error = null
    status = 'idle'
    stmt = ''
    initialClass = 'read'
    analyze = false
    store.select('0')

    const pending = consumePendingFor(routeId)
    if (pending) {
      stmt = pending.stmt || ''
      initialClass = pending.klass || 'read'
      analyze = !!pending.analyze
    }

    // Fetch connection meta.
    connLoading = true
    try {
      const c = await api.getConnection(routeId)
      const engine = (c?.engine === 'postgres' || c?.engine === 'postgresql') ? 'postgres' : 'mariadb'
      conn = { id: c?.id || routeId, engine, name: c?.name }
    } catch {
      conn = { id: routeId, engine: 'mariadb', name: '' }
    }
    connLoading = false
    if (stmt) await runExplain()
  }

  // ─── lifecycle ────────────────────────────────────────────────────
  //
  // FIX INT-1 / INT-2 (PR #14):
  //   - We treat the route's :id as the source of truth for which
  //     connection's plan is currently displayed.
  //   - `$effect` below fires on first mount AND whenever routeState
  //     params change. Tracking the previous id in a non-reactive local
  //     keeps the closure clean without triggering Svelte's
  //     "state_referenced_locally" warning.
  /** @type {string|undefined} */
  let lastRouteId
  $effect(() => {
    const next = id
    if (!next) return
    if (next === lastRouteId) return
    lastRouteId = next
    loadForRoute(next)
  })

  async function runExplain() {
    if (!stmt || !id) return
    loading = true
    status = 'planning'
    error = null
    try {
      const r = await api.explain(id, stmt, { analyze })
      plan = r?.plan || null
      status = 'done'
      // Reset selection to the root.
      store.select('0')
    } catch (err) {
      error = { code: err?.code || 'unknown', message: err?.message || String(err) }
      status = 'error'
    } finally {
      loading = false
    }
  }

  function onAnalyzeChange(next) {
    analyze = next
    runExplain()
  }

  function onToggleRaw() { store.setRaw(!treeState.showRaw) }

  function onOpenInEditor() {
    // Round-trip back to the SQL editor with the same statement loaded.
    try {
      sessionStorage.setItem('explain:return', JSON.stringify({ stmt }))
    } catch { /* ignore */ }
    navigate('/connections/' + encodeURIComponent(id) + '/query')
  }

  function onClose() {
    if (typeof history !== 'undefined' && document.referrer && document.referrer.includes(location.host)) {
      history.back()
    } else {
      navigate('/connections/' + encodeURIComponent(id) + '/query')
    }
  }

  function onRibbonFilter(kind) {
    if (kind === 'rows') store.toggleHotspot()
    // buffers / time / warnings actions deferred — popover lives in ribbon
  }

  // Selected node lookup (deep).
  const selectedNode = $derived(nodeAt(plan?.root, treeState.selectedId))

  function onScreenKeydown(ev) {
    if (ev.target && (ev.target.tagName === 'INPUT' || ev.target.tagName === 'TEXTAREA')) return
    if (ev.key === 'Escape') { onClose() }
    if (ev.key === 'h' || ev.key === 'H') { store.toggleHotspot() }
    if (ev.key === 'r' || ev.key === 'R') { onToggleRaw() }
    if ((ev.metaKey || ev.ctrlKey) && ev.key === 'Enter') {
      ev.preventDefault()
      runExplain()
    }
  }
</script>

<svelte:window onkeydown={onScreenKeydown} />

<div class="explain-inspector">
  <header class="explain-inspector__toolbar">
    <div class="explain-inspector__crumb">
      <span class="explain-inspector__crumbLabel">Aura DB</span>
      <span class="explain-inspector__crumbSep">/</span>
      <span class="explain-inspector__crumbConn">{conn?.name || id}</span>
      <span class="explain-inspector__crumbSep">/</span>
      <strong>EXPLAIN</strong>
      <span class="explain-inspector__engine" data-engine={conn?.engine || ''}>{conn?.engine || ''}</span>
      <StatusDot
        state={status === 'error' ? 'down' : (status === 'done' ? 'ok' : (status === 'idle' ? 'idle' : 'warn'))}
        pulse={status === 'planning' || status === 'running'}
        title={status}
      />
    </div>
    <div class="explain-inspector__actions">
      <AnalyzeToggle value={analyze} stmt={stmt} connId={id} currentClass={initialClass} onChange={onAnalyzeChange} />
      <Btn variant="primary" onclick={runExplain} disabled={loading || !stmt}>
        {#if loading}<Spinner size={10} />{/if}
        Re-run
      </Btn>
      <Btn variant="ghost" onclick={onToggleRaw}>{treeState.showRaw ? 'Tree' : 'RAW'}</Btn>
      <Btn variant="ghost" onclick={onOpenInEditor}>Open in SQL editor</Btn>
      <Btn variant="ghost" onclick={onClose}>Close</Btn>
    </div>
  </header>

  {#if connLoading}
    <div class="explain-inspector__loading">Loading connection…</div>
  {:else if error}
    <div class="explain-inspector__error" role="alert">
      <strong class="explain-inspector__errorCode">{error.code}</strong>
      <span class="explain-inspector__errorMsg">{error.message}</span>
      <Btn variant="ghost" onclick={runExplain}>Retry</Btn>
    </div>
  {:else if !plan && !loading}
    <EmptyState
      title="No plan yet"
      body="Send a statement from the SQL editor (Cmd+E) to inspect its plan."
    />
  {:else if plan}
    <MetricsRibbon {plan} onFilter={onRibbonFilter} />

    <div class="explain-inspector__body">
      <div class="explain-inspector__left">
        <WarningBanner warnings={plan.warnings || []} />
        {#if treeState.showRaw}
          <RawPlanView raw={plan.raw} />
        {:else}
          <FlameTree
            plan={plan}
            selectedId={treeState.selectedId}
            expanded={treeState.expanded}
            searchTerm={treeState.searchTerm}
            hotspotMode={treeState.hotspotMode}
            onSelect={(id) => store.select(id)}
            onToggleExpand={(id) => store.toggleExpand(id)}
            onExpandAll={(ids) => store.expandAll(ids)}
          />
        {/if}
      </div>
      <div class="explain-inspector__right">
        <NodeDetail
          node={selectedNode}
          engine={plan.engine}
          analyzed={plan.engine === 'postgres' && (plan.executionTimeMs || 0) > 0}
          planWarnings={plan.warnings || []}
        />
      </div>
    </div>

    <div class="explain-inspector__sqlbar" title={stmt}>
      <code class="explain-inspector__sqlcode">{stmt}</code>
    </div>
  {:else}
    <div class="explain-inspector__loading"><Spinner size={14} /> Planning…</div>
  {/if}
</div>

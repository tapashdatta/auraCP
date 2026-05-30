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
  /** @type {'idle'|'planning'|'running'|'done'|'error'|'aborted'} */
  let status = $state('idle')

  // FIX INT-7 / DC-5 (PR #14.5): track the in-flight AbortController so
  // the user can cancel a runaway 60s EXPLAIN, and a "still running…"
  // hint can escalate after a soft deadline. Without this, the initial
  // fetch on a deeplink could leave a blank screen for up to 60s with
  // no spinner and no abort.
  /** @type {AbortController|null} */
  let abortCtrl = null
  let elapsedMs = $state(0)
  /** @type {any} */
  let elapsedTimer = null
  const SOFT_TIMEOUT_MS = 5000

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
    store.setSearch('')

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

  // FIX INT-5 (PR #14.5): keep document.title in sync with the loaded
  // connection so multi-tab users can tell which connection's plan is
  // up. Restores the prior title on unmount so we don't pollute other
  // routes' tab strips.
  let _priorTitle = ''
  $effect(() => {
    if (typeof document === 'undefined') return
    if (!_priorTitle) _priorTitle = document.title
    const label = conn?.name || id || ''
    document.title = label ? `EXPLAIN · ${label} · Aura DB` : 'EXPLAIN · Aura DB'
    return () => {
      if (typeof document !== 'undefined' && _priorTitle) {
        document.title = _priorTitle
      }
    }
  })

  // FIX CORR-9 (PR #14.5): hydrate `initialClass` once from the
  // handoff payload, but if the operator edits the statement upstream
  // (or follows a deeplink without a class hint), AnalyzeToggle's
  // confirm-gate would otherwise key off a stale read/write classification.
  // We re-classify whenever `stmt` changes via api.classifySql so the
  // toggle's gate matches the actual statement. The server still owns
  // the security boundary (handleQuery re-classifies before dispatch)
  // — this is purely UX so a deep-linked DELETE shows the typed-ANALYZE
  // gate even before Re-run is hit.
  /** @type {string|null} */
  let _lastClassifiedStmt = null
  $effect(() => {
    const s = stmt
    if (!s || !id) return
    if (s === _lastClassifiedStmt) return
    _lastClassifiedStmt = s
    api.classifySql(id, s).then((r) => {
      // Tolerate response shape variance: server returns either
      // {class}|{statements:[{class}]}|{parsed:{class}}.
      const next = r?.class || r?.parsed?.class || r?.statements?.[0]?.class
      if (typeof next === 'string' && next) {
        initialClass = next
      }
    }).catch(() => { /* UX-only; server re-classify is the gate */ })
  })

  async function runExplain() {
    if (!stmt || !id) return
    // Cancel any prior in-flight call so a rapid Re-run can't race.
    if (abortCtrl) {
      try { abortCtrl.abort() } catch { /* ignore */ }
    }
    abortCtrl = new AbortController()
    const ctrl = abortCtrl
    loading = true
    status = 'planning'
    error = null
    elapsedMs = 0
    if (elapsedTimer) { clearInterval(elapsedTimer); elapsedTimer = null }
    const startedAt = (typeof performance !== 'undefined') ? performance.now() : Date.now()
    elapsedTimer = setInterval(() => {
      const now = (typeof performance !== 'undefined') ? performance.now() : Date.now()
      elapsedMs = Math.round(now - startedAt)
    }, 250)
    try {
      const r = await api.explain(id, stmt, { analyze, signal: ctrl.signal })
      // Drop the response if it returned for an aborted/superseded call.
      if (ctrl !== abortCtrl || ctrl.signal.aborted) return
      plan = r?.plan || null
      status = 'done'
      // Reset selection to the root.
      store.select('0')
    } catch (err) {
      if (ctrl.signal.aborted) {
        status = 'aborted'
      } else {
        error = { code: err?.code || 'unknown', message: err?.message || String(err) }
        status = 'error'
      }
    } finally {
      if (elapsedTimer) { clearInterval(elapsedTimer); elapsedTimer = null }
      if (ctrl === abortCtrl) {
        loading = false
        abortCtrl = null
      }
    }
  }

  function abortInFlight() {
    if (!abortCtrl) return
    try { abortCtrl.abort() } catch { /* ignore */ }
  }

  function onAnalyzeChange(next) {
    analyze = next
    runExplain()
  }

  function onToggleRaw() { store.setRaw(!treeState.showRaw) }

  // FIX INT-3 / INT-6 (PR #14.5): the previous editor-return write
  // was dead code (no reader on the editor side). Replace with the
  // INT-6 mechanism: stash the current stmt under
  // `editor:restore:<connId>` so the editor's onMount drains it and
  // restores the doc. Both the explicit "Open in SQL editor" button
  // and the implicit browser-back path hit `stashForEditor` so the
  // editor's docText is never silently destroyed by a navigation.
  function stashForEditor() {
    if (!id || !stmt) return
    try {
      sessionStorage.setItem('editor:restore:' + id, JSON.stringify({ stmt }))
    } catch { /* ignore */ }
  }

  function onOpenInEditor() {
    stashForEditor()
    navigate('/connections/' + encodeURIComponent(id) + '/query')
  }

  function onClose() {
    stashForEditor()
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

  // FIX CORR-12 (PR #14.5): surface an inline marker at the foot of
  // the tree when the server flagged truncation. The WarningBanner
  // also mentions it, but operators scrolling the tree need a marker
  // at the actual cut-off point so they don't read the partial tree
  // as complete. We detect via substring match on the warning text
  // (server formats it as "plan tree truncated at ...").
  const isTruncated = $derived(
    Array.isArray(plan?.warnings) && plan.warnings.some((w) => typeof w === 'string' && /\btruncated\b/i.test(w)),
  )

  // FIX A11Y-13 (PR #14.5): search UI lives in the toolbar; the store
  // already owns `searchTerm`. We bind the input to the store via
  // setSearch so the FlameTree's match-or-dim layer activates.
  /** @type {HTMLInputElement|undefined} */
  let searchInputEl = $state(undefined)
  function onSearchInput(ev) {
    store.setSearch(ev.currentTarget.value || '')
  }
  function onClearSearch() {
    store.setSearch('')
    queueMicrotask(() => { searchInputEl?.focus() })
  }

  // FIX INT-8 / A11Y-10 / INT-10 (PR #14.5): keymap scoping +
  // discoverability.
  //   - Bare `h` / `r` collided with typing in the (future) search
  //     input, the focus-trapped Analyze confirm dialog, or any
  //     contenteditable. We now ignore the key when:
  //       (a) the event target is an editable surface (input /
  //           textarea / contenteditable), OR
  //       (b) any modifier other than Shift is held — leaving
  //           Shift+H / Shift+R as the discoverable form (paired with
  //           an aria-keyshortcuts attribute on the button).
  //   - Cmd+E on this screen is now a documented re-run shortcut
  //     (paired with the cross-route Cmd+E on the editor that opens
  //     the inspector) and the button carries aria-keyshortcuts so AT
  //     users can discover it via the rotor.
  function _isEditingTarget(t) {
    if (!t) return false
    if (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT') return true
    if (t.isContentEditable) return true
    return false
  }
  function onScreenKeydown(ev) {
    if (_isEditingTarget(ev.target)) return
    if (ev.key === 'Escape') { onClose(); return }
    // Cmd/Ctrl+Enter or Cmd/Ctrl+E → Re-run (paired with editor's Cmd+E).
    if ((ev.metaKey || ev.ctrlKey) && (ev.key === 'Enter' || ev.key === 'e' || ev.key === 'E')) {
      ev.preventDefault()
      runExplain()
      return
    }
    // Cmd/Ctrl+F or `/` → focus search.
    if (((ev.metaKey || ev.ctrlKey) && (ev.key === 'f' || ev.key === 'F')) || ev.key === '/') {
      // `/` is bare-key but we already gated editing targets above,
      // and it's a long-standing search convention (Vim / GitHub).
      ev.preventDefault()
      queueMicrotask(() => { searchInputEl?.focus() })
      return
    }
    // Hotspot / Raw — require Shift so plain `h` / `r` doesn't fire.
    if (ev.shiftKey && !ev.metaKey && !ev.ctrlKey && !ev.altKey) {
      if (ev.key === 'H') { store.toggleHotspot(); return }
      if (ev.key === 'R') { onToggleRaw(); return }
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
      <!-- A11Y-13 / CORR-13: search box wired to the store. -->
      <label class="explain-inspector__search">
        <input
          bind:this={searchInputEl}
          class="explain-inspector__searchInput"
          type="search"
          placeholder="Search nodes…"
          value={treeState.searchTerm}
          oninput={onSearchInput}
          aria-label="Search plan nodes"
          aria-keyshortcuts="/ Control+F Meta+F"
          spellcheck="false"
          autocomplete="off"
        />
        {#if treeState.searchTerm}
          <button type="button" class="explain-inspector__searchKbd" onclick={onClearSearch} aria-label="Clear search">×</button>
        {:else}
          <span class="explain-inspector__searchKbd" aria-hidden="true">/</span>
        {/if}
      </label>
      <AnalyzeToggle value={analyze} stmt={stmt} connId={id} currentClass={initialClass} onChange={onAnalyzeChange} />
      <!-- INT-10 / A11Y-10 (PR #14.5): Cmd/Ctrl+E is the documented
           re-run shortcut on this screen (mirrors the editor's Cmd+E
           that opens the inspector). Reason= surfaces it as a tooltip
           and aria-describedby on the button; AT discovery of the
           shortcut itself lives on the wrapper span below. -->
      <Btn variant="primary" onclick={runExplain} disabled={loading || !stmt} reason="Re-run EXPLAIN — Cmd/Ctrl+E or Cmd/Ctrl+Enter">
        {#if loading}<Spinner size={10} />{/if}
        Re-run
      </Btn>
      <!-- DC-8: promote the RAW toggle to a 2-button tabbar so the
           Tree↔RAW switch matches the convention from DataGrip /
           pgMustard (tabs, not a flip button). aria-pressed carries
           the toggled state for AT. -->
      <div class="explain-inspector__tabbar" role="tablist" aria-label="Plan view">
        <button type="button" role="tab" aria-selected={!treeState.showRaw} onclick={() => store.setRaw(false)}>Tree</button>
        <button type="button" role="tab" aria-selected={treeState.showRaw} onclick={() => store.setRaw(true)} aria-keyshortcuts="Shift+R">RAW</button>
      </div>
      <Btn variant="ghost" onclick={onOpenInEditor}>Open in SQL editor</Btn>
      <Btn variant="ghost" onclick={onClose} reason="Close — Esc">Close</Btn>
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
  {:else if status === 'aborted' && !plan}
    <EmptyState
      title="EXPLAIN cancelled"
      body="Hit Re-run to try again, or edit the statement in the SQL editor."
    />
  {:else if loading && !plan}
    <!-- FIX DC-5 + INT-7 (PR #14.5): initial fetch was a blank screen.
         Render a spinner + elapsed-time counter + Abort affordance.
         The hint escalates from neutral to warn after SOFT_TIMEOUT_MS
         so the operator knows the request is unusually slow. -->
    <div class="explain-inspector__loading explain-inspector__loading--with-cancel" role="status" aria-live="polite">
      <span><Spinner size={14} /> Planning… <span
        class="explain-inspector__timeoutHint"
        class:explain-inspector__timeoutHint--late={elapsedMs >= SOFT_TIMEOUT_MS}
      >{(elapsedMs / 1000).toFixed(1)}s elapsed{elapsedMs >= SOFT_TIMEOUT_MS ? ' — server is slow' : ''}</span></span>
      <span class="explain-inspector__abort">
        <Btn variant="ghost" onclick={abortInFlight} reason="Cancel the in-flight EXPLAIN">Abort</Btn>
      </span>
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
          {#if isTruncated}
            <div class="flame-tree__truncated" role="note">
              <span aria-hidden="true">…</span>
              <span>Tree truncated by the server cap — see RAW or the warning banner for the full payload.</span>
            </div>
          {/if}
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

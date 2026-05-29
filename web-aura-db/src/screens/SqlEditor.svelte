<script>
  // Aura DB SQL editor screen (PR #13).
  //
  // Composition:
  //   - CodeMirrorPane: CM6 editor with engine-aware highlighting,
  //     autocomplete (schemaCache-backed), and a keymap (Cmd+Enter,
  //     Cmd+Shift+Enter, Cmd+., Cmd+Shift+F, Cmd+S).
  //   - ExecuteToolbar: connection breadcrumb, classifier chip, Execute /
  //     Cancel / Format / Save buttons.
  //   - ResultsPane: tab-per-execution result viewer (ResultGrid for
  //     SELECTs, MessagePanel for writes, ErrorPanel for failures).
  //   - WorkbenchSidebar: Schema (collapsible tree), History, Saved.
  //
  // Streaming: results arrive via sqlStream.exec(...). The exec handle
  // is registered in execRegistry so Cmd+. / Cancel button can reach it.
  //
  // Bundle: CodeMirror is statically imported here (first paint needs
  // it). sql-formatter is dynamically imported only when Format is hit.

  import { onMount, onDestroy } from 'svelte'
  import { routeState, navigate } from '../lib/router.svelte.js'
  import { api } from '../lib/api.js'
  import { connections } from '../lib/connections.svelte.js'
  import { sqlStream } from '../lib/sqlStream.js'
  import { t } from '../lib/strings.js'

  import CodeMirrorPane from '../lib/components/sqlEditor/CodeMirrorPane.svelte'
  import ClassifierChip from '../lib/components/sqlEditor/ClassifierChip.svelte'
  import ResultGrid from '../lib/components/sqlEditor/ResultGrid.svelte'
  import MessagePanel from '../lib/components/sqlEditor/MessagePanel.svelte'
  import ErrorPanel from '../lib/components/sqlEditor/ErrorPanel.svelte'
  import Btn from '../lib/components/Btn.svelte'
  import EmptyState from '../lib/components/EmptyState.svelte'

  import { createClassifierStore } from '../lib/sqlEditor/classifier.svelte.js'
  import { splitStatements, getStatementAtCursor } from '../lib/sqlEditor/splitStatements.js'
  import { register as registerExec, complete as completeExec, cancel as cancelExec, cancelAll as cancelAllExec, isExecuting } from '../lib/sqlEditor/execRegistry.svelte.js'
  import { loadSchemas, setEngine as setCacheEngine, invalidate as invalidateSchemaCache } from '../lib/sqlEditor/schemaCache.svelte.js'

  const id = $derived(routeState.params.id)
  /** @type {ReturnType<typeof createClassifierStore> | null} */
  let classifier = $state(null)
  /** @type {CodeMirrorPane|undefined} */
  let editorRef = $state(undefined)

  // Connection meta — engine drives CM dialect + classifier.
  /** @type {{id:string, engine:'mariadb'|'postgres', name?:string}|null} */
  let conn = $state(null)
  let connLoading = $state(true)

  let docText = $state('')
  let cursorPos = $state(0)

  // Result tabs (one per executed statement). Capped at 8, FIFO eviction.
  /**
   * @typedef {object} ResultTab
   * @property {string} id
   * @property {string} sql
   * @property {'executing'|'streaming'|'done'|'error'|'cancelled'} state
   * @property {Array<{name:string, databaseTypeName?:string}>} columns
   * @property {any[][]} rows
   * @property {number} durationMs
   * @property {number} affected
   * @property {string} errorCode
   * @property {string} errorMessage
   * @property {string} klass
   */
  /** @type {ResultTab[]} */
  let tabs = $state([])
  let activeTabId = $state('')

  /** @type {Array<{id:string|number, sql:string, durationMs:number, class:string}>} */
  let history = $state([])
  /** @type {Array<{id:string, name:string, statement:string, tags?:string[]}>} */
  let saved = $state([])

  let sidebarOpen = $state(true)
  let formatting = $state(false)
  /** @type {string} */
  let statusMsg = $state('')

  // ─── lifecycle ──────────────────────────────────────────────────
  let lastConnId = $state('')

  async function initConnection(connectionId) {
    connLoading = true
    try {
      const c = await api.getConnection(connectionId)
      const engine = (c?.engine === 'postgres' || c?.engine === 'postgresql') ? 'postgres' : 'mariadb'
      conn = { id: c?.id || connectionId, engine, name: c?.name }
      setCacheEngine(connectionId, engine)
      classifier = createClassifierStore({ connId: connectionId, engine })
      // Prefetch schemas for autocomplete.
      loadSchemas(connectionId).catch(() => {})
      // Eager fetch history + saved (cheap, in-memory mostly).
      refreshHistory()
      refreshSaved()
    } catch (err) {
      conn = { id: connectionId, engine: 'mariadb', name: '' }
      classifier = createClassifierStore({ connId: connectionId, engine: 'mariadb' })
    } finally {
      connLoading = false
    }
  }

  onMount(async () => {
    lastConnId = id
    await initConnection(id)
  })

  // EXEC-6: connection switch via route change must flush the engine /
  // classifier / schemaCache and cancel any in-flight execs so stale
  // state from the prior connection cannot bleed into the new one.
  $effect(() => {
    const cid = id
    if (!cid || cid === lastConnId) return
    // Tear down in-flight work + cached state for the previous conn.
    cancelAllExec()
    classifier?.reset()
    invalidateSchemaCache(lastConnId)
    // Drop result tabs (they belong to the old connection).
    tabs = []
    activeTabId = ''
    history = []
    saved = []
    docText = ''
    cursorPos = 0
    statusMsg = ''
    lastConnId = cid
    initConnection(cid)
  })

  onDestroy(() => {
    cancelAllExec()
    classifier?.cancel()
  })

  // ─── classifier wire-up ──────────────────────────────────────────
  function onDocChange(next) {
    docText = next
    classifier?.update(next)
  }

  const currentStatement = $derived.by(() => {
    const stmts = splitStatements(docText)
    return getStatementAtCursor(stmts, cursorPos) || (stmts.length === 1 ? stmts[0] : null)
  })

  // The class to display in the toolbar: prefer the cursor-statement's
  // class from the parsed query when offsets match; else use overall.
  const currentClass = $derived.by(() => {
    const p = classifier?.state.parsed
    if (!p) return 'unknown'
    if (!currentStatement) return p.class
    const s = p.statements.find((st) => st.offset === currentStatement.start)
    return s ? s.class : p.class
  })

  const isForbidden = $derived(currentClass === 'forbidden')

  // ─── execute lifecycle ────────────────────────────────────────────
  function newTabId() { return 'rt_' + Math.random().toString(36).slice(2, 10) }

  function pushTab(tab) {
    let list = tabs.concat([tab])
    // EXEC-4: FIFO eviction must cancel the evicted tab's exec handle —
    // otherwise the server keeps streaming for a tab the UI dropped.
    while (list.length > 8) {
      const evicted = list.shift()
      if (evicted && (evicted.state === 'executing' || evicted.state === 'streaming')) {
        try { cancelExec(evicted.id) } catch { /* ignore */ }
      }
    }
    tabs = list
    activeTabId = tab.id
  }

  // EXEC-5: once a tab is in a terminal state (cancelled / done / error),
  // late frames must not flip it back. This guard is the single point of
  // truth for terminality.
  function isTerminal(state) {
    return state === 'cancelled' || state === 'done' || state === 'error'
  }

  function updateTab(tabId, patch) {
    tabs = tabs.map((t) => {
      if (t.id !== tabId) return t
      if (isTerminal(t.state) && patch.state !== t.state) {
        // Already terminal — drop the late update.
        return t
      }
      return { ...t, ...patch }
    })
  }

  function appendRow(tabId, row) {
    tabs = tabs.map((t) => {
      if (t.id !== tabId) return t
      // EXEC-5: terminal tabs (esp. cancelled) drop late row frames.
      if (isTerminal(t.state)) return t
      // EXEC-7: in-place push avoids the O(n^2) spread-into-new-array
      // anti-pattern. We still re-emit the tab object so $state reacts.
      t.rows.push(row)
      return { ...t, rows: t.rows }
    })
  }

  // EXEC-1: cursorOverride lets the keymap handler pass the freshest
  // cursor offset from the CM6 view, side-stepping any timing window
  // between selectionSet → onCursor → cursorPos $state propagation.
  function resolveStatement(cursorOverride) {
    const stmts = splitStatements(docText)
    const cur = (typeof cursorOverride === 'number') ? cursorOverride : (editorRef?.getCursor?.() ?? cursorPos)
    return getStatementAtCursor(stmts, cur) || (stmts.length === 1 ? stmts[0] : null)
  }

  async function runOne(sqlText, klass) {
    if (!conn) return null
    const tabId = newTabId()
    /** @type {ResultTab} */
    const tab = {
      id: tabId,
      sql: sqlText,
      state: 'executing',
      columns: [],
      rows: [],
      durationMs: 0,
      affected: 0,
      errorCode: '',
      errorMessage: '',
      klass: klass || 'unknown',
    }
    pushTab(tab)
    statusMsg = 'executing…'

    return new Promise((resolve) => {
      try {
        const handle = sqlStream.exec(conn.id, sqlText)
        registerExec(tabId, handle)
        handle
          .onMeta((f) => {
            updateTab(tabId, { columns: f.columns || [], state: 'streaming' })
          })
          .onRow((values) => {
            appendRow(tabId, values)
          })
          .onEnd((f) => {
            updateTab(tabId, {
              state: 'done',
              durationMs: f.durationMs || 0,
              affected: f.affected || 0,
            })
            completeExec(tabId)
            statusMsg = `done in ${f.durationMs || 0}ms`
            refreshHistory()
            // INT-2: invalidate schemaCache after a successful DDL or
            // DANGEROUS statement so autocomplete refetches the new
            // shape on next listObjects.
            try { maybeInvalidateSchemaAfter(klass) } catch { /* ignore */ }
            resolve({ ok: true })
          })
          .onError((e) => {
            updateTab(tabId, {
              state: 'error',
              errorCode: e.code || 'error',
              errorMessage: e.message || '',
            })
            completeExec(tabId)
            statusMsg = `error: ${e.code}`
            resolve({ ok: false, error: e })
          })
      } catch (err) {
        updateTab(tabId, { state: 'error', errorCode: 'client_error', errorMessage: String(err) })
        resolve({ ok: false, error: err })
      }
    })
  }

  function maybeInvalidateSchemaAfter(klass) {
    // INT-2: a DDL or DANGEROUS write may have mutated the schema shape.
    if (klass === 'ddl' || klass === 'dangerous') {
      invalidateSchemaCache(conn?.id || id)
      // Re-prime the cache lazily; autocomplete will fetch on next use.
    }
  }

  // EXEC-1: execCurrent now accepts an explicit cursor (from CM6
  // keymap) so it isn't stuck on the FIRST statement when the user
  // moves the caret faster than $state propagates.
  async function execCurrent(_view, cursorOverride) {
    if (!conn) return
    const stmt = resolveStatement(cursorOverride)
    if (!stmt) { statusMsg = 'no statement under cursor'; return }
    // Resolve the per-statement class against the classifier parse.
    const p = classifier?.state.parsed
    let klass = currentClass
    if (p) {
      const m = p.statements.find((st) => st.offset === stmt.start)
      if (m) klass = m.class
    }
    if (klass === 'forbidden') {
      statusMsg = 'statement is forbidden — refusing'
      return
    }
    // EXEC-3: a stable per-screen exec slot would also work but we keep
    // a fresh tabId per execute. The race is solved by cancelling any
    // still-active tabs whose state has not terminated yet.
    cancelStillRunning()
    await runOne(stmt.trimmedText, klass)
  }

  // EXEC-2: run every statement in the buffer sequentially, aborting
  // on first error/cancel. Distinct from execCurrent — the keymap wires
  // Mod-Shift-Enter directly to this.
  async function execAll() {
    if (!conn) return
    const stmts = splitStatements(docText)
    if (stmts.length === 0) { statusMsg = 'no statements'; return }
    cancelStillRunning()
    const p = classifier?.state.parsed
    for (const stmt of stmts) {
      let klass = 'unknown'
      if (p) {
        const m = p.statements.find((st) => st.offset === stmt.start)
        if (m) klass = m.class
      }
      if (klass === 'forbidden') {
        statusMsg = 'aborted — forbidden statement in batch'
        return
      }
      const r = await runOne(stmt.trimmedText, klass)
      if (!r || !r.ok) {
        statusMsg = 'aborted — prior statement failed'
        return
      }
    }
  }

  // PR #14: hand off the cursor statement to the EXPLAIN inspector. The
  // statement travels via sessionStorage (not the URL — explain payloads
  // can exceed URL length limits + don't need to be shareable yet); the
  // route push is a clean #/connections/{id}/explain.
  function openExplain(_view, cursorOverride) {
    if (!conn) return
    const stmt = resolveStatement(cursorOverride)
    if (!stmt) { statusMsg = 'no statement under cursor'; return }
    let klass = currentClass
    const p = classifier?.state.parsed
    if (p) {
      const m = p.statements.find((st) => st.offset === stmt.start)
      if (m) klass = m.class
    }
    if (klass === 'forbidden') {
      statusMsg = 'statement is forbidden — refusing'
      return
    }
    try {
      sessionStorage.setItem('explain:pending', JSON.stringify({
        connId: id,
        stmt: stmt.trimmedText,
        klass,
        analyze: false,
        fromHash: typeof location !== 'undefined' ? location.hash : '',
      }))
    } catch { /* ignore */ }
    navigate('/connections/' + encodeURIComponent(id) + '/explain')
  }

  function cancelStillRunning() {
    // EXEC-3: cancel any tab whose exec is still in flight so a
    // rapid double-Execute doesn't race two queries to the server.
    for (const t of tabs) {
      if (t.state === 'executing' || t.state === 'streaming') {
        try { cancelExec(t.id) } catch { /* ignore */ }
      }
    }
  }

  function cancelCurrent() {
    if (!activeTabId) return
    if (cancelExec(activeTabId)) {
      updateTab(activeTabId, { state: 'cancelled' })
      statusMsg = 'cancelled'
    }
  }

  async function formatDoc() {
    formatting = true
    try {
      const mod = await import('../lib/sqlEditor/sqlFormatter.js')
      const next = mod.format(docText, conn?.engine || 'mariadb')
      editorRef?.setDoc(next)
      docText = next
    } catch {
      statusMsg = 'formatter unavailable'
    } finally {
      formatting = false
    }
  }

  async function saveQuery() {
    if (!docText.trim()) return
    const name = (typeof prompt === 'function') ? prompt('Save query as:') : 'query'
    if (!name) return
    try {
      await api.saveQuery(id, { name, statement: docText, tags: [] })
      refreshSaved()
      statusMsg = `saved: ${name}`
    } catch (err) {
      statusMsg = 'save failed'
    }
  }

  // ─── sidebar data ───────────────────────────────────────────────
  async function refreshHistory() {
    try {
      const r = await api.connHistory(id)
      const list = (r && Array.isArray(r.entries)) ? r.entries : []
      history = list.map((e) => ({ id: e.id, sql: e.sql, durationMs: e.durationMs, class: e.class }))
    } catch { history = [] }
  }

  async function refreshSaved() {
    try {
      const r = await api.listSaved(id)
      saved = Array.isArray(r) ? r : []
    } catch { saved = [] }
  }

  function loadIntoEditor(sqlText) {
    editorRef?.setDoc(sqlText)
    docText = sqlText
    editorRef?.focus()
  }

  function toggleSidebar() { sidebarOpen = !sidebarOpen }

  // a11y-04: WAI-ARIA tabs roving-tabindex keyboard model.
  // ArrowLeft/Right cycle, Home/End jump, Enter/Space activate, Delete closes.
  function handleTablistKeydown(ev) {
    if (!tabs.length) return
    const idx = tabs.findIndex((t) => t.id === activeTabId)
    if (idx < 0) return
    /** @type {(next:number)=>void} */
    const focusAndSelect = (next) => {
      const n = ((next % tabs.length) + tabs.length) % tabs.length
      activeTabId = tabs[n].id
      // Focus the newly-active tab so the roving tabindex stays glued
      // to keyboard expectations.
      queueMicrotask(() => {
        const root = ev.currentTarget
        if (!root) return
        /** @type {HTMLElement|null} */
        const btn = root.querySelector(`[data-tab-index="${n}"]`)
        btn?.focus()
      })
    }
    switch (ev.key) {
      case 'ArrowRight':
        ev.preventDefault(); focusAndSelect(idx + 1); break
      case 'ArrowLeft':
        ev.preventDefault(); focusAndSelect(idx - 1); break
      case 'Home':
        ev.preventDefault(); focusAndSelect(0); break
      case 'End':
        ev.preventDefault(); focusAndSelect(tabs.length - 1); break
      case 'Delete': {
        ev.preventDefault()
        const target = tabs[idx]
        if (!target) return
        // Cancel in-flight before removing the tab so we don't leak the WS handle.
        if (target.state === 'executing' || target.state === 'streaming') {
          try { cancelExec(target.id) } catch { /* ignore */ }
        }
        const next = tabs.filter((t) => t.id !== target.id)
        tabs = next
        if (next.length > 0) {
          const ni = Math.min(idx, next.length - 1)
          activeTabId = next[ni].id
        } else {
          activeTabId = ''
        }
        break
      }
      default: break
    }
  }

  // ─── derived: current tab ─────────────────────────────────────
  const activeTab = $derived(tabs.find((t) => t.id === activeTabId) || null)
  const anyExecuting = $derived(tabs.some((t) => t.state === 'executing' || t.state === 'streaming'))
</script>

<div class="sql-editor">
  <header class="sql-editor__toolbar">
    <div class="sql-editor__brand">
      <strong>{t('sql.title')}</strong>
      <span class="sql-editor__sep">·</span>
      <span class="sql-editor__conn">{conn?.name || id}</span>
      <span class="sql-editor__engine" data-engine={conn?.engine || ''}>{conn?.engine || ''}</span>
    </div>
    <div class="sql-editor__chip">
      <ClassifierChip klass={currentClass} />
    </div>
    <div class="sql-editor__buttons">
      <Btn variant="primary" onclick={execCurrent} disabled={connLoading || isForbidden}>
        Execute <span class="sql-editor__kbd">⌘↵</span>
      </Btn>
      <Btn variant="ghost" onclick={openExplain} disabled={connLoading || isForbidden || !currentStatement}>
        Explain <span class="sql-editor__kbd">⌘E</span>
      </Btn>
      {#if anyExecuting}
        <Btn variant="danger" onclick={cancelCurrent}>Cancel</Btn>
      {/if}
      <Btn variant="ghost" onclick={formatDoc} loading={formatting}>Format</Btn>
      <Btn variant="ghost" onclick={saveQuery}>Save</Btn>
      <Btn variant="ghost" onclick={toggleSidebar} aria-label="toggle sidebar">{sidebarOpen ? '▶' : '◀'}</Btn>
    </div>
  </header>

  <div class="sql-editor__body" class:sql-editor__body--with-sidebar={sidebarOpen}>
    <div class="sql-editor__center">
      <div class="sql-editor__editor" data-testid="editor-shell">
        {#if connLoading}
          <div style="padding:16px;color:var(--text-mute,#888)">Loading connection…</div>
        {:else if conn}
          <CodeMirrorPane
            bind:this={editorRef}
            doc={docText}
            engine={conn.engine}
            connId={id}
            onChange={onDocChange}
            onCursor={(p) => { cursorPos = p }}
            onExecute={(v, pos) => execCurrent(v, pos)}
            onExecuteAll={() => execAll()}
            onExplain={(v, pos) => openExplain(v, pos)}
            onCancel={cancelCurrent}
            onFormat={formatDoc}
            onSave={saveQuery}
          />
        {/if}
      </div>

      <div class="sql-editor__status" aria-live="polite">
        {statusMsg}
      </div>

      <div class="sql-editor__results" data-testid="results-pane">
        <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
        <!-- svelte-ignore a11y_interactive_supports_focus -->
        <div
          class="sql-editor__tabs"
          role="tablist"
          aria-label="Result tabs"
          aria-orientation="horizontal"
          tabindex="-1"
          onkeydown={handleTablistKeydown}
        >
          {#each tabs as tb, i (tb.id)}
            <button
              class="sql-editor__tab"
              class:sql-editor__tab--active={activeTabId === tb.id}
              role="tab"
              id={`result-tab-${tb.id}`}
              aria-selected={activeTabId === tb.id}
              aria-controls={`result-panel-${tb.id}`}
              tabindex={activeTabId === tb.id ? 0 : -1}
              data-tab-index={i}
              onclick={() => { activeTabId = tb.id }}
              title={tb.sql}
            >
              <span class="sql-editor__tabClass" data-class={tb.klass}>{tb.klass}</span>
              <span class="sql-editor__tabSql">{tb.sql.slice(0, 28)}</span>
              <span class="sql-editor__tabState">{tb.state === 'streaming' ? `· ${tb.rows.length} rows` : tb.state === 'executing' ? '· …' : ''}</span>
            </button>
          {/each}
        </div>
        <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
        <div
          class="sql-editor__tabBody"
          role={activeTab ? 'tabpanel' : undefined}
          id={activeTab ? `result-panel-${activeTab.id}` : undefined}
          aria-labelledby={activeTab ? `result-tab-${activeTab.id}` : undefined}
          tabindex={activeTab ? 0 : undefined}
        >
          {#if !activeTab}
            <EmptyState title="No results yet" body="Hit ⌘↵ to execute the statement under the cursor." />
          {:else if activeTab.state === 'error'}
            <ErrorPanel code={activeTab.errorCode} message={activeTab.errorMessage} onRetry={() => execCurrent()} />
          {:else if activeTab.state === 'cancelled'}
            <MessagePanel message="Query cancelled" rowsAffected={activeTab.rows.length} durationMs={activeTab.durationMs} />
          {:else if activeTab.columns.length === 0 && activeTab.state === 'done'}
            <MessagePanel rowsAffected={activeTab.affected} durationMs={activeTab.durationMs} />
          {:else}
            <ResultGrid
              columns={activeTab.columns}
              rows={activeTab.rows}
              streamingActive={activeTab.state === 'streaming' || activeTab.state === 'executing'}
            />
          {/if}
        </div>
      </div>
    </div>

    {#if sidebarOpen}
      <aside class="sql-editor__sidebar" aria-label="workbench sidebar">
        <section class="sql-editor__accordion">
          <h3>History</h3>
          {#if history.length === 0}
            <p class="sql-editor__empty">No history</p>
          {:else}
            <ul class="sql-editor__list">
              {#each history.slice(0, 25) as h (h.id)}
                <li>
                  <button class="sql-editor__listBtn" onclick={() => loadIntoEditor(h.sql)} title={h.sql}>
                    <span class="sql-editor__listClass" data-class={h.class}>{h.class}</span>
                    <span class="sql-editor__listSql">{h.sql.slice(0, 60)}</span>
                  </button>
                </li>
              {/each}
            </ul>
          {/if}
        </section>
        <section class="sql-editor__accordion">
          <h3>Saved</h3>
          {#if saved.length === 0}
            <p class="sql-editor__empty">No saved queries</p>
          {:else}
            <ul class="sql-editor__list">
              {#each saved as s (s.id)}
                <li>
                  <button class="sql-editor__listBtn" onclick={() => loadIntoEditor(s.statement)} title={s.statement}>
                    <span class="sql-editor__listName">{s.name}</span>
                  </button>
                </li>
              {/each}
            </ul>
          {/if}
        </section>
      </aside>
    {/if}
  </div>
</div>


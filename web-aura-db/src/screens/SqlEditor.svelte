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
  import SaveQueryModal from '../lib/components/sqlEditor/SaveQueryModal.svelte'
  import ConfirmDialog from '../lib/components/ConfirmDialog.svelte'
  import Btn from '../lib/components/Btn.svelte'
  import EmptyState from '../lib/components/EmptyState.svelte'
  import { pushToast } from '../lib/toastBus.svelte.js'

  import { createClassifierStore } from '../lib/sqlEditor/classifier.svelte.js'
  import { splitStatements, getStatementAtCursor } from '../lib/sqlEditor/splitStatements.js'
  import { register as registerExec, complete as completeExec, cancel as cancelExec, cancelAll as cancelAllExec, isExecuting } from '../lib/sqlEditor/execRegistry.svelte.js'
  import { loadSchemas, setEngine as setCacheEngine, invalidate as invalidateSchemaCache } from '../lib/sqlEditor/schemaCache.svelte.js'
  // PR #15: cross-route handoff from the palette / history screen. Mirror of openExplain.
  import { consumePending } from '../lib/replay.js'
  // C1: when the editor is already mounted for the target connection,
  // onMount won't fire on the next replayInEditor call — watch the
  // reactive tick bus and consume on each bump.
  import { editorPending } from '../lib/palette.svelte.js'

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
  // a11y-14 partial: a separate error live region so transient status
  // messages ("executing…", "done in 12ms") don't share the same
  // polite live region as errors ("error: …"). Errors get an
  // assertive region so AT interrupts; status stays polite.
  /** @type {string} */
  let errorMsg = $state('')

  // a11y-07: sidebar accordions are now collapsible with aria-expanded.
  let historyOpen = $state(true)
  let savedOpen = $state(true)

  // EXEC-9 / a11y-10: native window.prompt() replaced with a Svelte
  // modal that captures name, description, tags, and surfaces a
  // duplicate-name "Replace" affordance.
  let saveModalOpen = $state(false)
  // EXEC-10: dirty-check before load-into-editor REPLACES the buffer
  // — track a pending sql payload and re-fire after confirm.
  /** @type {string} */
  let pendingLoadSql = $state('')
  let confirmLoadOpen = $state(false)
  // a11y-08: keyboard-deletable saved query — confirm before delete.
  /** @type {{id:string, name:string}|null} */
  let pendingDelete = $state(null)
  // INT-6: preload-on-hover state for the sql-formatter chunk.
  let formatterPreloaded = $state(false)

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
    // PR #15: replay handoff from palette / history. Stale or
    // conn-mismatched payloads are dropped silently inside consumePending.
    const pending = consumePending(id)
    if (pending && pending.statement) {
      // loadIntoEditor calls setDoc which assumes editorRef is mounted;
      // tick once to let CodeMirrorPane finish its onMount.
      queueMicrotask(() => { loadIntoEditor(pending.statement) })
      return
    }
    // FIX INT-6 (PR #14.5): the inspector writes the editor's prior
    // docText to `editor:restore:<connId>` when it navigates away (via
    // the "Open in SQL editor" button or browser-back). On mount we
    // drain that slot so the editor's content is restored — a
    // round-trip through the inspector no longer eats the user's
    // unsaved query.
    try {
      const raw = sessionStorage.getItem('editor:restore:' + id)
      if (raw) {
        sessionStorage.removeItem('editor:restore:' + id)
        const parsed = JSON.parse(raw)
        if (parsed && typeof parsed.stmt === 'string' && parsed.stmt) {
          queueMicrotask(() => { loadIntoEditor(parsed.stmt) })
        }
      }
    } catch {
      try { sessionStorage.removeItem('editor:restore:' + id) } catch { /* ignore */ }
    }
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

  // C1: react to palette / history-screen replay handoffs that fire
  // while this editor is already mounted for the target connection.
  // onMount only fires once per mount, so a same-conn replay can't be
  // picked up by the onMount-based consumePending path alone. We track
  // the bumped tick and ignore the initial value (set at mount via the
  // onMount path above).
  let _seenTick = $state(/** @type {number|null} */(null))
  $effect(() => {
    const t = editorPending.tick
    // Skip the first observation — onMount has already drained the slot.
    if (_seenTick === null) { _seenTick = t; return }
    if (t === _seenTick) return
    _seenTick = t
    const pending = consumePending(id)
    if (pending && pending.statement) {
      queueMicrotask(() => { loadIntoEditor(pending.statement) })
    }
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
      // EXEC-11: single finalize() so the history sidebar refreshes on
      // BOTH success and error paths (previously the error branch
      // skipped refreshHistory(), leaving the sidebar one-behind).
      const finalize = () => {
        completeExec(tabId)
        refreshHistory()
      }
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
            statusMsg = `done in ${f.durationMs || 0}ms`
            errorMsg = ''
            finalize()
            // INT-2: invalidate schemaCache after a successful DDL or
            // DANGEROUS statement so autocomplete refetches the new
            // shape on next listObjects.
            try { maybeInvalidateSchemaAfter(klass) } catch { /* ignore */ }
            resolve({ ok: true })
          })
          .onError((e) => {
            // EXEC-8: a mid-stream error must NOT clear the rows we
            // already streamed in — they're real, the server saw a
            // failure AFTER emitting them. The Cancel path already
            // keeps partial rows; this brings the error path in line
            // with that rule so the UX is consistent. The ResultGrid
            // is keyed off `activeTab.state` to switch from grid to
            // ErrorPanel, so we expose the partial rows count via the
            // status line and tell the user to switch back to the
            // result tab to see the partial set if they want.
            updateTab(tabId, {
              state: 'error',
              errorCode: e.code || 'error',
              errorMessage: e.message || '',
            })
            errorMsg = `error: ${e.code || 'failed'}${e.message ? ' — ' + e.message : ''}`
            statusMsg = ''
            finalize()
            resolve({ ok: false, error: e })
          })
      } catch (err) {
        updateTab(tabId, { state: 'error', errorCode: 'client_error', errorMessage: String(err) })
        errorMsg = `error: ${String(err)}`
        statusMsg = ''
        // No registered exec to finalize, but still refresh history in
        // case the server logged the attempt.
        refreshHistory()
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
    // INT-8: tab klass would be captured as 'unknown' when the user
    // hits Cmd+Enter during the classifier's 250ms debounce window.
    // Flush forces a synchronous classify so the per-tab label, the
    // forbidden gate, and the toolbar chip all settle BEFORE we push
    // the exec frame. flush() is a no-op when the doc is empty or
    // already classified.
    try { await classifier?.flush?.() } catch { /* ignore */ }
    // Resolve the per-statement class against the (now-flushed) parse.
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
    // INT-8: same flush rationale as execCurrent — class labels on
    // every spawned tab must be classifier-truth, not "unknown".
    try { await classifier?.flush?.() } catch { /* ignore */ }
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
    // FIX INT-4 (PR #14.5): the previous payload carried a `fromHash`
    // field that was never read by the inspector — pure dead bytes in
    // the handoff. Removed. INT-6 is solved separately by the inspector
    // saving the editor's docText back to `editor:restore:<id>` on
    // navigation so a browser-back restores the editor state without
    // a hash-based round-trip.
    try {
      sessionStorage.setItem('explain:pending', JSON.stringify({
        connId: id,
        stmt: stmt.trimmedText,
        klass,
        analyze: false,
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
      // EXEC-12: ask CodeMirrorPane to preserve the cursor on a
      // non-whitespace anchor rather than the raw byte offset, so the
      // user lands on the same token after Format — not a random
      // column three rows down.
      editorRef?.setDoc(next, { preserveCursor: 'semantic' })
      docText = next
    } catch {
      statusMsg = 'formatter unavailable'
    } finally {
      formatting = false
    }
  }

  // INT-6: preload the sql-formatter chunk on hover/focus so the first
  // Format click doesn't pay the ~76 KB gz fetch latency. The first
  // hover triggers the dynamic import; result is cached by the module
  // graph so subsequent invocations are free.
  function preloadFormatter() {
    if (formatterPreloaded) return
    formatterPreloaded = true
    import('../lib/sqlEditor/sqlFormatter.js').catch(() => {
      // Pre-fetch failure is non-fatal — Format itself will retry.
      formatterPreloaded = false
    })
  }

  // EXEC-9 / EXEC-13 / a11y-10: open the SaveQueryModal instead of a
  // native window.prompt. The modal handles the empty-doc case with a
  // visible message (EXEC-13) and the duplicate-name case with a
  // "Replace" affordance.
  function saveQuery() {
    if (!docText.trim()) {
      // EXEC-13: tell the user (the old impl silently returned).
      statusMsg = ''
      errorMsg = 'cannot save — editor buffer is empty'
      try { pushToast({ message: 'Nothing to save — editor is empty.', tone: 'warning' }) } catch { /* ignore */ }
      return
    }
    saveModalOpen = true
  }

  // Modal commit handler. `replace=true` means the user is overwriting
  // an existing name; we DELETE then POST to keep the storage shape
  // unchanged. If delete fails we still attempt the save and let the
  // server reject if it truly can't take a duplicate.
  async function commitSave(payload) {
    const { name, description, tags, replace } = payload
    if (replace) {
      const dup = saved.find((s) => s.name === name)
      if (dup && dup.id) {
        try { await api.deleteSaved(id, dup.id) } catch { /* ignore — best-effort */ }
      }
    }
    try {
      await api.saveQuery(id, {
        name,
        statement: docText,
        // server schema currently accepts tags + description optional.
        description,
        tags,
      })
      saveModalOpen = false
      refreshSaved()
      statusMsg = `saved: ${name}`
      errorMsg = ''
      try { pushToast({ message: `Saved query: ${name}`, tone: 'success' }) } catch { /* ignore */ }
    } catch (err) {
      errorMsg = 'save failed'
      try { pushToast({ message: 'Save failed — check connectivity.', tone: 'danger' }) } catch { /* ignore */ }
    }
  }

  // v0.3.2-A: star / unstar a saved query. Optimistic — flips the
  // local flag, persists via Star toggle (re-save with starred=!cur),
  // refreshes on success. Star is currently expressed by re-creating
  // the saved row with starred=true; the server's Update path lives
  // behind the saved.Store interface but a dedicated star endpoint
  // isn't wired in the HTTP API yet. The optimistic flip keeps the
  // sidebar feeling responsive even when the network round-trip is
  // slow; on failure we refresh from the server to drop the stale flag.
  async function toggleStar(s) {
    const wantStarred = !s.starred
    // Optimistic UI flip so the icon updates immediately.
    saved = saved.map((r) => (r.id === s.id ? { ...r, starred: wantStarred } : r))
    try {
      // Round-trip via delete + re-create so the durable store flips
      // the persisted starred flag. This relies on the new server
      // accepting `starred` on the create body (v0.3.2-A).
      await api.deleteSaved(id, s.id)
      await api.saveQuery(id, {
        name: s.name,
        statement: s.statement,
        description: s.description || '',
        tags: s.tags || [],
        starred: wantStarred,
      })
      refreshSaved()
    } catch {
      // Roll back the optimistic flip on failure.
      refreshSaved()
      try { pushToast({ message: `Could not ${wantStarred ? 'star' : 'unstar'}: ${s.name}`, tone: 'danger' }) } catch { /* ignore */ }
    }
  }

  // a11y-08: keyboard-delete a saved query. ConfirmDialog gates the
  // destructive action and the Delete key on the saved <li> opens it.
  async function commitDeleteSaved() {
    if (!pendingDelete) return
    const target = pendingDelete
    pendingDelete = null
    try {
      await api.deleteSaved(id, target.id)
      refreshSaved()
      try { pushToast({ message: `Deleted saved query: ${target.name}`, tone: 'info' }) } catch { /* ignore */ }
    } catch {
      try { pushToast({ message: `Could not delete: ${target.name}`, tone: 'danger' }) } catch { /* ignore */ }
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

  // EXEC-10: clicking a history / saved entry REPLACES the editor
  // buffer. CM6 keeps undo so recovery is one Ctrl+Z away, but losing
  // half a page of unsaved work to a stray click is a real papercut.
  // When the current buffer is non-empty AND differs from the incoming
  // payload, gate the load behind a confirm dialog. The internal
  // bypass path (`replace handoff from palette / consumePending`) calls
  // _loadIntoEditorRaw directly so the existing replay flow is
  // untouched.
  function _loadIntoEditorRaw(sqlText) {
    editorRef?.setDoc(sqlText)
    docText = sqlText
    editorRef?.focus()
  }

  function loadIntoEditor(sqlText) {
    const current = (docText || '').trim()
    const incoming = (sqlText || '').trim()
    if (current.length > 0 && current !== incoming) {
      pendingLoadSql = sqlText
      confirmLoadOpen = true
      return
    }
    _loadIntoEditorRaw(sqlText)
  }

  function confirmLoad() {
    confirmLoadOpen = false
    const next = pendingLoadSql
    pendingLoadSql = ''
    if (next) _loadIntoEditorRaw(next)
  }

  function cancelLoad() {
    confirmLoadOpen = false
    pendingLoadSql = ''
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
        Execute <span class="sql-editor__kbd" aria-hidden="true">⌘↵</span>
      </Btn>
      <Btn variant="ghost" onclick={openExplain} disabled={connLoading || isForbidden || !currentStatement}>
        Explain <span class="sql-editor__kbd" aria-hidden="true">⌘E</span>
      </Btn>
      {#if anyExecuting}
        <!-- a11y-03: surface the Cmd+. keybinding so it's discoverable
             both for sighted users (kbd hint) and AT users
             (aria-keyshortcuts). The native attribute is ignored by
             older browsers but does no harm. -->
        <button
          type="button"
          class="btn btn--danger"
          onclick={cancelCurrent}
          aria-keyshortcuts="Meta+Period"
          title="Cancel running query (⌘.)"
        >
          Cancel <span class="sql-editor__kbd" aria-hidden="true">⌘.</span>
        </button>
      {/if}
      <!-- INT-6: preload the sql-formatter chunk on hover/focus so the
           first click doesn't wait on the ~76 KB gz fetch. -->
      <span onmouseenter={preloadFormatter} onfocusin={preloadFormatter}>
        <Btn variant="ghost" onclick={formatDoc} loading={formatting} ariaBusy={formatting}>Format</Btn>
      </span>
      <Btn variant="ghost" onclick={saveQuery}>Save</Btn>
      <Btn variant="ghost" onclick={toggleSidebar} ariaLabel="toggle sidebar">{sidebarOpen ? '▶' : '◀'}</Btn>
    </div>
  </header>

  <div class="sql-editor__body" class:sql-editor__body--with-sidebar={sidebarOpen}>
    <div class="sql-editor__center">
      <div class="sql-editor__editor" data-testid="editor-shell">
        {#if connLoading}
          <div class="u-loading" role="status">Loading connection…</div>
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

      <!-- a11y-14 (partial): split status + error live regions. The
           polite region carries progress / completion ("executing…",
           "done in 12ms"); the assertive region carries errors so AT
           interrupts the user with the failure. Keeping them in
           separate DOM nodes prevents a transient status update from
           steamrolling an unread error announcement. -->
      <div class="sql-editor__status" role="status" aria-live="polite">
        {statusMsg}
      </div>
      {#if errorMsg}
        <div class="sql-editor__status sql-editor__status--err" role="alert" aria-live="assertive">
          {errorMsg}
        </div>
      {/if}

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
        <!-- a11y-07: accordions now use a proper button + aria-expanded
             so AT users can collapse them and the visual state matches
             the semantic state. Click the header to toggle. -->
        <section class="sql-editor__accordion">
          <h3>
            <button
              type="button"
              class="sql-editor__accordionHead"
              aria-expanded={historyOpen}
              aria-controls="sql-editor__history-panel"
              onclick={() => { historyOpen = !historyOpen }}
            >
              <span class="sql-editor__caret" aria-hidden="true">{historyOpen ? '▾' : '▸'}</span>
              History
            </button>
          </h3>
          {#if historyOpen}
            <div id="sql-editor__history-panel">
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
            </div>
          {/if}
        </section>
        <section class="sql-editor__accordion">
          <h3>
            <button
              type="button"
              class="sql-editor__accordionHead"
              aria-expanded={savedOpen}
              aria-controls="sql-editor__saved-panel"
              onclick={() => { savedOpen = !savedOpen }}
            >
              <span class="sql-editor__caret" aria-hidden="true">{savedOpen ? '▾' : '▸'}</span>
              Saved
            </button>
          </h3>
          {#if savedOpen}
            <div id="sql-editor__saved-panel">
              <!-- v0.3.2-A: durable saved-queries store landed. The
                   session-only caveat (INT-5) was removed because the
                   server now persists across daemon restarts. -->
              {#if saved.length === 0}
                <p class="sql-editor__empty">No saved queries</p>
              {:else}
                <ul class="sql-editor__list">
                  {#each saved as s (s.id)}
                    <li class="sql-editor__savedRow" class:sql-editor__savedRow--starred={s.starred}>
                      <!-- a11y-08: keyboard-delete affordance — Delete /
                           Backspace on the row opens a confirm dialog
                           and routes through api.deleteSaved. The
                           explicit "Remove" button is visible too so
                           mouse users have parity. -->
                      <button
                        class="sql-editor__listBtn"
                        onclick={() => loadIntoEditor(s.statement)}
                        title={s.description || s.statement}
                        onkeydown={(e) => {
                          if (e.key === 'Delete' || e.key === 'Backspace') {
                            e.preventDefault()
                            pendingDelete = { id: s.id, name: s.name }
                          }
                        }}
                      >
                        <span class="sql-editor__listName">{s.name}</span>
                        {#if s.description}
                          <span class="sql-editor__listDesc">{s.description}</span>
                        {/if}
                      </button>
                      <!-- v0.3.2-A: star toggle. The icon reflects
                           current state; click flips it. The server's
                           List endpoint supports star_only=1; future
                           UI may surface a "Starred only" filter. -->
                      <button
                        type="button"
                        class="sql-editor__rowStar"
                        aria-pressed={!!s.starred}
                        aria-label={s.starred ? `Unstar saved query ${s.name}` : `Star saved query ${s.name}`}
                        title={s.starred ? 'Unstar' : 'Star'}
                        onclick={() => toggleStar(s)}
                      >{s.starred ? '★' : '☆'}</button>
                      <button
                        type="button"
                        class="sql-editor__rowDel"
                        aria-label={`Delete saved query ${s.name}`}
                        title="Delete (Del / Backspace)"
                        onclick={() => { pendingDelete = { id: s.id, name: s.name } }}
                      >×</button>
                    </li>
                  {/each}
                </ul>
              {/if}
            </div>
          {/if}
        </section>
      </aside>
    {/if}
  </div>

  <!-- EXEC-9 / EXEC-13 / INT-5 / a11y-10: replace window.prompt with a
       focus-trapped modal that captures name + description + tags and
       surfaces the session-only caveat next to the action. -->
  <SaveQueryModal
    bind:open={saveModalOpen}
    statement={docText}
    existingNames={saved.map((s) => s.name)}
    sessionOnly={false}
    onClose={() => { saveModalOpen = false }}
    onSave={commitSave}
  />

  <!-- EXEC-10: dirty-check confirmation when loading a saved/history
       entry would clobber the current buffer. -->
  <ConfirmDialog
    bind:open={confirmLoadOpen}
    title="Replace editor buffer?"
    message="The editor has unsaved changes. Loading this query will replace them. Continue?"
    confirmLabel="Replace"
    cancelLabel="Keep current"
    tone="danger"
    onConfirm={confirmLoad}
    onCancel={cancelLoad}
  />

  <!-- a11y-08: keyboard-delete confirm. -->
  <ConfirmDialog
    open={pendingDelete !== null}
    title="Delete saved query"
    message={pendingDelete ? `Delete saved query "${pendingDelete.name}"? This cannot be undone.` : ''}
    confirmLabel="Delete"
    cancelLabel="Keep"
    tone="danger"
    onConfirm={commitDeleteSaved}
    onCancel={() => { pendingDelete = null }}
  />
</div>

<style>
  /* PR #13.5 additive styles. Main editor styles live in the screen's parent stylesheet. */
  .sql-editor__status--err { color: var(--danger, #dc2626); }
  .sql-editor__accordionHead { display: inline-flex; align-items: center; gap: 6px; background: transparent; border: 0; padding: 4px 2px; color: inherit; font: inherit; font-weight: 600; cursor: pointer; width: 100%; text-align: left; }
  .sql-editor__accordionHead:focus-visible { outline: 2px solid var(--accent, #2563eb); outline-offset: 2px; border-radius: 4px; }
  .sql-editor__caret { display: inline-block; width: 1em; color: var(--text-dim, #888); }
  .sql-editor__sessionNote { margin: 4px 0 6px; padding: 4px 6px; font-size: 0.78em; color: var(--text-dim, #888); border-left: 2px solid var(--info, #2563eb); background: rgba(37, 99, 235, 0.06); }
  .sql-editor__savedRow { display: flex; align-items: center; gap: 4px; }
  .sql-editor__savedRow .sql-editor__listBtn { flex: 1; min-width: 0; display: flex; flex-direction: column; align-items: flex-start; }
  .sql-editor__savedRow--starred .sql-editor__listName { font-weight: 600; }
  .sql-editor__listDesc { display: block; font-size: 0.78em; color: var(--text-dim, #888); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 100%; margin-top: 2px; }
  .sql-editor__rowStar { background: transparent; border: 0; color: var(--text-dim, #888); cursor: pointer; padding: 2px 6px; border-radius: 3px; font-size: 1em; line-height: 1; }
  .sql-editor__rowStar[aria-pressed="true"] { color: var(--warning, #d97706); }
  .sql-editor__rowStar:hover { background: rgba(217, 119, 6, 0.1); color: var(--warning, #d97706); }
  .sql-editor__rowStar:focus-visible { outline: 2px solid var(--warning, #d97706); outline-offset: 1px; }
  .sql-editor__rowDel { background: transparent; border: 0; color: var(--text-dim, #888); cursor: pointer; padding: 2px 6px; border-radius: 3px; font-size: 1.1em; line-height: 1; }
  .sql-editor__rowDel:hover { color: var(--danger, #dc2626); background: rgba(220, 38, 38, 0.1); }
  .sql-editor__rowDel:focus-visible { outline: 2px solid var(--danger, #dc2626); outline-offset: 1px; }
</style>


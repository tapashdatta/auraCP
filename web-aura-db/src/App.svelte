<script>
  import { onMount } from 'svelte'
  import { routeState, navigate } from './lib/router.svelte.js'
  import { session } from './lib/session.svelte.js'
  import { loadConnections } from './lib/connections.svelte.js'
  // Side-effect imports register keyboard shortcuts on the window.
  import './lib/shortcuts.svelte.js'

  import AppShell from './lib/components/AppShell.svelte'
  import AuthGate from './screens/AuthGate.svelte'
  import { t } from './lib/strings.js'
  import WelcomeScreen from './screens/WelcomeScreen.svelte'
  import ConnectionList from './screens/ConnectionList.svelte'
  import ConnectionForm from './screens/ConnectionForm.svelte'
  import ConnectionDetail from './screens/ConnectionDetail.svelte'
  import SchemaBrowser from './screens/SchemaBrowser.svelte'
  import TableDetail from './screens/TableDetail.svelte'
  // WIRE-16 (PR #12.5): the old RowGrid.svelte was a one-line re-export
  // wrapper around TableScreen.svelte (a leftover from when the file was
  // renamed). Importing TableScreen directly removes a dead indirection
  // and a useless component instance from the runtime tree.
  import TableScreen from './screens/TableScreen.svelte'
  // a11y-12 / bundle: SqlEditor pulls CodeMirror (~140 KB gz). Lazy-
  // load it so non-/query routes never pay that tax. Vite splits the
  // import into its own chunk; loaded on demand when the user navigates
  // to /connections/:id/query.
  import AuditView from './screens/AuditView.svelte'
  import AccountScreen from './screens/AccountScreen.svelte'
  // PR #15: command palette + lazy history screen.
  import CommandPalette from './lib/components/CommandPalette.svelte'

  /** @type {any} */
  let SqlEditorComp = $state(null)
  let sqlEditorLoading = $state(false)
  async function ensureSqlEditor() {
    if (SqlEditorComp || sqlEditorLoading) return
    sqlEditorLoading = true
    try {
      const mod = await import('./screens/SqlEditor.svelte')
      SqlEditorComp = mod.default
    } finally {
      sqlEditorLoading = false
    }
  }
  $effect(() => {
    if (routeState.name === 'query') ensureSqlEditor()
  })

  // PR #14: ExplainInspectorScreen is lazy-loaded into its own chunk so
  // the SVG flame-tree + helpers don't bloat the main bundle.
  /** @type {any} */
  let ExplainInspectorComp = $state(null)
  let explainLoading = $state(false)
  async function ensureExplainInspector() {
    if (ExplainInspectorComp || explainLoading) return
    explainLoading = true
    try {
      const mod = await import('./screens/ExplainInspectorScreen.svelte')
      ExplainInspectorComp = mod.default
    } finally {
      explainLoading = false
    }
  }
  $effect(() => {
    if (routeState.name === 'explain') ensureExplainInspector()
  })

  // PR #15: HistoryScreen is lazy-loaded so the filter + table grid don't
  // ship in the initial bundle. Loaded on first /history navigation.
  /** @type {any} */
  let HistoryScreenComp = $state(null)
  let historyLoading = $state(false)
  async function ensureHistoryScreen() {
    if (HistoryScreenComp || historyLoading) return
    historyLoading = true
    try {
      const mod = await import('./screens/HistoryScreen.svelte')
      HistoryScreenComp = mod.default
    } finally {
      historyLoading = false
    }
  }
  $effect(() => {
    if (routeState.name === 'history') ensureHistoryScreen()
  })

  onMount(() => {
    if (!session.hasCookie) {
      navigate('/401')
      return
    }
    // No connections yet → kick off a fetch. Errors surface in the status bar.
    loadConnections()
  })

  // FIX (PR #11 a11y-07): per-route document title. The static "Aura DB"
  // title gave no orientation when multiple SPA tabs were open. We update
  // document.title from a $derived so a route change syncs the tab label.
  const docTitle = $derived.by(() => {
    const key = 'doc.title.' + (routeState.name || 'welcome')
    const candidate = t(key)
    // If the key wasn't in STRINGS, t() returns the key verbatim. Fall
    // back to the base brand string in that case.
    return candidate === key ? t('doc.title.base') : candidate
  })
  $effect(() => {
    if (typeof document !== 'undefined') document.title = docTitle
  })
</script>

{#if routeState.name === 'auth.gate' || !session.hasCookie}
  <AuthGate />
{:else}
  <AppShell>
    {#if routeState.name === 'welcome'}
      <WelcomeScreen />
    {:else if routeState.name === 'conn.list'}
      <ConnectionList />
    {:else if routeState.name === 'conn.new'}
      <ConnectionForm />
    {:else if routeState.name === 'conn.detail'}
      <ConnectionDetail />
    {:else if routeState.name === 'schema'}
      <SchemaBrowser />
    {:else if routeState.name === 'table'}
      <TableDetail />
    {:else if routeState.name === 'rows'}
      <TableScreen />
    {:else if routeState.name === 'query'}
      {#if SqlEditorComp}
        <SqlEditorComp />
      {:else}
        <div class="u-loading" role="status">Loading SQL editor…</div>
      {/if}
    {:else if routeState.name === 'explain'}
      {#if ExplainInspectorComp}
        <ExplainInspectorComp />
      {:else}
        <div class="u-loading" role="status">Loading EXPLAIN inspector…</div>
      {/if}
    {:else if routeState.name === 'history'}
      {#if HistoryScreenComp}
        <HistoryScreenComp />
      {:else}
        <div class="u-loading" role="status">Loading history…</div>
      {/if}
    {:else if routeState.name === 'audit'}
      <AuditView />
    {:else if routeState.name === 'account'}
      <AccountScreen />
    {:else}
      <WelcomeScreen />
    {/if}
  </AppShell>
  <CommandPalette />
{/if}

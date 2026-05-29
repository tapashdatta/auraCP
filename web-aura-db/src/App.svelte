<script>
  import { onMount } from 'svelte'
  import { routeState, navigate } from './lib/router.svelte.js'
  import { session } from './lib/session.svelte.js'
  import { loadConnections } from './lib/connections.svelte.js'
  // Side-effect imports register keyboard shortcuts on the window.
  import './lib/shortcuts.svelte.js'

  import AppShell from './lib/components/AppShell.svelte'
  import AuthGate from './screens/AuthGate.svelte'
  import WelcomeScreen from './screens/WelcomeScreen.svelte'
  import ConnectionList from './screens/ConnectionList.svelte'
  import ConnectionForm from './screens/ConnectionForm.svelte'
  import ConnectionDetail from './screens/ConnectionDetail.svelte'
  import SchemaBrowser from './screens/SchemaBrowser.svelte'
  import TableDetail from './screens/TableDetail.svelte'
  import RowGrid from './screens/RowGrid.svelte'
  // a11y-12 / bundle: SqlEditor pulls CodeMirror (~140 KB gz). Lazy-
  // load it so non-/query routes never pay that tax. Vite splits the
  // import into its own chunk; loaded on demand when the user navigates
  // to /connections/:id/query.
  import HistoryView from './screens/HistoryView.svelte'
  import AuditView from './screens/AuditView.svelte'
  import AccountScreen from './screens/AccountScreen.svelte'

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

  onMount(() => {
    if (!session.hasCookie) {
      navigate('/401')
      return
    }
    // No connections yet → kick off a fetch. Errors surface in the status bar.
    loadConnections()
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
      <RowGrid />
    {:else if routeState.name === 'query'}
      {#if SqlEditorComp}
        <SqlEditorComp />
      {:else}
        <div style="padding:24px;color:var(--text-mute,#888)">Loading SQL editor…</div>
      {/if}
    {:else if routeState.name === 'explain'}
      {#if ExplainInspectorComp}
        <ExplainInspectorComp />
      {:else}
        <div style="padding:24px;color:var(--text-mute,#888)">Loading EXPLAIN inspector…</div>
      {/if}
    {:else if routeState.name === 'history'}
      <HistoryView />
    {:else if routeState.name === 'audit'}
      <AuditView />
    {:else if routeState.name === 'account'}
      <AccountScreen />
    {:else}
      <WelcomeScreen />
    {/if}
  </AppShell>
{/if}

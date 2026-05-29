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
  import SqlEditor from './screens/SqlEditor.svelte'
  import HistoryView from './screens/HistoryView.svelte'
  import AuditView from './screens/AuditView.svelte'
  import AccountScreen from './screens/AccountScreen.svelte'

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
      <SqlEditor />
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

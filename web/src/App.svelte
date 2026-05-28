<script>
  import { onMount } from 'svelte'
  import { ui } from './lib/store.svelte.js'
  import { session, checkAuth } from './lib/auth.svelte.js'
  import TopBar from './lib/components/TopBar.svelte'
  import DialogHost from './lib/components/DialogHost.svelte'
  import Login from './screens/Login.svelte'
  import Sites from './screens/Sites.svelte'
  import AddSite from './screens/AddSite.svelte'
  import CreateForm from './screens/CreateForm.svelte'
  import SiteDetail from './screens/SiteDetail.svelte'
  import Account from './screens/Account.svelte'
  import AdminUsers from './screens/AdminUsers.svelte'
  import Instance from './screens/Instance.svelte'

  onMount(checkAuth)
</script>

{#if session.loading}
  <div class="boot"><span class="gem"></span></div>
{:else if !session.user}
  <Login />
{:else}
  <TopBar />
  {#if ui.view === 'sites'}
    <Sites />
  {:else if ui.view === 'add'}
    <AddSite />
  {:else if ui.view === 'create'}
    <CreateForm />
  {:else if ui.view === 'detail'}
    <SiteDetail />
  {:else if ui.view === 'account'}
    <Account />
  {:else if ui.view === 'users'}
    <AdminUsers />
  {:else if ui.view === 'instance'}
    <Instance />
  {/if}
{/if}

<!-- Modal dialog host — rendered above all screens, listens for any call
     to confirmDialog / promptDialog / alertDialog. -->
<DialogHost />

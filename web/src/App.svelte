<script>
  import { onMount } from 'svelte'
  import { ui, parseHash } from './lib/store.svelte.js'
  import { session, checkAuth } from './lib/auth.svelte.js'
  import { fetchSites } from './lib/data.js'
  import TopBar from './lib/components/TopBar.svelte'
  import DialogHost from './lib/components/DialogHost.svelte'
  import ToastHost from './lib/components/ToastHost.svelte'
  import Login from './screens/Login.svelte'
  import Sites from './screens/Sites.svelte'
  import AddSite from './screens/AddSite.svelte'
  import CreateForm from './screens/CreateForm.svelte'
  import SiteDetail from './screens/SiteDetail.svelte'
  import Account from './screens/Account.svelte'
  import AdminUsers from './screens/AdminUsers.svelte'
  import Instance from './screens/Instance.svelte'

  // Restore the UI state from the current URL hash. Called on initial mount
  // (page refresh / direct link) and when the browser fires popstate
  // (back / forward buttons). Never called for our own pushState/replaceState
  // writes — those update ui directly before writing the hash.
  async function restoreFromHash() {
    const route = parseHash()
    if (route.view === 'detail') {
      // Site detail — we need the full site object to populate ui.site.
      // Fetch the list (cheap, cached at network level) and find by domain.
      const sites = await fetchSites()
      const found = sites.find(s => s.domain === route.domain)
      if (found) {
        ui.site = found
        ui._tab = route.tab  // passed to SiteDetail via ui._tab
        ui.view = 'detail'
        return
      }
      // Domain no longer exists — fall back to sites list.
      history.replaceState(null, '', '#/')
      ui.view = 'sites'
      return
    }
    if (route.view === 'create') ui.createType = route.createType
    ui.view = route.view
  }

  onMount(async () => {
    await checkAuth()
    // Only restore the hash once we know the user is authenticated.
    // If not authenticated, session.user stays null and Login is shown
    // regardless of ui.view, so we can safely run restoreFromHash here.
    await restoreFromHash()
    window.addEventListener('popstate', restoreFromHash)
  })
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

<!-- Toast host — transient feedback that auto-dismisses after 4 s (6 s for
     errors). Listens to lib/toast.svelte.js. -->
<ToastHost />

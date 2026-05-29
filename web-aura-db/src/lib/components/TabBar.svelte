<script>
  import Tab from './Tab.svelte'
  import { workspaces, activateTab, closeTab, openTab } from '../workspaces.svelte.js'
  import { navigate, routeState } from '../router.svelte.js'
  import { icons } from '../icons.js'

  function onActivate(tab) {
    activateTab(tab.id)
    navigate(tab.path)
  }

  function newTab() {
    openTab({ title: 'Connections', path: '/connections' })
    navigate('/connections')
  }

  // Auto-sync: if the user navigates to a route that's not represented by a
  // tab, just leave activeId alone — the route content still renders. The
  // tab strip is a convenience, not the source of truth for the route.
  void routeState
</script>

<div class="tabbar" role="tablist">
  {#each workspaces.tabs as t (t.id)}
    <Tab
      id={t.id}
      title={t.title}
      active={t.id === workspaces.activeId}
      onActivate={() => onActivate(t)}
      onClose={() => closeTab(t.id)}
    />
  {/each}
  <button class="tabbar__new" onclick={newTab} aria-label="new tab">
    <svg width="12" height="12" viewBox="0 0 12 12" aria-hidden="true">
      <path d={icons.plus} stroke="currentColor" stroke-width="1.5" stroke-linecap="round" />
    </svg>
  </button>
</div>

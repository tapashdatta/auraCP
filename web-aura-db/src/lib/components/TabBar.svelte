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

  void routeState
</script>

<!-- FIX (PR #11 a11y-05): the tab strip looked like role=tablist with
     role=tab children but actually each "tab" is a route navigator,
     not a panel switcher. Per WAI-ARIA spec a tablist must own a
     tabpanel that toggles via aria-controls — we don't. Drop the
     role and expose it as a navigation region instead. The Tab
     component still carries role=tab visually, but is now treated as
     a navigation item. -->
<div class="tabbar" role="toolbar" aria-label="Open workspaces">
  {#each workspaces.tabs as t (t.id)}
    <Tab
      id={t.id}
      title={t.title}
      active={t.id === workspaces.activeId}
      onActivate={() => onActivate(t)}
      onClose={() => closeTab(t.id)}
    />
  {/each}
  <button type="button" class="tabbar__new" onclick={newTab} aria-label="Open new workspace">
    <svg width="12" height="12" viewBox="0 0 12 12" aria-hidden="true">
      <path d={icons.plus} stroke="currentColor" stroke-width="1.5" stroke-linecap="round" />
    </svg>
  </button>
</div>

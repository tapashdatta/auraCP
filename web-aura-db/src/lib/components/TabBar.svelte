<script>
  import Tab from './Tab.svelte'
  import { workspaces, activateTab, closeTab, openTab } from '../workspaces.svelte.js'
  import { navigate, routeState } from '../router.svelte.js'
  import Icon from './Icon.svelte'

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
    <Icon name="plus" size={15} />
  </button>
</div>

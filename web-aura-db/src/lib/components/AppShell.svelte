<script>
  import TopNav from './TopNav.svelte'
  import LeftTree from './LeftTree.svelte'
  import TabBar from './TabBar.svelte'
  import StatusBar from './StatusBar.svelte'

  /** @type {{ children?: any }} */
  let { children } = $props()
</script>

<div class="shell">
  <TopNav />
  <div class="shell__body">
    <LeftTree />
    <section class="center-pane">
      <TabBar />
      <div class="route-outlet">
        {@render children?.()}
      </div>
    </section>
  </div>
  <StatusBar />
</div>

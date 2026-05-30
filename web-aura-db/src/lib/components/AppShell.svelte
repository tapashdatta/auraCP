<script>
  import IconRail from './IconRail.svelte'
  import LeftTree from './LeftTree.svelte'
  import TopBar from './TopBar.svelte'
  import TabBar from './TabBar.svelte'
  import StatusBar from './StatusBar.svelte'
  import ToastRegion from './ToastRegion.svelte'
  import ErrorBoundary from './ErrorBoundary.svelte'
  import { routeState } from '../router.svelte.js'
  import { ui, closeTree } from '../ui.svelte.js'
  import { t } from '../strings.js'

  /** @type {{ children?: any }} */
  let { children } = $props()

  // Navigating closes the mobile drawer (no-op on desktop where the tree
  // is always pinned). Tracks the route path so any navigation collapses it.
  $effect(() => {
    void routeState.path
    closeTree()
  })
</script>

<!--
  FIX (PR #11 a11y-06, PR #14.5 A11Y-11): expose proper landmarks and a
  skip-link so keyboard / SR users can bypass the persistent chrome
  (IconRail + LeftTree + TopBar + TabBar) and land in the route content.

  Studio shell (prototype): vertical icon rail + context sidebar +
  breadcrumb topbar + main. The status bar spans the full width at the
  bottom. Below 720px the sidebar collapses into a drawer toggled by the
  topbar burger (.shell--tree-open).
-->
<a class="skip-link" href="#main-content">{t('a11y.skip.main')}</a>

<div class="shell" class:shell--tree-open={ui.treeOpen}>
  <div class="shell__body">
    <IconRail />
    <LeftTree />
    <section class="center-pane" aria-label="Workspace">
      <TopBar />
      <TabBar />
      <!-- FIX (PR #11 a11y-11): wrap the per-route render in an
           ErrorBoundary so a thrown render error becomes a graceful
           pane instead of a blank screen. The boundary is intentionally
           per-render-pass, not module-level, so a successful re-route
           clears the error. -->
      <main
        id="main-content"
        class="route-outlet"
        tabindex="-1"
        aria-label={t('a11y.landmark.main')}
      >
        <ErrorBoundary>
          {@render children?.()}
        </ErrorBoundary>
      </main>
    </section>
  </div>
  <StatusBar />
  {#if ui.treeOpen}
    <!-- Tap-away scrim for the mobile sidebar drawer. -->
    <button class="shell__scrim" type="button" aria-label="Close connections" onclick={closeTree}></button>
  {/if}
</div>
<ToastRegion />

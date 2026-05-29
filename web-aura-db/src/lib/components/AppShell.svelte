<script>
  import TopNav from './TopNav.svelte'
  import LeftTree from './LeftTree.svelte'
  import TabBar from './TabBar.svelte'
  import StatusBar from './StatusBar.svelte'
  import ToastRegion from './ToastRegion.svelte'
  import ErrorBoundary from './ErrorBoundary.svelte'
  import { t } from '../strings.js'

  /** @type {{ children?: any }} */
  let { children } = $props()
</script>

<!--
  FIX (PR #11 a11y-06, PR #14.5 A11Y-11): expose proper landmarks and a
  skip-link so keyboard / SR users can bypass the persistent chrome
  (TopNav + LeftTree + TabBar) and land in the route content directly.

  The skip link is the very first focusable element in the DOM. The
  route outlet is now a proper <main id="main-content"> landmark with
  tabindex=-1 so the skip target gets focus when activated.
-->
<a class="skip-link" href="#main-content">{t('a11y.skip.main')}</a>

<div class="shell">
  <TopNav />
  <div class="shell__body">
    <LeftTree />
    <section class="center-pane" aria-label="Workspace">
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
</div>
<ToastRegion />

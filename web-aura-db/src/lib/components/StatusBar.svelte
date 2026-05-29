<script>
  import { onMount } from 'svelte'
  import { sqlStream } from '../sqlStream.js'
  import { connections } from '../connections.svelte.js'
  import { t } from '../strings.js'

  let wsState = $state('idle')

  onMount(() => sqlStream.subscribe((s) => { wsState = s }))

  const wsLabel = $derived(t(`status.ws.${wsState}`))
  const connCount = $derived(connections.list.length)
</script>

<!--
  FIX-12 (PR #11 a11y-04): wrap the live regions in aria-live containers so
  screen readers announce connection-status changes, WS reconnects, and
  error messages without forcing the user to navigate to the status bar.

  The center region (connection state + error) uses aria-live="polite"
  when reporting a normal state and aria-live="assertive" when surfacing
  an error — assertive interrupts the AT immediately, which is the right
  tradeoff for a failure the user needs to know about now.
-->
<footer class="statusbar">
  <span class="statusbar__left" aria-live="polite" aria-atomic="true">
    <span>{t('status.connections.count', { n: connCount })}</span>
  </span>
  <span
    class="statusbar__center"
    aria-live={connections.error ? 'assertive' : 'polite'}
    aria-atomic="true"
    role={connections.error ? 'alert' : 'status'}
  >
    {#if connections.loading}
      <span>{t('status.loading')}</span>
    {:else if connections.error}
      <span class="u-color-danger">{connections.error}</span>
    {:else}
      <span>{t('status.ready')}</span>
    {/if}
  </span>
  <span class="statusbar__right" aria-live="polite" aria-atomic="true">
    <span>{wsLabel}</span>
  </span>
</footer>

<script>
  import { pushToast } from '../../toastBus.svelte.js'

  /** @type {{ code?: string, message?: string, onRetry?: ()=>void }} */
  let { code = '', message = '', onRetry = null } = $props()

  // a11y-06 / a11y-14 partial: route the error through the toast bus
  // on each new error (code+message tuple) so AT users get the
  // assertive announcement they would on any other panel error. The
  // visible panel still renders for sighted users with role="alert"
  // and aria-live="assertive" so a focused screen reader picks up
  // the change-in-place when the same tab transitions to error.
  let _last = $state(/** @type {string|null} */ (null))
  $effect(() => {
    const key = `${code || ''}::${message || ''}`
    if (!code && !message) { _last = key; return }
    if (_last === key) return
    _last = key
    try {
      pushToast({ message: `${code || 'error'}: ${message || 'query failed'}`, tone: 'danger' })
    } catch { /* ignore — toast bus optional in tests */ }
  })
</script>

<!-- a11y-06: role=alert + assertive live region so a screen reader
     interrupts to read the error. Pairs with the toast for users
     whose focus has moved away from the result panel. -->
<div class="err" role="alert" aria-live="assertive">
  <div class="err__head">
    <span class="err__code">{code || 'error'}</span>
    {#if onRetry}<button class="err__retry" onclick={onRetry}>Retry</button>{/if}
  </div>
  <pre class="err__body">{message}</pre>
</div>

<script>
  /** @type {{ code?: string, message?: string, onRetry?: ()=>void }} */
  let { code = '', message = '', onRetry = null } = $props()
</script>

<div class="err">
  <div class="err__head">
    <span class="err__code">{code || 'error'}</span>
    {#if onRetry}<button class="err__retry" onclick={onRetry}>Retry</button>{/if}
  </div>
  <pre class="err__body">{message}</pre>
</div>

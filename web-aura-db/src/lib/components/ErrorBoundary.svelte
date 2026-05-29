<script>
  import Btn from './Btn.svelte'
  import { t } from '../strings.js'

  /** @type {{
   *   error?: { code?: string, message?: string, requestId?: string } | null,
   *   onRetry?: ()=>void,
   *   children?: any,
   * }} */
  let { error = null, onRetry, children } = $props()
</script>

{#if error}
  <div class="empty">
    <h3 class="empty__title">{error.code || 'error'}</h3>
    <p class="empty__body">{error.message}</p>
    {#if error.requestId}
      <p class="empty__body" style="font-family:var(--font-mono); font-size:var(--fs-meta)">req {error.requestId}</p>
    {/if}
    {#if onRetry}
      <Btn variant="primary" onclick={onRetry}>{t('action.retry')}</Btn>
    {/if}
  </div>
{:else}
  {@render children?.()}
{/if}

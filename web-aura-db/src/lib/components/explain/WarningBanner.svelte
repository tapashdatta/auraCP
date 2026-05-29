<script>
  /**
   * @type {{
   *   warnings?: string[],
   *   onDismiss?: () => void,
   * }}
   */
  let { warnings = [], onDismiss } = $props()
  let dismissed = $state(false)
</script>

{#if warnings.length > 0 && !dismissed}
  <div class="warning-banner" role="status">
    <ul class="warning-banner__list">
      {#each warnings as w (w)}
        <li class="warning-banner__item">{w}</li>
      {/each}
    </ul>
    <button class="warning-banner__close" type="button" onclick={() => { dismissed = true; onDismiss?.() }} aria-label="Dismiss warnings">×</button>
  </div>
{/if}

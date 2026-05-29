<script>
  /** @type {{ keys?: string[] }} */
  let { keys = [] } = $props()
</script>

<span class="kbd">
  {#each keys as k, i (i)}
    {#if i > 0}<span>+</span>{/if}
    <span class="kbd__key">{k}</span>
  {/each}
</span>

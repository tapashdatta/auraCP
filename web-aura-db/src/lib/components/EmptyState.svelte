<script>
  import Btn from './Btn.svelte'

  /** @type {{
   *   title?: string,
   *   body?: string,
   *   action?: { label: string, onClick: ()=>void } | null,
   * }} */
  let { title = '', body = '', action = null } = $props()
</script>

<div class="empty">
  <h3 class="empty__title">{title}</h3>
  {#if body}<p class="empty__body">{body}</p>{/if}
  {#if action}
    <Btn variant="primary" onclick={action.onClick}>{action.label}</Btn>
  {/if}
</div>

<script>
  import Btn from './Btn.svelte'

  /** @type {{
   *   title?: string,
   *   body?: string,
   *   action?: { label: string, onClick: ()=>void } | null,
   *   level?: 1|2|3|4|5|6,
   * }} */
  // FIX (PR #11 a11y-23): heading level is now configurable so an empty
  // state nested inside a section with its own h2 doesn't skip from h2
  // to h3 indiscriminately. Defaults to h3 to keep existing callers
  // visually identical.
  let { title = '', body = '', action = null, level = 3 } = $props()
</script>

<div class="empty">
  {#if level === 1}
    <h1 class="empty__title">{title}</h1>
  {:else if level === 2}
    <h2 class="empty__title">{title}</h2>
  {:else if level === 4}
    <h4 class="empty__title">{title}</h4>
  {:else if level === 5}
    <h5 class="empty__title">{title}</h5>
  {:else if level === 6}
    <h6 class="empty__title">{title}</h6>
  {:else}
    <h3 class="empty__title">{title}</h3>
  {/if}
  {#if body}<p class="empty__body">{body}</p>{/if}
  {#if action}
    <Btn variant="primary" onclick={action.onClick}>{action.label}</Btn>
  {/if}
</div>

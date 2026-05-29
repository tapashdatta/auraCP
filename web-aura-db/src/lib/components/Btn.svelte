<script>
  import Spinner from './Spinner.svelte'
  /** @type {{
   *   variant?: 'primary'|'ghost'|'danger'|'default',
   *   size?: 'sm'|'md',
   *   disabled?: boolean,
   *   loading?: boolean,
   *   type?: 'button'|'submit',
   *   onclick?: (e: MouseEvent)=>void,
   *   children?: any
   * }} */
  let { variant = 'default', size = 'md', disabled = false, loading = false, type = 'button', onclick, children } = $props()
</script>

<button
  {type}
  class="btn {variant === 'primary' ? 'btn--primary' : variant === 'ghost' ? 'btn--ghost' : variant === 'danger' ? 'btn--danger' : ''} {size === 'sm' ? 'btn--sm' : ''}"
  disabled={disabled || loading}
  onclick={onclick}
>
  {#if loading}<Spinner size={10} />{/if}
  {@render children?.()}
</button>

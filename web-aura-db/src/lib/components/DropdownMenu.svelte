<script>
  import { onMount } from 'svelte'
  /** @type {{
   *   open?: boolean,
   *   items?: Array<{label:string, onSelect:()=>void, tone?:'danger'|'normal'}>,
   *   anchor?: HTMLElement,
   * }} */
  let { open = $bindable(false), items = [], anchor } = $props()

  let style = $state('')

  $effect(() => {
    if (open && anchor) {
      const r = anchor.getBoundingClientRect()
      style = `top:${Math.round(r.bottom + 4)}px;right:${Math.round(window.innerWidth - r.right)}px;`
    }
  })

  onMount(() => {
    const closeOnOutside = (e) => {
      if (!open) return
      if (anchor && (anchor === e.target || anchor.contains(e.target))) return
      open = false
    }
    document.addEventListener('mousedown', closeOnOutside)
    return () => document.removeEventListener('mousedown', closeOnOutside)
  })

  function pick(it) {
    open = false
    it.onSelect?.()
  }
</script>

{#if open}
  <div class="dropdown" style={style} role="menu">
    {#each items as it (it.label)}
      <button
        class="dropdown__item {it.tone === 'danger' ? 'dropdown__item--danger' : ''}"
        onclick={() => pick(it)}
      >{it.label}</button>
    {/each}
  </div>
{/if}

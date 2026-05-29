<script>
  import { onMount, tick } from 'svelte'
  /** @type {{
   *   open?: boolean,
   *   items?: Array<{label:string, onSelect:()=>void, tone?:'danger'|'normal'}>,
   *   anchor?: HTMLElement,
   * }} */
  let { open = $bindable(false), items = [], anchor } = $props()

  // FIX (PR #11 dc-7): the file-level comment says "modal is the only
  // element with a shadow", yet .dropdown carried a box-shadow. We now
  // render the dropdown as a flat bordered popover (no shadow).
  let style = $state('')
  /** @type {HTMLDivElement | undefined} */
  let menuEl = $state(undefined)
  /** @type {number} */
  let cursor = $state(0)

  $effect(() => {
    if (open && anchor) {
      const r = anchor.getBoundingClientRect()
      style = `top:${Math.round(r.bottom + 4)}px;right:${Math.round(window.innerWidth - r.right)}px;`
    }
  })

  // FIX (PR #11 a11y-14): keyboard pattern per WAI-ARIA menu.
  //   - On open: focus moves to the first item.
  //   - ArrowDown/ArrowUp: cycle through items (wraparound).
  //   - Home/End: jump to first/last.
  //   - Enter/Space: invoke focused item.
  //   - Escape: close + restore focus to anchor.
  //   - Tab: close (let focus leave naturally).
  let wasOpen = false
  $effect(() => {
    if (open && !wasOpen) {
      wasOpen = true
      cursor = 0
      tick().then(() => focusItem(0))
    } else if (!open && wasOpen) {
      wasOpen = false
    }
  })

  function focusItem(i) {
    if (!menuEl) return
    const btns = menuEl.querySelectorAll('button.dropdown__item')
    const idx = Math.max(0, Math.min(btns.length - 1, i))
    /** @type {HTMLElement | undefined} */ (btns[idx])?.focus()
    cursor = idx
  }

  onMount(() => {
    const closeOnOutside = (e) => {
      if (!open) return
      if (anchor && (anchor === e.target || anchor.contains(e.target))) return
      if (menuEl && (menuEl === e.target || menuEl.contains(e.target))) return
      open = false
    }
    document.addEventListener('mousedown', closeOnOutside)
    return () => document.removeEventListener('mousedown', closeOnOutside)
  })

  function pick(it) {
    open = false
    it.onSelect?.()
    // Restore focus to the anchor after activation.
    queueMicrotask(() => anchor?.focus?.())
  }

  function onKey(e) {
    if (!open) return
    if (e.key === 'Escape') {
      e.preventDefault()
      open = false
      queueMicrotask(() => anchor?.focus?.())
      return
    }
    if (e.key === 'Tab') {
      // Let Tab close the menu so focus moves naturally to the next
      // focusable element in the document.
      open = false
      return
    }
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      focusItem((cursor + 1) % Math.max(1, items.length))
      return
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault()
      focusItem((cursor - 1 + items.length) % Math.max(1, items.length))
      return
    }
    if (e.key === 'Home') {
      e.preventDefault()
      focusItem(0)
      return
    }
    if (e.key === 'End') {
      e.preventDefault()
      focusItem(items.length - 1)
      return
    }
  }
</script>

{#if open}
  <div
    bind:this={menuEl}
    class="dropdown"
    style={style}
    role="menu"
    tabindex="-1"
    onkeydown={onKey}
  >
    {#each items as it, i (it.label)}
      <button
        type="button"
        role="menuitem"
        class="dropdown__item {it.tone === 'danger' ? 'dropdown__item--danger' : ''}"
        tabindex={i === cursor ? 0 : -1}
        onfocus={() => { cursor = i }}
        onclick={() => pick(it)}
      >{it.label}</button>
    {/each}
  </div>
{/if}

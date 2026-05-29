<script>
  import { tick } from 'svelte'

  /** @type {{
   *   open?: boolean,
   *   title?: string,
   *   width?: number,
   *   onClose?: ()=>void,
   *   children?: any,
   *   footer?: any,
   * }} */
  let { open = $bindable(false), title = '', width = 600, onClose, children, footer } = $props()

  // FIX-11 (PR #11 a11y-03): focus trap + Escape close + focus restore.
  //
  //   - When the modal opens, we record document.activeElement so we can
  //     restore focus when it closes (return the user to where they were).
  //   - We focus the first focusable element inside the modal on open so
  //     keyboard users land somewhere meaningful.
  //   - Tab / Shift+Tab are intercepted at the modal root and cycled
  //     through the modal's own focusables — Tab cannot escape into the
  //     page behind.
  //   - Escape closes the modal.
  /** @type {HTMLDivElement|undefined} */
  let modalEl = $state(undefined)
  /** @type {Element|null} */
  let prevFocus = null

  function close() {
    open = false
    onClose?.()
  }
  function onBack(e) {
    if (e.target === e.currentTarget) close()
  }

  // Map of focusable selectors we care about. Excludes elements that are
  // disabled, hidden, or explicitly tabindex="-1".
  const FOCUSABLE = [
    'a[href]',
    'button:not([disabled])',
    'textarea:not([disabled])',
    'input:not([disabled])',
    'select:not([disabled])',
    '[tabindex]:not([tabindex="-1"])',
  ].join(',')

  function getFocusables() {
    if (!modalEl) return []
    return /** @type {HTMLElement[]} */ (
      Array.from(modalEl.querySelectorAll(FOCUSABLE))
        .filter((el) => /** @type {HTMLElement} */(el).offsetParent !== null
          || /** @type {HTMLElement} */(el).getClientRects().length > 0)
    )
  }

  function onKey(e) {
    if (e.key === 'Escape') {
      e.preventDefault()
      close()
      return
    }
    if (e.key !== 'Tab') return
    const items = getFocusables()
    if (items.length === 0) {
      e.preventDefault()
      modalEl?.focus()
      return
    }
    const first = items[0]
    const last  = items[items.length - 1]
    const active = document.activeElement
    if (e.shiftKey && active === first) {
      e.preventDefault()
      last.focus()
    } else if (!e.shiftKey && active === last) {
      e.preventDefault()
      first.focus()
    }
  }

  // React to open transitions — record/restore focus.
  let wasOpen = false
  $effect(() => {
    if (open && !wasOpen) {
      wasOpen = true
      prevFocus = typeof document !== 'undefined' ? document.activeElement : null
      // Wait for the modal to mount, then focus the first focusable.
      tick().then(() => {
        const items = getFocusables()
        if (items.length > 0) items[0].focus()
        else modalEl?.focus()
      })
    } else if (!open && wasOpen) {
      wasOpen = false
      // Restore focus to whatever was focused before the modal opened.
      if (prevFocus && typeof (/** @type {any} */(prevFocus).focus) === 'function') {
        try { /** @type {any} */(prevFocus).focus() } catch { /* ignore */ }
      }
      prevFocus = null
    }
  })
</script>

{#if open}
  <div class="modal-back" onclick={onBack} role="presentation">
    <div
      bind:this={modalEl}
      class="modal"
      style="width:{width}px"
      role="dialog"
      aria-modal="true"
      aria-label={title}
      tabindex="-1"
      onkeydown={onKey}
    >
      <div class="modal__head">{title}</div>
      <div class="modal__body">{@render children?.()}</div>
      {#if footer}<div class="modal__foot">{@render footer()}</div>{/if}
    </div>
  </div>
{/if}

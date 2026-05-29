// Focus-trap helper shared between Modal.svelte and CommandPalette.svelte
// (A11Y-1). Pulled out into a plain JS module so it can be unit-tested in
// jsdom — rendering Svelte components hits an upstream Vite preprocessor
// edge case in this codebase, but the trap itself is pure DOM logic.
//
// Usage:
//
//   onkeydown = (ev) => {
//     if (handleFocusTrap(ev, rootEl)) return
//     ...other key handling...
//   }
//
// Returns true if the event was a Tab that the trap consumed (caller
// should early-out), false otherwise.

const FOCUSABLE = [
  'a[href]',
  'button:not([disabled])',
  'textarea:not([disabled])',
  'input:not([disabled])',
  'select:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(',')

/**
 * Collect focusable elements within a root, filtering out hidden ones.
 * Visibility is approximated via offsetParent + getClientRects (the same
 * cheap check Modal.svelte already uses). jsdom returns 0 rects and
 * offsetParent=null for every element, so we accept connected elements
 * as a final fallback — that keeps the helper unit-testable while still
 * filtering out detached / display:none nodes in real browsers.
 * @param {Element | null | undefined} root
 * @returns {HTMLElement[]}
 */
export function getFocusables(root) {
  if (!root) return []
  return /** @type {HTMLElement[]} */ (
    Array.from(root.querySelectorAll(FOCUSABLE))
      .filter((el) => {
        const e = /** @type {HTMLElement} */(el)
        if (e.offsetParent !== null) return true
        if (e.getClientRects().length > 0) return true
        // jsdom fallback: layout isn't computed, so accept anything
        // connected to the document. Real browsers will have already
        // matched one of the two checks above for visible elements.
        return e.isConnected
      })
  )
}

/**
 * Trap Tab / Shift+Tab inside `root`. Cycles last->first and first->last.
 * Falls back to focusing `root` itself when no focusables exist.
 *
 * @param {KeyboardEvent} ev
 * @param {HTMLElement | null | undefined} root
 * @returns {boolean} true if the event was a Tab and was handled.
 */
export function handleFocusTrap(ev, root) {
  if (ev.key !== 'Tab') return false
  const items = getFocusables(root)
  if (items.length === 0) {
    ev.preventDefault()
    root?.focus()
    return true
  }
  const first = items[0]
  const last = items[items.length - 1]
  const active = typeof document !== 'undefined' ? document.activeElement : null
  if (ev.shiftKey && active === first) {
    ev.preventDefault()
    last.focus()
    return true
  }
  if (!ev.shiftKey && active === last) {
    ev.preventDefault()
    first.focus()
    return true
  }
  return false
}

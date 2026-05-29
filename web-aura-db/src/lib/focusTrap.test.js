// A11Y-1 focus-trap unit tests. Exercises the trap behavior on a
// hand-rolled DOM (jsdom) without rendering the full CommandPalette
// component (see SqlEditor.test.js for the upstream Vite preprocessor
// edge case that prevents rendering complex .svelte files in jsdom).

import { describe, it, expect, beforeEach } from 'vitest'
import { handleFocusTrap, getFocusables } from './focusTrap.js'

/** @returns {HTMLElement} */
function buildRoot() {
  // Three focusables (input, button1, button2) inside a dialog root.
  // jsdom returns offsetParent=null for nodes outside the document, so
  // attach to document.body.
  document.body.innerHTML = `
    <div id="root" tabindex="-1">
      <input id="search" />
      <button id="b1">b1</button>
      <button id="b2">b2</button>
      <button id="dis" disabled>nope</button>
      <span tabindex="-1">no-tab</span>
    </div>
  `
  return /** @type {HTMLElement} */(document.getElementById('root'))
}

/** @returns {KeyboardEvent} */
function tabEvent(shiftKey = false) {
  // jsdom KeyboardEvent supports the relevant fields we read.
  let prevented = false
  const ev = /** @type {any} */ ({
    key: 'Tab',
    shiftKey,
    preventDefault() { prevented = true },
    get defaultPrevented() { return prevented },
  })
  return /** @type {KeyboardEvent} */(ev)
}

beforeEach(() => {
  document.body.innerHTML = ''
})

describe('getFocusables', () => {
  it('returns enabled, tabbable elements only', () => {
    const root = buildRoot()
    const items = getFocusables(root)
    const ids = items.map((el) => el.id)
    expect(ids).toContain('search')
    expect(ids).toContain('b1')
    expect(ids).toContain('b2')
    expect(ids).not.toContain('dis')  // disabled
    // tabindex=-1 elements (incl. root) are excluded.
    expect(ids).not.toContain('root')
  })

  it('returns [] when root is null', () => {
    expect(getFocusables(null)).toEqual([])
  })
})

describe('handleFocusTrap', () => {
  it('Tab from the last focusable cycles to the first', () => {
    const root = buildRoot()
    const items = getFocusables(root)
    const first = items[0]
    const last = items[items.length - 1]
    last.focus()
    expect(document.activeElement).toBe(last)
    const ev = tabEvent(false)
    const handled = handleFocusTrap(ev, root)
    expect(handled).toBe(true)
    expect(/** @type {any} */(ev).defaultPrevented).toBe(true)
    expect(document.activeElement).toBe(first)
  })

  it('Shift+Tab from the first focusable cycles to the last', () => {
    const root = buildRoot()
    const items = getFocusables(root)
    const first = items[0]
    const last = items[items.length - 1]
    first.focus()
    expect(document.activeElement).toBe(first)
    const ev = tabEvent(true)
    const handled = handleFocusTrap(ev, root)
    expect(handled).toBe(true)
    expect(document.activeElement).toBe(last)
  })

  it('mid-list Tab is not consumed (browser handles native sequence)', () => {
    const root = buildRoot()
    const items = getFocusables(root)
    items[0].focus()
    const ev = tabEvent(false)
    const handled = handleFocusTrap(ev, root)
    expect(handled).toBe(false)
    expect(/** @type {any} */(ev).defaultPrevented).toBe(false)
  })

  it('non-Tab keys are ignored', () => {
    const root = buildRoot()
    const ev = /** @type {any} */ ({ key: 'Enter', shiftKey: false, preventDefault() {} })
    expect(handleFocusTrap(ev, root)).toBe(false)
  })

  it('falls back to focusing the root when no focusables exist', () => {
    document.body.innerHTML = `<div id="root" tabindex="-1"></div>`
    const root = /** @type {HTMLElement} */(document.getElementById('root'))
    const ev = tabEvent(false)
    const handled = handleFocusTrap(ev, root)
    expect(handled).toBe(true)
    expect(document.activeElement).toBe(root)
  })
})

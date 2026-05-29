<script>
  import { icons } from '../icons.js'

  /** @type {{
   *   id: string,
   *   title: string,
   *   active?: boolean,
   *   onActivate?: ()=>void,
   *   onClose?: ()=>void,
   *   closeLabel?: string,
   *   ariaControls?: string,
   * }} */
  // id is part of the public Tab type for keyed-list parents; not used in markup.
  // eslint-disable-next-line no-unused-vars
  let { id: _id, title, active = false, onActivate, onClose, closeLabel, ariaControls } = $props()

  function activate(e) {
    onActivate?.(e)
  }
  function close(e) {
    e.stopPropagation()
    onClose?.()
  }
  function onKey(e) {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      activate(e)
    }
  }
</script>

<!-- FIX (PR #11 a11y-12): expose the full title via title= so truncated
     long connection names are still readable on hover and announced by
     SR (the close button has its own aria-label that includes the
     title). -->
<!-- FIX (PR #11 a11y-05 follow-up): drop role=tab since the parent is
     now role=toolbar, not role=tablist. The button still behaves as a
     pressable workspace switcher; pressed state tracks the route. -->
<div
  class="tab {active ? 'tab--active' : ''}"
  role="button"
  tabindex="0"
  aria-pressed={active}
  aria-controls={ariaControls}
  title={title}
  onclick={activate}
  onkeydown={onKey}
>
  <span class="tab__title">{title}</span>
  <!-- FIX (PR #11 a11y-13 / dc-3): the close button was visibility:hidden
       until the row hovered, so keyboard users could never see/find it.
       It is now always visible (faded) and animates to full opacity on
       hover for a calmer resting state. -->
  <button
    type="button"
    class="tab__close"
    aria-label={closeLabel || `Close ${title}`}
    onclick={close}
    onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); close(e) } }}
  >
    <svg width="10" height="10" viewBox="0 0 12 12" aria-hidden="true">
      <path d={icons.x} stroke="currentColor" stroke-width="1.5" stroke-linecap="round" fill="none" />
    </svg>
  </button>
</div>

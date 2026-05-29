<script>
  import { icons } from '../icons.js'

  /** @type {{
   *   id: string,
   *   title: string,
   *   active?: boolean,
   *   onActivate?: ()=>void,
   *   onClose?: ()=>void,
   * }} */
  // id is part of the public Tab type for keyed-list parents; not used in markup.
  // eslint-disable-next-line no-unused-vars
  let { id: _id, title, active = false, onActivate, onClose } = $props()

  function activate(e) {
    // Treat the wrapper as the tab activator. The inner close zone bubbles
    // would re-fire onActivate otherwise — so close handles stopPropagation.
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

<div
  class="tab {active ? 'tab--active' : ''}"
  role="tab"
  tabindex="0"
  aria-selected={active}
  onclick={activate}
  onkeydown={onKey}
>
  <span>{title}</span>
  <span
    class="tab__close"
    role="button"
    tabindex="0"
    aria-label="close tab"
    onclick={close}
    onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); close(e) } }}
  >
    <svg width="10" height="10" viewBox="0 0 12 12" aria-hidden="true">
      <path d={icons.x} stroke="currentColor" stroke-width="1.5" stroke-linecap="round" fill="none" />
    </svg>
  </span>
</div>

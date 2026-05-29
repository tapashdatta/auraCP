<script>
  /** @type {{
   *   onResize?: (newWidth: number) => void,
   *   min?: number,
   *   max?: number,
   *   startWidth?: number,
   *   ariaLabel?: string,
   *   step?: number,
   * }} */
  let { onResize, min = 220, max = 480, startWidth = 280, ariaLabel = 'Resize panel', step = 16 } = $props()

  let active = $state(false)
  let dragStartX = 0
  let dragStartW = startWidth
  let current = $state(startWidth)

  $effect(() => { current = startWidth })

  function onDown(e) {
    active = true
    dragStartX = e.clientX
    dragStartW = current
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp, { once: true })
    e.preventDefault()
  }
  function onMove(e) {
    const dx = e.clientX - dragStartX
    const w = Math.min(max, Math.max(min, dragStartW + dx))
    current = w
    onResize?.(w)
  }
  function onUp() {
    active = false
    window.removeEventListener('mousemove', onMove)
  }
  // FIX (PR #11 a11y-21): keyboard-only operators couldn't drive the
  // resize handle. ArrowLeft/ArrowRight nudge by `step` px; Home/End
  // jump to min/max. The handle still uses role=separator + a tabindex
  // so focus reaches it.
  function onKey(e) {
    let next = current
    if (e.key === 'ArrowLeft')  next = Math.max(min, current - step)
    else if (e.key === 'ArrowRight') next = Math.min(max, current + step)
    else if (e.key === 'Home') next = min
    else if (e.key === 'End')  next = max
    else return
    e.preventDefault()
    current = next
    onResize?.(next)
  }
</script>

<div
  class="resize-handle {active ? 'resize-handle--active' : ''}"
  role="separator"
  aria-orientation="vertical"
  aria-label={ariaLabel}
  aria-valuenow={current}
  aria-valuemin={min}
  aria-valuemax={max}
  tabindex="0"
  onmousedown={onDown}
  onkeydown={onKey}
></div>

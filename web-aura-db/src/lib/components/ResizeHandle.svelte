<script>
  /** @type {{
   *   onResize?: (newWidth: number) => void,
   *   min?: number,
   *   max?: number,
   *   startWidth?: number,
   * }} */
  let { onResize, min = 220, max = 480, startWidth = 280 } = $props()

  let active = $state(false)
  let dragStartX = 0
  let dragStartW = startWidth

  function onDown(e) {
    active = true
    dragStartX = e.clientX
    dragStartW = startWidth
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp, { once: true })
    e.preventDefault()
  }
  function onMove(e) {
    const dx = e.clientX - dragStartX
    const w = Math.min(max, Math.max(min, dragStartW + dx))
    onResize?.(w)
  }
  function onUp() {
    active = false
    window.removeEventListener('mousemove', onMove)
  }
</script>

<div
  class="resize-handle {active ? 'resize-handle--active' : ''}"
  role="separator"
  aria-orientation="vertical"
  onmousedown={onDown}
></div>

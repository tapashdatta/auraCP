<script>
  // FIX (PR #11 a11y-16): canonical WAI-ARIA toggle uses role=switch with
  // aria-checked, not aria-pressed. Keyboard Space/Enter toggle. We keep
  // the visual rail markup but expose semantics that match
  // explain/AnalyzeToggle which already used role=switch.
  /** @type {{ value?: boolean, label?: string, disabled?: boolean, ariaLabel?: string }} */
  let { value = $bindable(false), label, disabled = false, ariaLabel } = $props()
  function toggle() {
    if (disabled) return
    value = !value
  }
  function onKey(e) {
    if (disabled) return
    if (e.key === ' ' || e.key === 'Enter') {
      e.preventDefault()
      toggle()
    }
  }
</script>

<button
  type="button"
  class="toggle {value ? 'toggle--on' : ''}"
  role="switch"
  aria-checked={value}
  aria-label={ariaLabel || label || undefined}
  disabled={disabled}
  onclick={toggle}
  onkeydown={onKey}
>
  <span class="toggle__rail" aria-hidden="true"></span>
  {#if label}<span class="toggle__label">{label}</span>{/if}
</button>

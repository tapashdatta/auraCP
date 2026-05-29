<script>
  /** @type {{ value?: boolean, label?: string, disabled?: boolean }} */
  let { value = $bindable(false), label, disabled = false } = $props()
  function toggle() {
    if (disabled) return
    value = !value
  }
</script>

<button
  type="button"
  class="toggle {value ? 'toggle--on' : ''}"
  aria-pressed={value}
  disabled={disabled}
  onclick={toggle}
>
  <span class="toggle__rail" aria-hidden="true"></span>
  {#if label}<span class="toggle__label">{label}</span>{/if}
</button>

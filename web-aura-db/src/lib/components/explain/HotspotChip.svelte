<script>
  /**
   * @type {{ kind?: 'estimate'|'loops'|'warning', label?: string }}
   */
  let { kind = 'warning', label } = $props()
</script>

<span class="hotspot-chip hotspot-chip--{kind}" title={label || kind}>
  <span class="hotspot-chip__glyph" aria-hidden="true">!</span>
  <span class="hotspot-chip__label">{label || (kind === 'estimate' ? 'estimate' : kind === 'loops' ? 'loops' : 'warn')}</span>
</span>

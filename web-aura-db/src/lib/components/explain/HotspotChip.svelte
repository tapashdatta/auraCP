<script>
  /**
   * @type {{ kind?: 'estimate'|'loops'|'warning', label?: string }}
   */
  let { kind = 'warning', label } = $props()

  // FIX (PR #14.5 A11Y-14 routed to PR #11.5): the chip leaned on title=
  // (only visible on mouse hover) and the short label was ambiguous for
  // SR users. Pair the visual short label with a longer aria-label so
  // keyboard and AT users get the full meaning.
  const longLabel = $derived.by(() => {
    if (label) return label
    if (kind === 'estimate') return 'Row estimate mismatch'
    if (kind === 'loops') return 'High loop count'
    return 'Plan warning'
  })
  const shortLabel = $derived(label || (kind === 'estimate' ? 'estimate' : kind === 'loops' ? 'loops' : 'warn'))
</script>

<span
  class="hotspot-chip hotspot-chip--{kind}"
  title={longLabel}
  aria-label={longLabel}
  role="note"
>
  <span class="hotspot-chip__glyph" aria-hidden="true">!</span>
  <span class="hotspot-chip__label">{shortLabel}</span>
</span>

<script>
  import { fmtMs, fmtRows, kFormat } from '../../sqlEditor/explainFormat.js'
  import { nodeCount } from '../../sqlEditor/explainFlatten.js'

  /**
   * @type {{
   *   plan: any,
   *   onFilter?: (kind: 'warnings'|'buffers'|'rows'|'time') => void,
   * }}
   */
  let { plan, onFilter } = $props()

  const total = $derived(plan?.total || {})
  const isMaria = $derived(plan?.engine === 'mariadb')
  const analyzed = $derived(plan?.engine === 'postgres' && (plan?.executionTimeMs || 0) > 0)
  const nCount = $derived(nodeCount(plan?.root))
  const warnings = $derived(plan?.warnings || [])

  // FIX CORR-1 (PR #14): EXEC TIME is meaningless until ANALYZE runs;
  // MariaDB never produces a planning time. Show em-dash, not 0.00ms,
  // so users don't mistake "unknown" for "instantaneous".
  const planTimeStr = $derived(
    (isMaria && !(plan?.planningTimeMs > 0)) ? '—' : fmtMs(plan?.planningTimeMs),
  )
  const execTimeStr = $derived(
    analyzed ? fmtMs(plan?.executionTimeMs) : '—',
  )

  let popoverOpen = $state(false)
  /** @type {HTMLDivElement|undefined} */
  let popEl = $state(undefined)
  /** @type {HTMLButtonElement|undefined} */
  let warnBtn = $state(undefined)

  function toggleWarn() {
    popoverOpen = !popoverOpen
    if (popoverOpen) {
      queueMicrotask(() => popEl?.focus())
    }
  }
  // FIX (PR #14.5 A11Y-9 routed to PR #11.5): the previous popover
  // claimed role=dialog without a focus trap, Esc handler, or focus
  // restore. We're not promoting it to a full Modal (the metric chip
  // anchor pattern wouldn't survive that), but we do add: Esc-to-close,
  // outside-click-to-close, focus on open, and restore on close.
  function onPopKey(e) {
    if (e.key === 'Escape') {
      e.preventDefault()
      popoverOpen = false
      queueMicrotask(() => warnBtn?.focus())
    }
  }
  $effect(() => {
    if (!popoverOpen) return
    const onDocClick = (ev) => {
      if (!popEl) return
      if (popEl === ev.target || popEl.contains(ev.target)) return
      if (warnBtn && (warnBtn === ev.target || warnBtn.contains(ev.target))) return
      popoverOpen = false
    }
    document.addEventListener('mousedown', onDocClick)
    return () => document.removeEventListener('mousedown', onDocClick)
  })
</script>

<div class="metrics-ribbon" role="group" aria-label="Plan summary metrics">
  <button class="metric" type="button" title={`Planning time: ${plan?.planningTimeMs || 0}ms`} onclick={() => onFilter?.('time')}>
    <span class="metric__label">PLAN TIME</span>
    <span class="metric__value num" class:metric__value--dim={planTimeStr === '—' || !plan?.planningTimeMs}>{planTimeStr}</span>
  </button>

  <button class="metric" type="button" title={`Execution time: ${plan?.executionTimeMs || 0}ms`} onclick={() => onFilter?.('time')}>
    <span class="metric__label">EXEC TIME</span>
    <span class="metric__value num" class:metric__value--dim={!analyzed}>{execTimeStr}</span>
  </button>

  <div class="metric metric--static">
    <span class="metric__label">NODES</span>
    <span class="metric__value num">{kFormat(nCount)}</span>
  </div>

  <button class="metric" type="button" title="Click to highlight rows-actual mismatches" onclick={() => onFilter?.('rows')}>
    <span class="metric__label">{analyzed ? 'ROWS ACTUAL' : 'ROWS PLAN'}</span>
    <span class="metric__value num">{fmtRows(analyzed ? total.rowsActual : total.rowsExpected)}</span>
  </button>

  <button class="metric" type="button" title="Click to highlight nodes that read from disk" onclick={() => onFilter?.('buffers')} disabled={isMaria}>
    <span class="metric__label">BUFFERS</span>
    <span class="metric__value num" class:metric__value--dim={isMaria}>
      {#if isMaria}—{:else}{fmtRows(total.buffersHit)}H · {fmtRows(total.buffersRead)}R{/if}
    </span>
  </button>

  <button
    class="metric metric--warn"
    type="button"
    bind:this={warnBtn}
    aria-expanded={popoverOpen}
    aria-haspopup="dialog"
    aria-controls="metrics-warn-popover"
    onclick={toggleWarn}
    disabled={warnings.length === 0}
  >
    <span class="metric__label">WARNINGS</span>
    <span class="metric__value num" class:metric__value--bad={warnings.length > 0}>{warnings.length}</span>
  </button>

  {#if popoverOpen && warnings.length > 0}
    <div
      id="metrics-warn-popover"
      bind:this={popEl}
      class="metric__popover"
      role="dialog"
      aria-label="Plan warnings"
      tabindex="-1"
      onkeydown={onPopKey}
    >
      <ul class="metric__warnList">
        {#each warnings as w (w)}
          <li>{w}</li>
        {/each}
      </ul>
    </div>
  {/if}

  <span class="metric__engine" data-engine={plan?.engine || ''}>{plan?.engine || ''}</span>
</div>

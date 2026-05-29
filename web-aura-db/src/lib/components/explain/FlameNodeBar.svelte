<script>
  import { fmtCost, fmtRows, fmtMs, fmtPct } from '../../sqlEditor/explainFormat.js'
  import HotspotChip from './HotspotChip.svelte'

  /**
   * @type {{
   *   entry: import('../../sqlEditor/explainFlatten.js').FlatEntry,
   *   domId?: string,
   *   y: number,
   *   barX: number,
   *   barWidth: number,
   *   rowHeight: number,
   *   colorStep: 1|2|3|4|5,
   *   selected: boolean,
   *   dimmed: boolean,
   *   share: number,
   *   hotspotMode: boolean,
   *   hotspot: { estimate: boolean, loops: boolean },
   *   analyzed: boolean,
   *   engine: string,
   *   onSelect: (id: string) => void,
   *   onToggle: (id: string) => void,
   * }}
   */
  let {
    entry,
    domId,
    y,
    barX,
    barWidth,
    rowHeight,
    colorStep,
    selected,
    dimmed,
    share,
    hotspotMode,
    hotspot,
    analyzed,
    engine,
    onSelect,
    onToggle,
  } = $props()

  const node = $derived(entry.node)
  const m = $derived(node.metrics || {})

  // FIX CORR-3 (PR #14): PG nodes with ANALYZE-set but loops=0 were
  // never executed. Avoid painting their misleading-zero metrics in the
  // bar tail / hotspot chips.
  const notExecuted = $derived(analyzed && engine === 'postgres' && m.loops === 0)

  const showHotEstimate = $derived(!notExecuted && hotspot.estimate)
  const showHotLoops = $derived(!notExecuted && hotspotMode && hotspot.loops)

  // Stringify the metric tail. Avoid zero-value misreporting on MariaDB.
  const tail = $derived.by(() => {
    if (notExecuted) return 'not executed'
    const parts = []
    if (m.costTotal > 0) parts.push('cost ' + fmtCost(m.costTotal))
    if (m.rowsExpected > 0) parts.push('rows ' + fmtRows(m.rowsExpected))
    if (analyzed && m.timeTotalMs > 0) parts.push(fmtMs(m.timeTotalMs))
    if (analyzed && m.loops > 1) parts.push('×' + m.loops + ' loops')
    return parts.join(' · ')
  })

  function clickBar() { onSelect?.(entry.id) }
  function clickChevron(ev) {
    ev.stopPropagation()
    onToggle?.(entry.id)
  }
</script>

<g
  id={domId}
  class="flame-row"
  data-id={entry.id}
  data-selected={selected ? 'true' : null}
  data-hotspot={showHotEstimate ? 'estimate' : (showHotLoops ? 'loops' : null)}
  data-dim={dimmed ? 'true' : null}
  transform="translate(0,{y})"
  role="treeitem"
  aria-level={entry.depth + 1}
  aria-expanded={entry.hasChildren ? entry.expanded : undefined}
  aria-selected={selected}
  aria-label={`${node.kind}${node.relation ? ' on ' + node.relation : ''}, ${fmtPct(share)} of total`}
>
  <rect
    class="flame-row__bar"
    data-step={colorStep}
    x={barX}
    y={2}
    width={Math.max(8, barWidth)}
    height={rowHeight - 4}
    rx="2"
    role="presentation"
    aria-hidden="true"
    pointer-events="none"
  ></rect>
  <foreignObject x={barX + 4} y={2} width={Math.max(8, barWidth - 8)} height={rowHeight - 4}>
    <div
      class="flame-row__content"
      role="presentation"
      onclick={clickBar}
      onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); clickBar() } }}
    >
      {#if entry.hasChildren}
        <!-- svelte-ignore a11y_consider_explicit_label -->
        <button
          type="button"
          class="flame-row__chevron"
          aria-label={entry.expanded ? 'Collapse' : 'Expand'}
          onclick={clickChevron}
          tabindex="-1"
        >{entry.expanded ? '▾' : '▸'}</button>
      {:else}
        <span class="flame-row__chevron flame-row__chevron--leaf" aria-hidden="true">·</span>
      {/if}
      <span class="flame-row__kind" data-engine={engine}>{node.kind}</span>
      {#if node.relation}
        <span class="flame-row__relation">{node.schema ? node.schema + '.' : ''}{node.relation}{node.alias ? ' AS ' + node.alias : ''}</span>
      {/if}
      {#if node.index}
        <span class="flame-row__index">via {node.index}</span>
      {/if}
      {#if tail}
        <span class="flame-row__tail">{tail}</span>
      {/if}
      {#if showHotEstimate}
        <HotspotChip kind="estimate" label="estimate off >10x" />
      {/if}
      {#if showHotLoops}
        <HotspotChip kind="loops" label={`${node.metrics?.loops || 0} loops`} />
      {/if}
      <span class="flame-row__pct num">{fmtPct(share)}</span>
    </div>
  </foreignObject>
</g>

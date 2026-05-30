<script>
  // Pure-SVG flame-tree visualization for an EXPLAIN plan. ~150 LOC of
  // layout helper; no charting library. Each visible node renders as one
  // SVG row (bar + foreignObject content). Width is proportional to the
  // share-of-total metric (time when analyzed; cost otherwise).
  //
  // Interaction model:
  //   - Click a bar → onSelect(id)
  //   - Click chevron → onToggleExpand(id)
  //   - Container is role=tree with aria-activedescendant pointing at the
  //     selected row, matching WAI-ARIA 1.2 tree pattern.
  //
  // Performance: trees with >256 visible rows enter a simple windowed
  // render — we slice the FlatEntry[] to the visible viewport plus a
  // 6-row overscan so the SVG never holds more than ~80 <g> elements at
  // once, even for 10k-node trees (the walker cap).

  import FlameNodeBar from './FlameNodeBar.svelte'
  import { flattenPlan, allIds, nodeAt } from '../../sqlEditor/explainFlatten.js'
  import { shareFor, costStep, hotspotFlags } from '../../sqlEditor/explainFormat.js'

  /**
   * @type {{
   *   plan: any,
   *   selectedId: string,
   *   expanded: Set<string>,
   *   searchTerm: string,
   *   hotspotMode: boolean,
   *   onSelect: (id: string) => void,
   *   onToggleExpand: (id: string) => void,
   *   onExpandAll?: (ids: string[]) => void,
   * }}
   */
  let {
    plan,
    selectedId,
    expanded,
    searchTerm,
    hotspotMode,
    onSelect,
    onToggleExpand,
    onExpandAll,
  } = $props()

  // FIX DC-2 (PR #14.5): align to the 24px tree grid used by
  // LeftTree. The 22px value created a 2-pixel cadence drift between
  // the editor's object tree and the inspector's flame tree —
  // noticeable when both are on screen during the editor↔inspector
  // round-trip. The bar itself shrinks by 2px height-2 so vertical
  // spacing inside the row stays balanced.
  const ROW_H = 24
  const INDENT_X = 14
  const RAIL_W = 14
  const RIGHT_GUTTER = 24

  /** @type {HTMLDivElement|undefined} */
  let host = $state(undefined)
  let width = $state(640)
  let viewportH = $state(420)
  let scrollTop = $state(0)

  $effect(() => {
    if (!host) return
    const ro = new ResizeObserver((entries) => {
      for (const e of entries) {
        width = Math.max(320, Math.round(e.contentRect.width))
        viewportH = Math.max(160, Math.round(e.contentRect.height))
      }
    })
    ro.observe(host)
    return () => ro.disconnect()
  })

  const entries = $derived(flattenPlan(plan?.root, expanded, searchTerm))
  const totalHeight = $derived(entries.length * ROW_H)
  const analyzed = $derived(plan?.engine === 'postgres' && (plan?.executionTimeMs || 0) > 0)
  const engine = $derived(plan?.engine || 'postgres')

  // Visible window with overscan.
  const window_ = $derived.by(() => {
    const start = Math.max(0, Math.floor(scrollTop / ROW_H) - 6)
    const end = Math.min(entries.length, Math.ceil((scrollTop + viewportH) / ROW_H) + 6)
    return { start, end }
  })

  function onScroll(ev) {
    scrollTop = ev.currentTarget.scrollTop
  }

  // Keyboard navigation — model matches the WAI-ARIA tree pattern.
  function onKeydown(ev) {
    if (!entries.length) return
    const idx = entries.findIndex((e) => e.id === selectedId)
    const cur = idx >= 0 ? idx : 0
    switch (ev.key) {
      case 'ArrowDown': {
        ev.preventDefault()
        const next = Math.min(entries.length - 1, cur + 1)
        onSelect?.(entries[next].id)
        scrollIntoView(next)
        break
      }
      case 'ArrowUp': {
        ev.preventDefault()
        const next = Math.max(0, cur - 1)
        onSelect?.(entries[next].id)
        scrollIntoView(next)
        break
      }
      case 'ArrowRight': {
        ev.preventDefault()
        const e = entries[cur]
        if (e?.hasChildren && !e.expanded) onToggleExpand?.(e.id)
        else if (e?.hasChildren && cur + 1 < entries.length) {
          onSelect?.(entries[cur + 1].id)
          scrollIntoView(cur + 1)
        }
        break
      }
      case 'ArrowLeft': {
        ev.preventDefault()
        // FIX A11Y-6 (PR #14): WAI-ARIA tree pattern for ArrowLeft —
        //   - expanded parent → collapse (do NOT move focus).
        //   - collapsed parent OR leaf → move focus to parent
        //     (never re-expand and never become a no-op).
        const e = entries[cur]
        if (e?.hasChildren && e.expanded) {
          onToggleExpand?.(e.id)
        } else if (e?.parentId) {
          const pIdx = entries.findIndex((x) => x.id === e.parentId)
          if (pIdx >= 0) { onSelect?.(entries[pIdx].id); scrollIntoView(pIdx) }
        }
        break
      }
      case 'Home':
        ev.preventDefault(); onSelect?.(entries[0].id); scrollIntoView(0); break
      case 'End':
        ev.preventDefault(); onSelect?.(entries[entries.length - 1].id); scrollIntoView(entries.length - 1); break
      case 'Enter':
      case ' ':
        ev.preventDefault()
        if (entries[cur]?.hasChildren) onToggleExpand?.(entries[cur].id)
        break
      case '*':
        ev.preventDefault()
        onExpandAll?.(allIds(plan?.root))
        break
      default: break
    }
  }

  function scrollIntoView(rowIdx) {
    if (!host) return
    const y = rowIdx * ROW_H
    const min = host.scrollTop
    const max = host.scrollTop + viewportH - ROW_H
    if (y < min) host.scrollTop = y
    else if (y > max) host.scrollTop = y - viewportH + ROW_H
  }

  // Layout helper: given an entry, return its render coordinates.
  function layout(entry) {
    const x = RAIL_W + entry.depth * INDENT_X
    const avail = Math.max(80, width - x - RIGHT_GUTTER)
    const share = shareFor(entry.node.metrics, plan)
    const w = Math.max(20, avail * share)
    return { x, w, share }
  }
</script>

<div class="flame-tree" bind:this={host} onscroll={onScroll}>
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <div
    class="flame-tree__inner"
    role="tree"
    aria-label="Query plan tree"
    aria-activedescendant={`flame-node-${selectedId}`}
    tabindex="0"
    onkeydown={onKeydown}
    style="height: {totalHeight}px"
  >
    <svg
      class="flame-tree__svg"
      width={width}
      height={totalHeight}
      role="presentation"
      xmlns="http://www.w3.org/2000/svg"
    >
      <!-- Depth-rails: thin guides parents → children -->
      {#each entries.slice(window_.start, window_.end) as entry, i (entry.id)}
        {@const idx = window_.start + i}
        {@const lay = layout(entry)}
        {@const step = costStep(lay.share)}
        {@const hot = hotspotFlags(entry.node.metrics)}
        {@const isDim = !entry.matchesSearch}
        <!-- FIX A11Y-3 (PR #14): aria-activedescendant must point at the
             element that carries role=treeitem. That role lives on the
             <g class="flame-row"> inside FlameNodeBar, so we pass the id
             down rather than wrap with a separate outer <g>. -->
        <FlameNodeBar
          {entry}
          domId={`flame-node-${entry.id}`}
          y={idx * ROW_H}
          barX={lay.x}
          barWidth={lay.w}
          rowHeight={ROW_H}
          colorStep={step}
          selected={entry.id === selectedId}
          dimmed={isDim}
          share={lay.share}
          hotspotMode={hotspotMode}
          hotspot={hot}
          analyzed={analyzed}
          engine={engine}
          searchTerm={searchTerm}
          onSelect={onSelect}
          onToggle={onToggleExpand}
        />
      {/each}
    </svg>
  </div>
</div>

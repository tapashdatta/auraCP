<script>
  // Read-only presentational grid for SQL result rows. Decoupled from the
  // rowgrid composable so it can render streaming data without coupling
  // to PK / edit / undo machinery (which only TableScreen cares about).
  //
  // a11y-05: restores WAI-ARIA grid roles (role=grid + row + gridcell +
  // aria-rowcount/colcount/rowindex) that the PR #12 TableScreen carries.
  //
  // EXEC-7: rows array is appended in-place by the caller (push), and we
  // virtualize the visible window so a 10k-row stream renders ~30 row
  // divs at a time. The window math reuses rowgrid/virtualWindow.js.

  import { virtualWindow } from '../../rowgrid/virtualWindow.js'

  /** @type {{
   *   columns?: Array<{name:string, databaseTypeName?:string, dataType?:string}>,
   *   rows?: Array<any[]>,
   *   streamingActive?: boolean,
   *   density?: 'compact'|'cozy'|'comfortable',
   * }} */
  let { columns = [], rows = [], streamingActive = false, density = 'cozy' } = $props()

  const rowH = $derived(density === 'compact' ? 24 : density === 'comfortable' ? 32 : 28)

  /** @type {HTMLDivElement|undefined} */
  let bodyEl = $state(undefined)
  let scrollTop = $state(0)
  let viewportH = $state(400)

  function onScroll(ev) {
    scrollTop = ev.currentTarget.scrollTop
  }

  $effect(() => {
    if (!bodyEl) return
    const ro = new ResizeObserver((entries) => {
      for (const e of entries) {
        viewportH = e.contentRect.height
      }
    })
    ro.observe(bodyEl)
    viewportH = bodyEl.clientHeight || 400
    return () => ro.disconnect()
  })

  const win = $derived(virtualWindow({
    scrollTop,
    viewportH,
    rowH,
    total: rows.length,
    buffer: 6,
  }))

  const visibleRows = $derived.by(() => {
    const out = []
    for (let i = win.startIdx; i < win.endIdx; i++) {
      out.push({ idx: i, row: rows[i] })
    }
    return out
  })

  const totalHeight = $derived(rows.length * rowH)

  function renderCell(v) {
    if (v === null || v === undefined) return ''
    if (typeof v === 'object') return JSON.stringify(v)
    const s = String(v)
    if (s.length > 200) return s.slice(0, 200) + '…'
    return s
  }
</script>

<div
  class="result-grid"
  data-density={density}
  role="grid"
  aria-rowcount={rows.length + 1}
  aria-colcount={columns.length}
>
  {#if columns.length === 0}
    <div class="result-grid__empty">No columns yet</div>
  {:else}
    <div class="result-grid__head" role="row" aria-rowindex={1}>
      {#each columns as col, j (col.name)}
        <div
          class="result-grid__cell result-grid__cell--head"
          role="columnheader"
          aria-colindex={j + 1}
          title={col.databaseTypeName || col.dataType || ''}
        >
          {col.name}
        </div>
      {/each}
    </div>
    <div
      class="result-grid__body"
      style:--row-h="{rowH}px"
      bind:this={bodyEl}
      onscroll={onScroll}
    >
      <div class="result-grid__spacer" style="height:{totalHeight}px;position:relative;">
        <div class="result-grid__viewport" style="position:absolute;left:0;right:0;top:{win.yOffset}px;">
          {#each visibleRows as item (item.idx)}
            <div
              class="result-grid__row"
              role="row"
              aria-rowindex={item.idx + 2}
              style="height:{rowH}px;"
            >
              {#each item.row as v, j (j)}
                <div
                  class="result-grid__cell"
                  role="gridcell"
                  aria-colindex={j + 1}
                  title={renderCell(v)}
                >{renderCell(v)}</div>
              {/each}
            </div>
          {/each}
        </div>
        {#if streamingActive}
          <div
            class="result-grid__row result-grid__row--streaming"
            role="row"
            aria-live="polite"
            aria-rowindex={rows.length + 2}
            style="position:absolute;left:0;right:0;top:{totalHeight}px;"
          >
            <div class="result-grid__cell" role="gridcell" aria-colindex={1} style="grid-column: 1 / -1;">streaming…</div>
          </div>
        {/if}
      </div>
    </div>
  {/if}
</div>

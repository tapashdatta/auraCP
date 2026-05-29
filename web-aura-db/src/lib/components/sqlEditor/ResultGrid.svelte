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
  //
  // ux-2 (PR #16): per-result-tab Export button. The /export endpoint
  // takes schema+table+filter+sort — it cannot run arbitrary SQL — so
  // for SqlEditor we fall back to CLIENT-side serialization of whatever
  // rows are currently in memory for the active tab. The download is
  // capped at the in-memory window; for full-result exports the user
  // must run the query from the TableScreen instead.

  import { virtualWindow } from '../../rowgrid/virtualWindow.js'

  /** @type {{
   *   columns?: Array<{name:string, databaseTypeName?:string, dataType?:string}>,
   *   rows?: Array<any[]>,
   *   streamingActive?: boolean,
   *   density?: 'compact'|'cozy'|'comfortable',
   * }} */
  let { columns = [], rows = [], streamingActive = false, density = 'cozy' } = $props()

  /** @type {'csv'|'ndjson'} */
  let exportFormat = $state('csv')
  let exportOpen = $state(false)
  let exportBusy = $state(false)
  let exportMsg = $state('')

  function toggleExportMenu() { exportOpen = !exportOpen }

  function cellToCSV(v) {
    if (v === null || v === undefined) return ''
    let s
    if (typeof v === 'object') s = JSON.stringify(v)
    else s = String(v)
    // SEC-1 mirror: client-side CSV must apply the same formula-injection
    // defence the server applies. Otherwise an attacker who controlled
    // a row value could weaponize the editor's local export.
    if (s.length > 0 && /^[=+\-@\t\r]/.test(s)) s = "'" + s
    if (/[",\r\n]/.test(s)) s = '"' + s.replace(/"/g, '""') + '"'
    return s
  }

  function cellToJSON(v) {
    if (v === null || v === undefined) return null
    if (typeof v === 'object') return v
    return v
  }

  function buildCSV() {
    const lines = []
    lines.push(columns.map((c) => cellToCSV(c.name)).join(','))
    for (const r of rows) lines.push(r.map(cellToCSV).join(','))
    return lines.join('\r\n') + '\r\n'
  }

  function buildNDJSON() {
    const out = []
    for (const r of rows) {
      /** @type {Record<string,any>} */
      const obj = {}
      for (let i = 0; i < columns.length; i++) obj[columns[i].name] = cellToJSON(r[i])
      out.push(JSON.stringify(obj))
    }
    return out.join('\n') + (out.length > 0 ? '\n' : '')
  }

  function exportNow() {
    if (exportBusy) return
    if (rows.length === 0) { exportMsg = 'no rows to export'; return }
    exportBusy = true
    try {
      const isCSV = exportFormat === 'csv'
      const text = isCSV ? buildCSV() : buildNDJSON()
      const blob = new Blob([text], {
        type: isCSV ? 'text/csv;charset=utf-8' : 'application/x-ndjson;charset=utf-8',
      })
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      const stamp = new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-')
      a.href = url
      a.download = `result-${stamp}.${isCSV ? 'csv' : 'ndjson'}`
      document.body.appendChild(a)
      a.click()
      a.remove()
      setTimeout(() => URL.revokeObjectURL(url), 1000)
      exportMsg = `exported ${rows.length} rows`
      exportOpen = false
    } catch (e) {
      exportMsg = (e && /** @type {any} */(e).message) || 'export failed'
    } finally {
      exportBusy = false
    }
  }

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
  {#if columns.length > 0}
    <div class="result-grid__toolbar" data-testid="result-grid-toolbar">
      <button
        type="button"
        class="result-grid__exportBtn"
        onclick={toggleExportMenu}
        aria-haspopup="menu"
        aria-expanded={exportOpen}
        title="Export rendered rows ({rows.length})"
      >Export ▾</button>
      {#if exportOpen}
        <div class="result-grid__exportMenu" role="menu">
          <label class="result-grid__exportRow">
            <input type="radio" name="rg-fmt" value="csv" checked={exportFormat === 'csv'} onchange={() => exportFormat = 'csv'} />
            CSV
          </label>
          <label class="result-grid__exportRow">
            <input type="radio" name="rg-fmt" value="ndjson" checked={exportFormat === 'ndjson'} onchange={() => exportFormat = 'ndjson'} />
            NDJSON
          </label>
          <button type="button" class="result-grid__exportGo" onclick={exportNow} disabled={exportBusy || rows.length === 0}>
            {exportBusy ? 'Saving…' : 'Save file'}
          </button>
          <div class="result-grid__exportNote">
            Exports the {rows.length} rendered rows. For full-table
            exports use the Table view.
          </div>
        </div>
      {/if}
      {#if exportMsg}
        <span class="result-grid__exportMsg" role="status" aria-live="polite">{exportMsg}</span>
      {/if}
    </div>
  {/if}
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

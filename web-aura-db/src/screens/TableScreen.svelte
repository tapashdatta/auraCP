<script>
  // Page-level row-grid screen. Owns useRowGrid state; renders toolbar +
  // filter + header + body + footer in a vertical grid layout. The body
  // is the single vertical scroller; header + filter sit inside it as
  // `position: sticky; top:0` siblings so they translate with horizontal
  // scroll naturally.
  //
  // All keyboard handling lives here (one onkeydown on .rowgrid root) and
  // routes through the pure keyToAction() dispatch table.

  import { onMount } from 'svelte'
  import { routeState } from '../lib/router.svelte.js'
  import { openTab } from '../lib/workspaces.svelte.js'
  import { createRowGrid } from '../lib/rowgrid/useRowGrid.svelte.js'
  import { virtualWindow, ariaRowIndex } from '../lib/rowgrid/virtualWindow.js'
  import { classifyKind, renderCell } from '../lib/rowgrid/cellRenderers.js'
  import { buildPKKey } from '../lib/rowgrid/pkKey.js'
  import { keyToAction } from '../lib/rowgrid/keyboard.js'
  import { toastBus, dismissToast } from '../lib/rowgrid/toasts.svelte.js'
  import { icons } from '../lib/icons.js'

  // ux-8 (PR #16.5): ExportModal is dynamic-imported on first open so
  // it doesn't ship in the main grid chunk (saves ~3 KB gzipped on
  // first paint). The component reference is held in a $state cell
  // and rendered only after it resolves.
  /** @type {any} */
  let ExportModalComponent = $state(null)
  let exportModalLoading = $state(false)
  async function ensureExportModal() {
    if (ExportModalComponent || exportModalLoading) return
    exportModalLoading = true
    try {
      const mod = await import('../lib/components/ExportModal.svelte')
      ExportModalComponent = mod.default
    } finally {
      exportModalLoading = false
    }
  }

  // Svelte action for autofocus + select-all on edit-input mount. Avoids
  // the autofocus attribute (which Svelte's a11y rules flag) while keeping
  // the same user experience.
  function focusInput(node) {
    queueMicrotask(() => { node.focus(); node.select?.() })
    return {}
  }

  // edit-11 (PR #12.5): the edit input's onblur previously committed the
  // value unconditionally. When the user pressed Escape, the keydown
  // handler called cancelEdit() (which un-renders the input) — but the
  // blur event then fired AFTER the input was removed and re-mounted, so
  // the cancel logically won by accident. That ordering wasn't guaranteed
  // (Safari's order differs from Chrome's on detach + focus moves) and
  // any future change to cancelEdit() that didn't immediately tear down
  // the input would silently start committing cancelled edits. Fix:
  // explicitly suppress the next blur-commit when an Escape was just
  // pressed inside the edit input.
  let escSuppressBlur = false
  function onEditKeydown(e) {
    if (e.key === 'Escape') escSuppressBlur = true
  }
  function onEditBlur() {
    if (escSuppressBlur) {
      escSuppressBlur = false
      return
    }
    void grid?.commitEdit()
  }

  const id = $derived(routeState.params.id)
  const schema = $derived(routeState.params.schema)
  const table = $derived(routeState.params.table)

  let grid = $state(/** @type {ReturnType<typeof createRowGrid>|null} */ (null))

  // Re-create the grid composable whenever the (conn, schema, table) tuple
  // changes; this gives each tab its own state.
  // perf-9 (PR #12.5): the previous effect created a new grid on every
  // dependency change without disposing of the previous one — leaving its
  // AbortController + pending Map + any in-flight layout-save timer
  // dangling. We now return a cleanup that calls grid.dispose() so the
  // old grid's resources are released before the next one mounts.
  $effect(() => {
    if (!id || !schema || !table) return
    const g = createRowGrid({ connId: id, schema, table })
    grid = g
    g.reload()
    return () => {
      try { g.dispose() } catch { /* defensive — never let cleanup throw */ }
      if (grid === g) grid = null
    }
  })

  // Make sure a tab exists for this table.
  onMount(() => {
    if (id && schema && table) {
      openTab({ title: `${schema}.${table}`, path: `/connections/${id}/schemas/${schema}/tables/${table}/rows`, icon: 'table' })
    }
  })

  // ─────────────────────────────────────────────────────────────────────
  // Scroll virtualization
  // ─────────────────────────────────────────────────────────────────────
  /** @type {HTMLDivElement|undefined} */
  let bodyEl = $state(undefined)
  let scrollTop = $state(0)
  let viewportH = $state(400)

  // perf-2 (PR #12.5): the previous onScroll handler called
  // requestAnimationFrame on every scroll event. On a touch-pad flick
  // the browser fires 60+ scroll events per second AND each one queued
  // a fresh rAF callback — the scheduler then ran every one of them
  // before the next frame, redundantly assigning scrollTop and
  // triggering the visibleSlice $derived recompute N times per frame
  // instead of once. Adding a `rafPending` latch coalesces all
  // intra-frame scroll events into a single rAF callback (true
  // requestAnimationFrame throttling).
  let rafPending = false
  function onScroll(e) {
    const el = /** @type {HTMLDivElement} */(e.currentTarget)
    if (rafPending) return
    rafPending = true
    requestAnimationFrame(() => {
      rafPending = false
      scrollTop = el.scrollTop
    })
  }

  $effect(() => {
    if (!bodyEl) return
    const ro = new ResizeObserver(() => { viewportH = bodyEl.clientHeight })
    ro.observe(bodyEl)
    viewportH = bodyEl.clientHeight
    return () => ro.disconnect()
  })

  const rowH = $derived(grid?.densityPx ?? 24)
  // perf-3 (PR #12.5): buffer raised to virtualWindow's new default of 8.
  // Explicit default override removed so the constant lives in one place.
  const window_ = $derived(grid ? virtualWindow({
    scrollTop, viewportH, rowH, total: grid.rows.data.length,
  }) : { startIdx: 0, endIdx: 0, visCount: 0, yOffset: 0 })

  const visibleSlice = $derived(grid ? grid.rows.data.slice(window_.startIdx, window_.endIdx) : [])

  const columnsForRender = $derived(grid ? grid.meta.columns.filter((c) => !grid.view.hiddenCols.has(c.name)) : [])
  const columnKinds = $derived(columnsForRender.map((c) => classifyKind(c.type)))

  const gridTemplateColumns = $derived(
    `40px ${columnsForRender.map((c) => (grid?.view.columnWidths[c.name] ?? 160) + 'px').join(' ')}`
  )

  // ─────────────────────────────────────────────────────────────────────
  // Keyboard
  // ─────────────────────────────────────────────────────────────────────
  function onKeyDown(e) {
    if (!grid) return
    const target = /** @type {HTMLElement} */(e.target)
    const isFilterInput = target?.classList?.contains('rg-filter__input')
    const isEditing = !!grid.selection.editing
    const mode = isFilterInput ? 'filter' : isEditing ? 'edit' : 'cell'
    const action = keyToAction(e, mode)
    if (!action) return
    if (mode === 'cell') {
      // Don't capture printable keys if the focus is on an input that
      // already has its own behaviour (e.g. the page-number input).
      if (action === 'edit.startTyping' && target.tagName === 'INPUT') return
    }
    // a11y-6: commitAndExit must NOT preventDefault — we want the browser
    // to advance focus normally so Tab leaves the grid (no keyboard trap).
    if (action !== 'edit.commitAndExit') e.preventDefault()
    handleAction(action, e)
  }

  /** @param {string} action @param {KeyboardEvent} e */
  function handleAction(action, e) {
    if (!grid) return
    const cols = columnsForRender.length
    switch (action) {
      case 'move.up':    grid.focus(grid.selection.focusRow - 1, grid.selection.focusCol); scrollFocusedIntoView(); return
      case 'move.down':  grid.focus(grid.selection.focusRow + 1, grid.selection.focusCol); scrollFocusedIntoView(); return
      case 'move.left':  grid.focus(grid.selection.focusRow, grid.selection.focusCol - 1); return
      case 'move.right': grid.focus(grid.selection.focusRow, grid.selection.focusCol + 1); return
      case 'move.tab': {
        if (grid.selection.editing) void grid.commitEdit()
        let r = grid.selection.focusRow, c = grid.selection.focusCol + 1
        if (c >= cols) { c = 0; r++ }
        grid.focus(r, c)
        return
      }
      case 'move.tabBack': {
        let r = grid.selection.focusRow, c = grid.selection.focusCol - 1
        if (c < 0) { c = cols - 1; r-- }
        grid.focus(r, c)
        return
      }
      case 'move.home': grid.focus(grid.selection.focusRow, 0); return
      case 'move.end':  grid.focus(grid.selection.focusRow, cols - 1); return
      case 'view.pageDown': grid.focus(grid.selection.focusRow + Math.floor(viewportH / rowH), grid.selection.focusCol); scrollFocusedIntoView(); return
      case 'view.pageUp':   grid.focus(grid.selection.focusRow - Math.floor(viewportH / rowH), grid.selection.focusCol); scrollFocusedIntoView(); return
      case 'page.next':  grid.nextPage(); return
      case 'page.prev':  grid.prevPage(); return
      case 'page.first': grid.gotoPage(1); return
      case 'page.last':  grid.gotoPage(Math.ceil((grid.rows.total ?? 0) / grid.page.limit)); return
      case 'edit.start': grid.startEdit(grid.selection.focusRow, grid.selection.focusCol); return
      case 'edit.commit': void grid.commitEdit(); grid.focus(grid.selection.focusRow + 1, grid.selection.focusCol); return
      case 'edit.commitAndExit':
        // a11y-6: commit the value, then DO NOT preventDefault on the Tab
        // key — let the browser advance focus out of the grid root so the
        // user is never trapped in an edit cell. handleAction is called
        // after preventDefault() in onKeyDown; we re-enable native Tab
        // propagation by resetting the default-prevention here.
        void grid.commitEdit()
        return
      case 'edit.cancel':
        if (grid.selection.editing) grid.cancelEdit()
        else if (grid.selection.newRow) grid.cancelNewRow()
        else grid.clearSelection()
        return
      case 'edit.startTyping': grid.startEdit(grid.selection.focusRow, grid.selection.focusCol, e.key); return
      case 'edit.clear':
        // edit-1-followup-a11y-24: Backspace in cell mode enters edit
        // mode with the value pre-cleared so the user can see their
        // intent and confirm with Enter (or Esc to abort). The actual
        // null/empty-string distinction is owned by parseEditValue +
        // the NULL toggle UI affordance.
        grid.startEdit(grid.selection.focusRow, grid.selection.focusCol, '')
        return
      case 'select.toggle': grid.toggleRowSelected(grid.selection.focusRow); return
      case 'select.all': grid.selectAllOnPage(); return
      case 'select.clear': grid.clearSelection(); return
      case 'row.delete': {
        const rowsSel = grid.selection.selectedRows.size > 0
          ? Array.from(grid.selection.selectedRows)
          : [grid.selection.focusRow]
        if (window.confirm(`Delete ${rowsSel.length} row(s)? This cannot be undone.`)) {
          void grid.deleteRows(rowsSel)
        }
        return
      }
      case 'row.insert': grid.startNewRow(); return
      case 'history.undo': void grid.undo(); return
      case 'history.redo': void grid.redo(); return
      case 'view.refresh': grid.refresh(); return
      case 'view.findFocus': {
        const inp = /** @type {HTMLInputElement|null} */(document.querySelector('.rg-filter__input'))
        inp?.focus()
        return
      }
    }
  }

  function scrollFocusedIntoView() {
    if (!grid || !bodyEl) return
    const y = grid.selection.focusRow * rowH
    const top = bodyEl.scrollTop
    const bot = top + viewportH
    if (y < top) bodyEl.scrollTop = y
    else if (y + rowH > bot) bodyEl.scrollTop = y + rowH - viewportH
  }

  function onCellDblClick(rowIdx, colIdx) {
    if (!grid) return
    grid.focus(rowIdx, colIdx)
    grid.startEdit(rowIdx, colIdx)
  }

  function onHeaderClick(e, colName) {
    if (!grid) return
    grid.toggleSort(colName, e.shiftKey)
  }

  // Resize a column via pointer events.
  function onResizeStart(e, colName) {
    if (!grid) return
    e.preventDefault()
    const startX = e.clientX
    const startW = grid.view.columnWidths[colName] ?? 160
    function move(ev) {
      const w = Math.max(40, startW + (ev.clientX - startX))
      grid?.setColumnWidth(colName, w)
    }
    function up() {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up)
  }

  // a11y-4: keyboard-driven column resize. When the resize handle has
  // focus, Left/Right adjust the width by 8 px; Cmd/Ctrl+Left/Right by
  // 32 px; Home resets to the default; Enter auto-fits to a sane width
  // (we approximate auto-fit with a fixed 240 px since measuring text is
  // expensive at render time). Persisted via the same layoutPersist path
  // as pointer drags.
  function onResizeKey(e, colName) {
    if (!grid) return
    const cur = grid.view.columnWidths[colName] ?? 160
    const step = (e.metaKey || e.ctrlKey) ? 32 : 8
    let next = cur
    switch (e.key) {
      case 'ArrowLeft':  next = Math.max(40, cur - step); break
      case 'ArrowRight': next = cur + step; break
      case 'Home':       next = 160; break
      case 'Enter':      next = 240; break
      default: return
    }
    e.preventDefault()
    e.stopPropagation()
    grid.setColumnWidth(colName, next)
  }

  function onFilterInput(e, colName, kind) {
    grid?.setFilter(colName, e.currentTarget.value, kind)
  }

  function sortBadge(colName) {
    if (!grid) return null
    const k = grid.view.sortKeys.find((s) => s.col === colName)
    if (!k) return null
    const idx = grid.view.sortKeys.findIndex((s) => s.col === colName)
    return { dir: k.dir, index: grid.view.sortKeys.length > 1 ? idx + 1 : null }
  }

  // Export modal state + menu state (PR #16).
  let exportMenuOpen = $state(false)
  let exportOpen = $state(false)
  /** @type {'csv'|'ndjson'|'sql'} */
  let exportFormat = $state('csv')
  /** @type {HTMLButtonElement|undefined} */
  let exportBtn = $state(undefined)

  function openExportModal(fmt) {
    exportFormat = fmt
    exportMenuOpen = false
    // ux-8 (PR #16.5): lazy-load the modal component on first open.
    // The render block ({#if ExportModalComponent && exportOpen})
    // mounts the dialog as soon as the import resolves.
    ensureExportModal().then(() => { exportOpen = true })
  }

  // Build the filter / sort payload the export endpoint expects from
  // the grid's current view state. We translate the grid's filter Map
  // (col → {raw,op,value,kind,ok}) into the wire shape — only
  // well-formed filters with a non-empty value are included.
  const exportFilterPayload = $derived.by(() => {
    if (!grid) return []
    const out = []
    for (const [col, f] of grid.view.filters) {
      if (!f || !f.ok || f.value == null || f.value === '') continue
      out.push({ column: col, op: f.op || '=', value: f.value })
    }
    return out
  })
  const exportSortPayload = $derived.by(() => {
    if (!grid) return []
    return grid.view.sortKeys.map((s) => ({ column: s.col, descending: s.dir === 'desc' }))
  })
  const exportColumns = $derived.by(() => {
    if (!grid) return []
    return grid.meta.columns.map((c) => c.name)
  })
</script>

<!-- FIX (PR #12.5 a11y-7/8/9 routed to PR #11.5): expose
     aria-multiselectable so AT users know the grid is multi-select,
     aria-readonly so the canonical READ-ONLY semantic is announced
     (separate from the visible pill), and aria-busy during loads. -->
<!-- a11y-20 (PR #12.5): aria-rowcount can lie when the server total is
     unknown (loading, or backend didn't return it). The ARIA spec defines
     aria-rowcount=-1 as "total is unknown" — AT announces "row X of
     many" instead of the misleading "row X of <current-page-count>+2".
     a11y-5: only ONE tab stop in the grid — the focused cell carries
     tabindex=0; the grid root drops to tabindex=-1 so keyboard users
     don't land on a non-interactive container before reaching a cell.
     onkeydown stays on the root because key events bubble from the
     focused cell. -->
<div
  class="rowgrid"
  role="grid"
  aria-rowcount={grid?.rows.total != null
    ? grid.rows.total + 2
    : (grid?.rows.data.length ? grid.rows.data.length + 2 : -1)}
  aria-colcount={(columnsForRender.length || 0) + 1}
  aria-multiselectable="true"
  aria-readonly={grid?.meta.readOnly ? 'true' : 'false'}
  aria-busy={grid?.rows.loading ? 'true' : undefined}
  data-readonly={grid?.meta.readOnly ? 'true' : 'false'}
  data-density={grid?.density.mode}
  tabindex="-1"
  style="--rg-grid-cols: {gridTemplateColumns}"
  onkeydown={onKeyDown}
>
  {#if grid}
    <!-- Toolbar -->
    <div class="rowgrid__toolbar">
      <span class="rg-toolbar__title">
        <strong>{schema}.{table}</strong>
        {#if grid.meta.readOnly}
          <span class="pill pill--warning u-ml-2">READ-ONLY · no PK</span>
        {/if}
        {#if grid.pendingState.count > 0}
          <span class="pill pill--info u-ml-2">{grid.pendingState.count} pending</span>
        {/if}
      </span>
      <div class="rg-toolbar__spacer"></div>
      <button class="btn btn--sm" onclick={() => grid.refresh()} disabled={grid.rows.loading} title="Refresh (Ctrl+R)">
        Refresh
      </button>
      <button class="btn btn--sm" onclick={() => grid.startNewRow()} disabled={grid.meta.readOnly} title="Insert row (Ctrl+N)">
        + Row
      </button>
      <button
        class="btn btn--sm btn--danger"
        onclick={() => {
          const sel = Array.from(grid.selection.selectedRows)
          if (sel.length === 0) return
          if (window.confirm(`Delete ${sel.length} row(s)? This cannot be undone.`)) void grid.deleteRows(sel)
        }}
        disabled={grid.meta.readOnly || grid.selection.selectedRows.size === 0}
        title="Delete selected (Del)"
      >Delete</button>
      <select
        class="select rg-toolbar__density"
        value={grid.density.mode}
        onchange={(e) => grid.setDensity(/** @type {any} */(e.currentTarget.value))}
        aria-label="density"
      >
        <option value="compact">Compact</option>
        <option value="cozy">Cozy</option>
        <option value="comfortable">Comfortable</option>
      </select>
      {#if grid.view.filters.size > 0}
        <button class="btn btn--ghost btn--sm" onclick={() => grid.clearAllFilters()}>Clear filters</button>
      {/if}
      <div class="rg-toolbar__export rg-toolbar__export--pos">
        <button
          bind:this={exportBtn}
          class="btn btn--sm export-trigger"
          aria-haspopup="menu"
          aria-expanded={exportMenuOpen}
          aria-controls="export-menu"
          onclick={() => { exportMenuOpen = !exportMenuOpen; if (exportMenuOpen) void ensureExportModal() }}
          title="Export"
        >
          Export
          <!-- DC-3 (PR #16.5): shared chevron SVG instead of the
               Unicode ▾ glyph (inconsistent baseline across OS fonts). -->
          <svg width="10" height="10" viewBox="0 0 12 12" aria-hidden="true" class="export-trigger__caret">
            <path d={icons.chevron} fill="currentColor" />
          </svg>
        </button>
        {#if exportMenuOpen}
          <ul
            id="export-menu"
            class="export-menu"
            role="menu"
            onkeydown={(e) => {
              if (e.key === 'Escape') { exportMenuOpen = false; exportBtn?.focus() }
            }}
          >
            <li role="none">
              <button role="menuitem" type="button" class="export-menu__item" onclick={() => openExportModal('csv')}>Download CSV</button>
            </li>
            <li role="none">
              <button role="menuitem" type="button" class="export-menu__item" onclick={() => openExportModal('ndjson')}>Download NDJSON</button>
            </li>
            <li role="none">
              <button role="menuitem" type="button" class="export-menu__item" onclick={() => openExportModal('sql')}>Download SQL</button>
            </li>
          </ul>
        {/if}
      </div>
    </div>

    <!-- Body — single scroller. Header + filter sit inside as sticky-top. -->
    <div
      class="rowgrid__body"
      role="presentation"
      bind:this={bodyEl}
      onscroll={onScroll}
    >
      <!-- Header. perf-6 (PR #12.5): the grid-template-columns string was
           previously inlined on every header / filter / data row, costing
           a fresh per-row style attr write whenever any column width
           changed (mid-drag). The columns string is now lifted to a CSS
           custom property `--rg-grid-cols` on the .rowgrid root; every
           row reads `grid-template-columns: var(--rg-grid-cols)` from a
           single stylesheet rule. The browser only invalidates one style
           recompute per drag tick instead of N. -->
      <div class="rowgrid__head" role="row" aria-rowindex={1}>
        <div class="rg-th rg-th--gutter" role="columnheader" aria-colindex={1}></div>
        {#each columnsForRender as col, ci (col.name)}
          {@const badge = sortBadge(col.name)}
          <div
            class="rg-th {col.primaryKey ? 'rg-th--pk' : ''}"
            role="columnheader"
            aria-colindex={ci + 2}
            aria-sort={badge ? (badge.dir === 'asc' ? 'ascending' : 'descending') : 'none'}
            tabindex="-1"
            onclick={(e) => onHeaderClick(e, col.name)}
            onkeydown={(e) => {
              // a11y-16: Enter / Space sort; Shift+Enter mirrors Shift+Click
              // for multi-sort (append-keep-direction-cycle).
              if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault()
                onHeaderClick(/** @type {any} */(e), col.name)
              }
            }}
            title="{col.type}{col.nullable ? '' : ' · NOT NULL'}"
          >
            {#if col.primaryKey}<span class="rg-th__pkglyph" aria-hidden="true">⌖</span>{/if}
            <span class="rg-th__name">{col.name}</span>
            {#if !col.nullable}<span class="rg-th__nullable" aria-hidden="true">·</span>{/if}
            {#if badge}
              <span class="rg-th__sortbadge">{badge.dir === 'asc' ? '▲' : '▼'}{#if badge.index}<sup>{badge.index}</sup>{/if}</span>
            {/if}
            <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
            <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
            <span
              class="rg-th__resize"
              role="separator"
              aria-orientation="vertical"
              aria-label="Resize {col.name}"
              aria-valuenow={grid.view.columnWidths[col.name] ?? 160}
              aria-valuemin={40}
              tabindex="0"
              onpointerdown={(e) => onResizeStart(e, col.name)}
              onkeydown={(e) => onResizeKey(e, col.name)}
              onclick={(e) => e.stopPropagation()}
            ></span>
          </div>
        {/each}
      </div>

      <!-- Filter bar — a11y-2: the filter bar is presentational, not a grid
           data row. Inputs carry their own per-column aria-labels; we do
           not nest role=search inside role=grid (invalid) nor advertise
           role=row on a non-data row. -->
      <div
        class="rowgrid__filterbar"
        role="presentation"
        aria-label="Filter row"
      >
        <div class="rg-filter rg-filter--gutter" role="presentation"></div>
        {#each columnsForRender as col, ci (col.name)}
          {@const f = grid.view.filters.get(col.name)}
          {@const kind = columnKinds[ci]}
          <div class="rg-filter" role="presentation">
            <input
              type="text"
              class="rg-filter__input {f && !f.ok ? 'rg-filter__input--err' : ''}"
              placeholder="filter…"
              value={f?.raw ?? ''}
              aria-label="Filter {col.name}"
              oninput={(e) => onFilterInput(e, col.name, kind)}
            />
          </div>
        {/each}
      </div>

      <!-- Virtualized rows. Spacer height = total rows * rowH. -->
      <div
        class="rowgrid__viewport"
        style="height: {grid.rows.data.length * rowH}px;"
      >
        {#if grid.selection.newRow}
          <div
            class="rg-row rg-row--new"
            role="row"
            aria-rowindex={3}
            style="top: 0; height: {rowH}px"
          >
            <div class="rg-cell rg-cell--gutter" role="rowheader">+</div>
            {#each columnsForRender as col, ci (col.name)}
              <div class="rg-cell rg-cell--editing" role="gridcell">
                <input
                  class="rg-cell__input"
                  type="text"
                  value={grid.selection.newRow.values[col.name] ?? ''}
                  oninput={(e) => grid.setNewRowValue(col.name, e.currentTarget.value)}
                  onkeydown={(e) => {
                    if (e.key === 'Enter') { e.preventDefault(); void grid.commitNewRow() }
                    if (e.key === 'Escape') { e.preventDefault(); grid.cancelNewRow() }
                  }}
                  aria-label={`new ${col.name}`}
                />
              </div>
            {/each}
          </div>
        {/if}

        {#each visibleSlice as row, localIdx (window_.startIdx + localIdx)}
          {@const rowIdx = window_.startIdx + localIdx}
          {@const top = rowIdx * rowH}
          {@const isFocused = rowIdx === grid.selection.focusRow}
          {@const isSelected = grid.selection.selectedRows.has(rowIdx)}
          {@const rowPK = buildPKKey(row, grid.meta.pk, grid.view.columnOrder)}
          <div
            class="rg-row {isFocused ? 'rg-row--focus' : ''} {isSelected ? 'rg-row--selected' : ''}"
            role="row"
            aria-rowindex={ariaRowIndex({ idx: rowIdx, offset: grid.page.offset, hasNewRow: !!grid.selection.newRow })}
            aria-selected={isSelected}
            style="top: {top}px; height: {rowH}px"
          >
            <div
              class="rg-cell rg-cell--gutter"
              role="rowheader"
              tabindex="-1"
              onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); grid.toggleRowSelected(rowIdx) } }}
              onclick={(e) => {
                if (e.shiftKey) {
                  const a = grid.selection.anchorRow
                  const lo = Math.min(a, rowIdx), hi = Math.max(a, rowIdx)
                  const next = new Set()
                  for (let k = lo; k <= hi; k++) next.add(k)
                  grid.selection.selectedRows = next
                } else if (e.metaKey || e.ctrlKey) {
                  grid.toggleRowSelected(rowIdx)
                } else {
                  grid.selection.selectedRows = new Set([rowIdx])
                  grid.selection.anchorRow = rowIdx
                }
              }}
            >{grid.page.offset + rowIdx + 1}</div>
            {#each columnsForRender as col, ci (col.name)}
              {@const isEditing = grid.selection.editing && grid.selection.editing.row === rowIdx && grid.selection.editing.col === ci}
              {@const cell = renderCell(row[ci], { kind: columnKinds[ci], name: col.name, typeName: col.type })}
              {@const cellFocus = isFocused && grid.selection.focusCol === ci}
              {@const cellSaving = rowPK ? grid.isCellSaving(rowPK, col.name) : false}
              <div
                class="{cell.className} {col.primaryKey ? 'rg-cell--pk' : ''} {cellFocus ? 'rg-cell--focus' : ''} {cellSaving ? 'rg-cell--saving' : ''}"
                aria-busy={cellSaving ? 'true' : undefined}
                role="gridcell"
                aria-colindex={ci + 2}
                tabindex={cellFocus ? 0 : -1}
                ondblclick={() => onCellDblClick(rowIdx, ci)}
                onclick={() => grid.focus(rowIdx, ci)}
                onkeydown={(e) => { if (e.key === 'Enter' || e.key === 'F2') { e.preventDefault(); onCellDblClick(rowIdx, ci) } }}
                title={cell.title}
              >
                {#if isEditing && grid.selection.editing}
                  <input
                    class="rg-cell__input"
                    type="text"
                    value={grid.selection.editing.value}
                    oninput={(e) => grid.setEditValue(e.currentTarget.value)}
                    onkeydown={onEditKeydown}
                    onblur={onEditBlur}
                    aria-label={`Edit ${col.name}`}
                    use:focusInput
                  />
                {:else}
                  <span class={cell.isNull ? 'rg-null' : cell.isEmpty ? 'rg-empty' : ''}>{cell.text}</span>
                {/if}
              </div>
            {/each}
          </div>
        {/each}
      </div>
    </div>

    <!-- Footer -->
    <div class="rowgrid__footer">
      <span class="rg-footer__total num">
        {#if grid.rows.total != null}
          {grid.rows.total.toLocaleString()} rows
        {:else}
          {grid.rows.data.length} on page
        {/if}
      </span>
      <span class="rg-footer__spacer"></span>
      <button class="btn btn--sm btn--ghost" disabled={grid.page.offset === 0} onclick={() => grid.prevPage()}>‹</button>
      <span class="num rg-footer__page">
        Page {Math.floor(grid.page.offset / grid.page.limit) + 1}
        {#if grid.rows.total != null}/ {Math.max(1, Math.ceil(grid.rows.total / grid.page.limit))}{/if}
      </span>
      <button
        class="btn btn--sm btn--ghost"
        disabled={(grid.rows.total != null && grid.page.offset + grid.page.limit >= grid.rows.total) || (grid.rows.total == null && grid.rows.data.length < grid.page.limit)}
        onclick={() => grid.nextPage()}
      >›</button>
      <select
        class="select rg-footer__size"
        value={grid.page.limit}
        onchange={(e) => grid.setPageSize(Number(e.currentTarget.value))}
        aria-label="page size"
      >
        {#each [100, 250, 500, 1000] as s}<option value={s}>{s}</option>{/each}
      </select>
      {#if grid.rows.loading}<span class="spinner" aria-label="loading"></span>{/if}
    </div>

    {#if id && schema && table && ExportModalComponent}
      <!-- ux-8 (PR #16.5): only mounted after the dynamic-import of
           ExportModal resolves. The exportOpen state is bound across
           the boundary so the toolbar's open intent flows through. -->
      <ExportModalComponent
        bind:open={exportOpen}
        connId={id}
        schema={schema}
        table={table}
        columns={exportColumns}
        filter={exportFilterPayload}
        sort={exportSortPayload}
        defaultFormat={exportFormat}
      />
    {/if}

    <!-- Toasts — a11y-10: per-toast role + aria-live so errors preempt the
         SR queue (alert/assertive) while info/success stay polite. The
         outer container intentionally has no aria-live to avoid double
         announcement; each toast announces itself. -->
    <div class="rg-toasts">
      {#each toastBus.items as t (t.id)}
        <div class="rg-toast rg-toast--{t.level}" role={t.role} aria-live={t.ariaLive}>
          <span>{t.text}</span>
          <button class="rg-toast__close" onclick={() => dismissToast(t.id)} aria-label="dismiss">×</button>
        </div>
      {/each}
    </div>
  {/if}
</div>

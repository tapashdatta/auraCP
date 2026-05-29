// Central state composable for the row grid. Owns the reactive $state
// trees, the data-fetch effect, optimistic-update bookkeeping, the
// undoStack, and the layout-persistence sync. Returns a single `grid`
// object the TableScreen + components consume by reference.
//
// Designed so most action handlers are pure-data mutations — networking is
// gated through async helpers that update `pending` and roll back on
// rejection.

import { api, AuraDBError, request } from '../api.js'
import { classifyKind, parseEditValue } from './cellRenderers.js'
import { buildPKKey } from './pkKey.js'
import { cycleSort, serializeSort } from './sortCycle.js'
import { parseFilterInput, serializeFilter } from './filterParse.js'
import { loadLayout, saveLayoutDebounced, flushLayoutSave } from './layoutPersist.js'
import { createUndoStack } from './undoStack.js'
import { runPool } from './promisePool.js'
import { pushToast, toastFromError } from './toasts.svelte.js'

const DENSITY_PX = { compact: 24, cozy: 28, comfortable: 32 }

/**
 * @param {{connId:string, schema:string, table:string}} params
 */
export function createRowGrid(params) {
  const initial = loadLayout(params.connId, params.schema, params.table)

  const meta = $state({
    /** @type {Array<{name:string, type:string, nullable:boolean, primaryKey:boolean}>} */
    columns: [],
    /** @type {string[]} */
    pk: [],
    readOnly: true,
    loadedAt: 0,
  })

  const rows = $state({
    /** @type {any[][]} */
    data: [],
    /** @type {number|null} */
    total: null,
    loading: false,
    /** @type {Error|null} */
    error: null,
  })

  const view = $state({
    /** @type {Array<{col:string,dir:'asc'|'desc'}>} */
    sortKeys: initial.sortKeys || [],
    /** @type {Map<string, ReturnType<typeof parseFilterInput>>} */
    filters: new Map(),
    /** @type {Record<string, number>} */
    columnWidths: initial.columnWidths || {},
    /** @type {string[]} */
    columnOrder: initial.columnOrder || [],
    /** @type {Set<string>} */
    hiddenCols: new Set(initial.hiddenCols || []),
    frozenLeftCount: initial.frozenLeftCount || 0,
  })

  const page = $state({
    limit: initial.pageSize || 100,
    offset: 0,
  })

  const density = $state({
    mode: /** @type {'compact'|'cozy'|'comfortable'} */(initial.density || 'compact'),
  })

  const selection = $state({
    focusRow: 0,
    focusCol: 0,
    anchorRow: 0,
    /** @type {Set<number>} */
    selectedRows: new Set(),
    /** @type {null | {row:number, col:number, value:string, originalValue:any}} */
    editing: null,
    /** @type {null | {values: Record<string, any>}} */
    newRow: null,
  })

  const undoStack = createUndoStack()

  /** @type {Map<string,{row:number,col:number,originalValue:any,pkKey:string,colName:string}>} */
  const pending = new Map()
  const pendingState = $state({
    count: 0,
    /**
     * edit-14 (PR #12.5): per-cell saving indicator. Keyed by
     * `${pkKey}:${colName}` so the cell template can light up a tiny
     * spinner / saving badge while an optimistic write is in flight.
     * @type {Set<string>}
     */
    cells: new Set(),
  })
  function bumpPending() {
    pendingState.count = pending.size
    /** @type {Set<string>} */
    const cells = new Set()
    for (const op of pending.values()) {
      if (op.pkKey && op.colName) cells.add(`${op.pkKey}:${op.colName}`)
    }
    pendingState.cells = cells
  }

  /** @type {AbortController | null} */
  let inflight = null
  let reqId = 0

  function buildLayoutSnapshot() {
    return {
      v: 1,
      columnWidths: view.columnWidths,
      columnOrder: view.columnOrder,
      hiddenCols: Array.from(view.hiddenCols),
      frozenLeftCount: view.frozenLeftCount,
      pageSize: page.limit,
      density: density.mode,
      sortKeys: view.sortKeys.slice(),
    }
  }

  /**
   * PR #12.5: layout writes are now debounced (150 ms trailing). Column
   * resize drags fire pointermove ~60 Hz; without debouncing each tick
   * re-serializes the full layout JSON + a sync localStorage write —
   * measurable jank on large layouts. The trailing edge guarantees the
   * final width still lands; teardown (dispose) flushes any pending
   * timer so a tab close doesn't drop the last write.
   */
  function persistLayout() {
    saveLayoutDebounced(params.connId, params.schema, params.table, buildLayoutSnapshot())
  }

  /**
   * Build a URLSearchParams object that includes repeated keys for sort +
   * filter. Returns the encoded query string (without leading `?`).
   *
   * @param {{wantTotal?: boolean}} [opts]
   */
  function buildSearchParams(opts = {}) {
    const sp = new URLSearchParams()
    sp.set('limit', String(page.limit))
    sp.set('offset', String(page.offset))
    for (const s of view.sortKeys) sp.append('sort', (s.dir === 'desc' ? '-' : '') + s.col)
    for (const [col, parsed] of view.filters.entries()) {
      if (parsed && parsed.ok) sp.append('filter', serializeFilter(col, parsed))
    }
    // WIRE-08: ask for the row count only when the client doesn't have one.
    // Backend Count() is per-conn so it's free relative to Read; we still
    // skip it on every page-step to keep navigation snappy.
    if (opts.wantTotal) sp.set('total', '1')
    return sp.toString()
  }

  // edit-6: separate reload-cookie from per-edit cookies. reload() races are
  // resolved by the AbortController above + this dedicated counter so that
  // a stale fetch that resolves after a newer one is dropped silently.
  let reloadReqId = 0

  async function reload() {
    if (inflight) inflight.abort()
    const ac = typeof AbortController !== 'undefined' ? new AbortController() : null
    inflight = ac
    const myId = ++reloadReqId
    // edit-13: any reload invalidates undo entries that referenced the
    // pre-reload row state, and pending optimistic ops are now meaningless.
    undoStack.clear()
    pending.clear()
    bumpPending()
    rows.loading = true
    rows.error = null
    try {
      // Ask for the total only when we don't have one yet — subsequent
      // page-steps reuse the cached number until something invalidates it
      // (filter / sort / refresh).
      const wantTotal = rows.total == null
      const qs = buildSearchParams({ wantTotal })
      // WIRE-14: the rows-list endpoint accepts repeated `sort` and
      // `filter` query keys (e.g. `?sort=-id&sort=created_at&filter=…`).
      // URLSearchParams(Record<string,string>) deduplicates repeat keys,
      // so we hand-build the encoded query string via buildSearchParams
      // (which uses .append) and call request() directly through
      // listRowsRaw(). The previous AuraDBClient.listRows wrapper was
      // dead code and was removed in PR #12.5.
      const data = await listRowsRaw(params.connId, params.schema, params.table, qs, ac?.signal)
      if (myId !== reloadReqId) return
      // WIRE-04: the canonical wire field names are camelCase
      // (databaseTypeName, primaryKey) per pkg/dbadmin/httpapi/dto.go's
      // columnInfoDTO. Read the canonical names directly — no defensive
      // both-Pascal-and-lowercase fallback, since divergence between client
      // expectations and server output is a contract bug we want loud.
      const cols = (data?.columns || []).map(/** @param {any} c */(c) => ({
        name: String(c.name ?? ''),
        type: String(c.databaseTypeName ?? ''),
        nullable: !!c.nullable,
        primaryKey: !!c.primaryKey,
      }))
      meta.columns = cols
      meta.pk = cols.filter((c) => c.primaryKey).map((c) => c.name)
      meta.readOnly = meta.pk.length === 0
      meta.loadedAt = Date.now()
      // Sync columnOrder default if not set
      if (!view.columnOrder.length || view.columnOrder.some((c) => !cols.find((cc) => cc.name === c))) {
        view.columnOrder = cols.map((c) => c.name)
      }
      // Default column widths
      for (const c of cols) {
        if (!view.columnWidths[c.name]) view.columnWidths[c.name] = 160
      }
      rows.data = Array.isArray(data?.rows) ? data.rows : []
      if (typeof data?.total === 'number') rows.total = data.total
      // Clamp selection
      if (selection.focusRow >= rows.data.length) selection.focusRow = Math.max(0, rows.data.length - 1)
      if (selection.focusCol >= cols.length) selection.focusCol = Math.max(0, cols.length - 1)
    } catch (e) {
      if ((/** @type {any} */(e))?.name === 'AbortError') return
      rows.error = /** @type {any} */(e)
      const t = toastFromError(e)
      pushToast(t.level, t.text)
    } finally {
      if (myId === reloadReqId) rows.loading = false
    }
  }

  /**
   * Force-refresh: invalidate the cached total so the next reload asks the
   * backend again, then call reload. Bound to Refresh + filter / sort
   * actions where the count may have changed.
   */
  function refresh() {
    rows.total = null
    return reload()
  }

  // ─────────────────────────────────────────────────────────────────────
  // Sort / filter / paging actions
  // ─────────────────────────────────────────────────────────────────────

  /**
   * @param {string} col
   * @param {boolean} [append]
   */
  function toggleSort(col, append = false) {
    view.sortKeys = cycleSort(view.sortKeys, col, append)
    page.offset = 0
    persistLayout()
    // Sort doesn't change the count, so reload() is enough. WIRE-08.
    reload()
  }

  /**
   * @param {string} col
   * @param {string} raw
   * @param {import('./cellRenderers.js').CellKind} [kind]
   */
  function setFilter(col, raw, kind) {
    const parsed = parseFilterInput(raw, kind)
    if (parsed === null) {
      view.filters.delete(col)
    } else {
      view.filters.set(col, parsed)
    }
    view.filters = new Map(view.filters) // trigger reactivity
    page.offset = 0
    // Filter changes the resulting row count — refresh forces a Count.
    refresh()
  }

  function clearAllFilters() {
    view.filters = new Map()
    page.offset = 0
    refresh()
  }

  function nextPage() {
    if (rows.total != null && page.offset + page.limit >= rows.total) return
    page.offset = page.offset + page.limit
    reload()
  }
  function prevPage() {
    page.offset = Math.max(0, page.offset - page.limit)
    reload()
  }
  /** @param {number} p */
  function gotoPage(p) {
    const max = rows.total != null ? Math.max(1, Math.ceil(rows.total / page.limit)) : Infinity
    const clamped = Math.max(1, Math.min(max, p | 0))
    page.offset = (clamped - 1) * page.limit
    reload()
  }
  /** @param {number} size */
  function setPageSize(size) {
    page.limit = size
    page.offset = 0
    persistLayout()
    reload()
  }

  /** @param {'compact'|'cozy'|'comfortable'} mode */
  function setDensity(mode) {
    density.mode = mode
    persistLayout()
  }

  function cycleDensity() {
    const order = /** @type {const} */(['compact', 'cozy', 'comfortable'])
    const idx = order.indexOf(density.mode)
    setDensity(order[(idx + 1) % order.length])
  }

  /**
   * @param {string} col @param {number} w
   */
  function setColumnWidth(col, w) {
    view.columnWidths = { ...view.columnWidths, [col]: Math.max(40, Math.round(w)) }
    persistLayout()
  }

  // ─────────────────────────────────────────────────────────────────────
  // Selection / focus
  // ─────────────────────────────────────────────────────────────────────

  /** @param {number} row @param {number} col */
  function focus(row, col) {
    selection.focusRow = Math.max(0, Math.min(rows.data.length - 1, row))
    selection.focusCol = Math.max(0, Math.min(meta.columns.length - 1, col))
  }

  /** @param {number} row */
  function toggleRowSelected(row) {
    const next = new Set(selection.selectedRows)
    if (next.has(row)) next.delete(row); else next.add(row)
    selection.selectedRows = next
    selection.anchorRow = row
  }

  function selectAllOnPage() {
    selection.selectedRows = new Set(rows.data.map((_, i) => i))
  }

  function clearSelection() {
    selection.selectedRows = new Set()
  }

  // ─────────────────────────────────────────────────────────────────────
  // Inline edit lifecycle
  // ─────────────────────────────────────────────────────────────────────

  function isPKCol(colIdx) {
    const c = meta.columns[colIdx]
    return !!c && c.primaryKey
  }

  /** @param {number} row @param {number} col @param {string} [seedValue] */
  function startEdit(row, col, seedValue) {
    if (meta.readOnly) {
      pushToast('warning', 'Read-only: table has no primary key')
      return false
    }
    if (isPKCol(col)) {
      pushToast('warning', 'PK columns are not editable')
      return false
    }
    const c = meta.columns[col]
    if (!c) return false
    const kind = classifyKind(c.type)
    if (kind === 'binary') {
      pushToast('info', 'Binary cells are not editable inline')
      return false
    }
    const originalValue = rows.data[row]?.[col] ?? null
    selection.editing = {
      row, col,
      value: seedValue !== undefined ? seedValue : (originalValue === null || originalValue === undefined ? '' : String(originalValue)),
      originalValue,
    }
    return true
  }

  function cancelEdit() { selection.editing = null }

  /** @param {string} value */
  function setEditValue(value) {
    if (selection.editing) selection.editing.value = value
  }

  /**
   * Find a row's current index by its PK string. Returns -1 when the row
   * is no longer in the current view (e.g. paged out, filtered out, or
   * removed). edit-2 / edit-7: rollback + undo identify rows by PK rather
   * than rowIdx so reorder / pagination doesn't corrupt unrelated rows.
   *
   * @param {string} pkKey
   * @returns {number}
   */
  function findRowByPK(pkKey) {
    if (!pkKey) return -1
    for (let i = 0; i < rows.data.length; i++) {
      if (buildPKKey(rows.data[i], meta.pk, view.columnOrder) === pkKey) return i
    }
    return -1
  }

  /**
   * Commit current edit. Optimistically mutates rows.data; on backend error
   * rolls back (by PK lookup) and surfaces toast.
   *
   * edit-1: sends a `where` snapshot of the row's pre-edit non-PK column
   *         values so the backend can detect concurrent modification.
   * edit-4: no-op detection runs AFTER parseEditValue coerces the input,
   *         so "42"==42 doesn't fire a needless PATCH.
   * edit-7: rollback identifies the row by PK; if it's no longer visible
   *         we surface a 'row no longer in view' toast instead of writing
   *         the rollback into a different row at the same index.
   */
  async function commitEdit() {
    const e = selection.editing
    if (!e) return
    const col = meta.columns[e.col]
    if (!col) { cancelEdit(); return }
    const kind = classifyKind(col.type)
    const r = parseEditValue(e.value, kind, { nullable: col.nullable })
    if (!r.ok) {
      pushToast('warning', r.error)
      return
    }
    const newValue = r.value
    const rowIdx = e.row
    const colIdx = e.col
    const before = e.originalValue
    const targetRow = rows.data[rowIdx]
    if (!targetRow) { selection.editing = null; return }
    const pkKey = buildPKKey(targetRow, meta.pk, view.columnOrder)
    // edit-1: snapshot the row's non-PK + non-target column values so the
    // backend can refuse to clobber a concurrent edit. PK columns are
    // already pinned via the URL; the column being patched is excluded
    // because we are intentionally overwriting it.
    /** @type {Record<string, any>} */
    const whereSnap = {}
    for (let i = 0; i < view.columnOrder.length; i++) {
      const cName = view.columnOrder[i]
      if (cName === col.name) continue
      if (meta.pk.includes(cName)) continue
      whereSnap[cName] = targetRow[i]
    }
    selection.editing = null

    // edit-4: comparison happens on the coerced value, with Object.is to
    // distinguish null vs "" vs 0.
    if (Object.is(newValue, before)) return

    // Optimistic write.
    // perf-4 (PR #12.5): the previous implementation called
    // `rows.data = rows.data.slice()` after every cell edit to nudge
    // reactivity — that is an O(n) copy per commit. Svelte 5's deep
    // reactivity tracks index-assignment on $state arrays directly, so
    // we replace just the touched row reference and let Svelte propagate.
    // We DO build the new row via .slice()+set to keep row references
    // immutable (other code paths snapshot rows.data[i] expecting it not
    // to mutate under them) — that's O(cols), not O(rows*cols).
    const oldRow = rows.data[rowIdx]
    const newRow = oldRow.slice()
    newRow[colIdx] = newValue
    rows.data[rowIdx] = newRow
    const opId = `u:${pkKey}:${col.name}:${Date.now()}:${Math.random()}`
    pending.set(opId, { row: rowIdx, col: colIdx, originalValue: before, pkKey, colName: col.name })
    bumpPending()
    try {
      const result = await api.updateRow(
        params.connId, params.schema, params.table, pkKey,
        { [col.name]: newValue },
        { where: whereSnap },
      )
      // edit-2 / edit-3: undo entries identify the row by PK, not rowIdx,
      // so reorder / pagination doesn't corrupt the wrong row.
      undoStack.push({ kind: 'update', pkKey, colName: col.name, before, after: newValue })
      // WIRE-13 (PR #12.5): the server may have coerced the value (e.g.
      // MySQL silently truncates an over-long VARCHAR, Postgres trims
      // trailing space on CHAR(n), all engines normalize DATETIME format).
      // If the response carries a `row` payload, swap it in by PK so the
      // user sees what was actually persisted. Backwards-compatible:
      // when the server doesn't return a row, behavior matches the
      // pre-WIRE-13 optimistic path.
      if (result && Array.isArray(result.row)) {
        const idxNow = findRowByPK(pkKey)
        if (idxNow >= 0) rows.data[idxNow] = result.row
      } else if (result && result.coerced) {
        // Server signalled a coercion happened but didn't include the
        // row — schedule a quiet background reload so the displayed
        // value reconverges without disrupting the user's focus.
        void reload()
      }
    } catch (err) {
      // edit-7: locate the row by PK at rollback time. If it's no longer
      // in the view (paged out, filtered out, reload happened), surface
      // a toast instead of writing into the wrong row.
      const currentIdx = findRowByPK(pkKey)
      if (currentIdx >= 0) {
        const r2 = rows.data[currentIdx].slice()
        r2[colIdx] = before
        rows.data[currentIdx] = r2
      } else {
        pushToast('info', 'Row no longer visible; refresh to see the saved state')
      }
      const t = toastFromError(err)
      pushToast(t.level, t.text)
      const code = String(/** @type {any} */(err)?.code || '').toLowerCase().replace(/_/g, '-')
      if (code === 'no-primary-key') meta.readOnly = true
    } finally {
      pending.delete(opId)
      bumpPending()
    }
  }

  // ─────────────────────────────────────────────────────────────────────
  // Insert / delete
  // ─────────────────────────────────────────────────────────────────────

  function startNewRow() {
    if (meta.readOnly) {
      pushToast('warning', 'Read-only: table has no primary key')
      return
    }
    /** @type {Record<string,any>} */
    const values = {}
    for (const c of meta.columns) {
      const kind = classifyKind(c.type)
      values[c.name] = c.nullable ? null : (kind === 'number' ? 0 : kind === 'boolean' ? false : '')
    }
    selection.newRow = { values }
  }

  /** @param {string} col @param {any} v */
  function setNewRowValue(col, v) {
    if (!selection.newRow) return
    selection.newRow.values = { ...selection.newRow.values, [col]: v }
  }

  function cancelNewRow() { selection.newRow = null }

  async function commitNewRow() {
    if (!selection.newRow || meta.readOnly) return
    // edit-8: pipe every user-typed field through parseEditValue so
    // strings get coerced to the right primitive (number / boolean /
    // JSON / null) before they hit the backend. Without this, a column
    // typed as INT would receive "42" rather than 42, and a "null" text
    // value would survive as the literal string.
    /** @type {Record<string, any>} */
    const payload = {}
    for (const c of meta.columns) {
      const raw = selection.newRow.values[c.name]
      if (raw === null) { payload[c.name] = null; continue }
      // If the form gave us a non-string (e.g. a default boolean / number
      // from startNewRow), pass it through; parseEditValue is for strings.
      if (typeof raw !== 'string') { payload[c.name] = raw; continue }
      const kind = classifyKind(c.type)
      const r = parseEditValue(raw, kind, { nullable: c.nullable })
      if (!r.ok) {
        pushToast('warning', `${c.name}: ${r.error}`)
        return
      }
      payload[c.name] = r.value
    }
    const draft = { ...selection.newRow.values }
    selection.newRow = null
    try {
      const res = await api.insertRow(params.connId, params.schema, params.table, payload)
      // edit-3: insert is undoable. Capture the PK from the inserted
      // payload — for auto-generated PKs (LastInsertID) we synthesize a
      // single-column key when the table's declared PK has length 1.
      let undoPK = buildPKKey(meta.columns.map((c) => payload[c.name]), meta.pk, view.columnOrder)
      if (!undoPK && res && typeof res.lastInsertId === 'number' && meta.pk.length === 1) {
        const enc = encodeURIComponent
        undoPK = `${enc(meta.pk[0])}=${enc(String(res.lastInsertId))}`
      }
      if (undoPK) {
        // Snapshot the row in current columnOrder for redo-insert.
        const rowSnap = view.columnOrder.map((c) => payload[c])
        undoStack.push({ kind: 'insert', pkKey: undoPK, row: rowSnap })
      }
      // Refetch to pick up server defaults / generated PKs.
      await refresh()
      pushToast('success', 'Row inserted')
    } catch (err) {
      const t = toastFromError(err)
      pushToast(t.level, t.text)
      // Restore the entry so user can fix.
      selection.newRow = { values: draft }
    }
  }

  /** @param {number[]} rowIdxs */
  async function deleteRows(rowIdxs) {
    if (meta.readOnly) {
      pushToast('warning', 'Read-only: table has no primary key')
      return
    }
    if (!rowIdxs.length) return
    // edit-10: capture the columnOrder at delete time so undo can map row
    // values back to columns even if the user reorders columns between
    // delete and undo.
    const orderAtDelete = view.columnOrder.slice()
    // Snapshot before splicing so we can rollback.
    const snapshots = rowIdxs
      .map((i) => ({ i, row: rows.data[i], pkKey: rows.data[i] ? buildPKKey(rows.data[i], meta.pk, orderAtDelete) : '' }))
      .filter((s) => !!s.row)
    // Optimistic splice in reverse order
    const next = rows.data.slice()
    const sortedDesc = snapshots.slice().sort((a, b) => b.i - a.i)
    for (const s of sortedDesc) next.splice(s.i, 1)
    rows.data = next
    selection.selectedRows = new Set()

    const results = await runPool(snapshots, async (s) => {
      await api.deleteRow(params.connId, params.schema, params.table, s.pkKey)
      return s
    })
    // edit-9 (PR #12.5): the previous restore path iterated results in
    // pool-completion order and spliced each failure back at its
    // pre-delete index. That index was correct at delete time but every
    // PRIOR restore had since shifted later rows down by one — leaving
    // failures clustered near the wrong end of the list. We now collect
    // failures, sort by ascending original index, and splice them back
    // in order so each restore's target index is still valid (each prior
    // restore at index i can only push rows at index ≥ i down by one,
    // which is exactly what a later restore at a larger index expects).
    /** @type {Array<{snap:typeof snapshots[number], err:any}>} */
    const failures = []
    /** @type {typeof snapshots} */
    const successes = []
    for (let i = 0; i < results.length; i++) {
      const r = results[i]
      const s = snapshots[i]
      if (!r.ok) failures.push({ snap: s, err: r.error })
      else successes.push(s)
    }
    if (failures.length > 0) {
      const out = rows.data.slice()
      failures
        .slice()
        .sort((a, b) => a.snap.i - b.snap.i)
        .forEach((f) => { out.splice(Math.min(f.snap.i, out.length), 0, f.snap.row) })
      rows.data = out
    }
    for (const s of successes) {
      // edit-2 / edit-10: undo entry identifies the row by PK and
      // records the columnOrder snapshot so reconstruction is correct
      // even if the user reorders columns afterwards.
      undoStack.push({
        kind: 'delete',
        pkKey: s.pkKey,
        beforeRow: s.row,
        columnOrder: orderAtDelete.slice(),
      })
    }
    if (failures.length > 0) {
      const t = toastFromError(failures[0].err)
      pushToast(t.level, failures.length === snapshots.length
        ? t.text
        : `${failures.length} of ${snapshots.length} delete(s) failed — ${t.text}`)
    } else {
      pushToast('success', `Deleted ${snapshots.length} row(s)`)
    }
  }

  async function undo() {
    const entry = undoStack.popForUndo()
    if (!entry) {
      pushToast('info', 'Nothing to undo')
      return
    }
    try {
      if (entry.kind === 'update') {
        // edit-2: identify the row by PK at undo time. If still in view,
        // patch optimistically; otherwise just issue the PATCH and rely
        // on the next refresh to surface state.
        // perf-4: drop the outer `rows.data = rows.data.slice()` — row
        // index assignment is reactive on its own in Svelte 5.
        await api.updateRow(params.connId, params.schema, params.table, entry.pkKey, { [entry.colName]: entry.before })
        const idx = findRowByPK(entry.pkKey)
        const colIdx = view.columnOrder.indexOf(entry.colName)
        if (idx >= 0 && colIdx >= 0) {
          const r = rows.data[idx].slice()
          r[colIdx] = entry.before
          rows.data[idx] = r
        }
      } else if (entry.kind === 'delete') {
        // edit-10: reconstruct using the column order captured at delete
        // time, not the current columnOrder which may have been reordered.
        /** @type {Record<string,any>} */
        const obj = {}
        const order = entry.columnOrder || view.columnOrder
        for (let i = 0; i < order.length; i++) obj[order[i]] = entry.beforeRow[i]
        await api.insertRow(params.connId, params.schema, params.table, obj)
        await refresh()
      } else if (entry.kind === 'insert') {
        await api.deleteRow(params.connId, params.schema, params.table, entry.pkKey)
        await refresh()
      }
    } catch (err) {
      undoStack.rewindUndo()
      const t = toastFromError(err)
      pushToast(t.level, t.text)
    }
  }

  async function redo() {
    const entry = undoStack.popForRedo()
    if (!entry) {
      pushToast('info', 'Nothing to redo')
      return
    }
    try {
      if (entry.kind === 'update') {
        await api.updateRow(params.connId, params.schema, params.table, entry.pkKey, { [entry.colName]: entry.after })
        const idx = findRowByPK(entry.pkKey)
        const colIdx = view.columnOrder.indexOf(entry.colName)
        if (idx >= 0 && colIdx >= 0) {
          // perf-4: see commitEdit comment — drop the redundant whole-
          // array slice; Svelte 5 propagates from the index assignment.
          const r = rows.data[idx].slice()
          r[colIdx] = entry.after
          rows.data[idx] = r
        }
      } else if (entry.kind === 'delete') {
        await api.deleteRow(params.connId, params.schema, params.table, entry.pkKey)
        await refresh()
      } else if (entry.kind === 'insert') {
        /** @type {Record<string,any>} */
        const obj = {}
        const order = entry.columnOrder || view.columnOrder
        for (let i = 0; i < order.length; i++) obj[order[i]] = entry.row[i]
        await api.insertRow(params.connId, params.schema, params.table, obj)
        await refresh()
      }
    } catch (err) {
      undoStack.rewindRedo()
      const t = toastFromError(err)
      pushToast(t.level, t.text)
    }
  }

  /**
   * perf-9 (PR #12.5): teardown hook. The previous implementation
   * created a fresh grid on every (conn, schema, table) change without
   * ever releasing the AbortController or flushing the debounced
   * localStorage timer — memory churn on tab-hop and (rarely) lost layout
   * writes on fast tab close. dispose() aborts the inflight fetch,
   * clears the pending map, and flushes any pending layout save so the
   * effect cleanup in TableScreen can run synchronously.
   */
  function dispose() {
    if (inflight) {
      try { inflight.abort() } catch { /* already-aborted is fine */ }
      inflight = null
    }
    pending.clear()
    bumpPending()
    flushLayoutSave(params.connId, params.schema, params.table, buildLayoutSnapshot())
  }

  return {
    params,
    meta, rows, view, page, density, selection, pendingState, undoStack,
    reload, refresh,
    toggleSort, setFilter, clearAllFilters,
    nextPage, prevPage, gotoPage, setPageSize,
    setDensity, cycleDensity,
    setColumnWidth,
    focus, toggleRowSelected, selectAllOnPage, clearSelection,
    startEdit, cancelEdit, setEditValue, commitEdit,
    startNewRow, setNewRowValue, cancelNewRow, commitNewRow,
    deleteRows,
    undo, redo,
    dispose,
    /**
     * edit-14 helper: return true when a (rowIdx, colIdx) cell has an
     * optimistic write in flight. Used by TableScreen to apply the
     * per-cell saving indicator without leaking the pending Map shape.
     * @param {string} pkKey @param {string} colName
     */
    isCellSaving(pkKey, colName) {
      return pendingState.cells.has(`${pkKey}:${colName}`)
    },
    get densityPx() { return DENSITY_PX[density.mode] },
  }
}

// ─────────────────────────────────────────────────────────────────────
// Internal: low-level rows fetch that supports repeated query keys.
// The rows endpoint accepts repeated `sort` and `filter` query keys —
// URLSearchParams(record) deduplicates them, so callers must hand-build
// the query string. listRowsRaw is the single hot path; WIRE-14 removed
// the now-dead AuraDBClient.listRows wrapper that took a record bag.
// ─────────────────────────────────────────────────────────────────────

/**
 * @param {string} id @param {string} s @param {string} t @param {string} rawQs
 * @param {AbortSignal} [signal]
 */
async function listRowsRaw(id, s, t, rawQs, signal) {
  const enc = encodeURIComponent
  return request(`/connections/${enc(id)}/schemas/${enc(s)}/tables/${enc(t)}/rows?${rawQs}`, { signal })
}

export { listRowsRaw, AuraDBError }

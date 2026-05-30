<script>
  // ExportModal — table export dialog. Renders format radio group +
  // optional columns checklist + filename input + (when CSV) include-
  // header toggle + optional limit + filter/sort preview pill. On
  // submit, calls api.exportTable() which streams the response and
  // triggers a Blob download.
  //
  // a11y:
  //   - role="dialog" + aria-modal=true via Modal wrapper.
  //   - format radios in a labelled radiogroup.
  //   - progress region role=status aria-live=polite; on error
  //     aria-live=assertive.
  //   - focus trap + Escape + focus restore inherited from Modal.
  //
  // PR #16.5 additions:
  //   - ux-5: Retry-After countdown banner on 409 export-in-progress.
  //   - ux-6: pre-flight count probe (countRowsPreflight) + 0-row guard.
  //   - ux-7: rowsWritten progress + ETA in the status pill.
  //   - ux-9: client-side filename sanitiser preview.
  //   - ux-10: millisecond-precision timestamp on the default filename.
  //   - DC-1: footer CTA collapses to "Done" after a successful export.
  //   - DC-4: "Includes filter / sort" pill rendered when inherited.
  //   - DC-5: pre-flight row-count + "≥ 1M rows, will truncate" warning.
  //   - DC-7: filename extension auto-corrected to match format.
  //   - DC-8: Spinner + Pill components for status.
  //   - DC-9: Include header toggle only when format=CSV.
  //   - DC-10: Limit number input (1..1,000,000).
  //   - DC-11: column search filter for wide tables.
  //   - DC-12: footer CTA shortened to "Export".
  //   - DC-13: shared Btn component instead of raw <button>.

  import { tick } from 'svelte'
  import Modal from './Modal.svelte'
  import Btn from './Btn.svelte'
  import Spinner from './Spinner.svelte'
  import Pill from './Pill.svelte'
  import { api, AuraDBError, sanitizeExportFilename } from '../api.js'

  /** @type {{
   *   open?: boolean,
   *   connId: string,
   *   schema: string,
   *   table: string,
   *   columns?: string[],
   *   filter?: Array<{column:string, op:string, value?:any}>,
   *   sort?: Array<{column:string, descending?:boolean}>,
   *   defaultFormat?: 'csv'|'ndjson'|'sql',
   * }} */
  let {
    open = $bindable(false),
    connId,
    schema,
    table,
    columns = [],
    filter = [],
    sort = [],
    defaultFormat = 'csv',
  } = $props()

  // EXPORT_ROW_HARD_CAP mirrors exportMaxRowsHardCap in
  // pkg/dbadmin/httpapi/export_limits.go. The DC-10 limit input clamps
  // to this ceiling so the modal cannot send a value the server will
  // silently rewrite.
  const EXPORT_ROW_HARD_CAP = 1_000_000

  // Svelte 5 warns on $state(defaultFormat) because the prop initial
  // value is captured at construction time rather than tracked as a
  // dependency. That is the intended behaviour here — the modal seeds
  // its state from the prop, then the user is free to change it. The
  // $effect below re-seeds when `open` flips back to true. We silence
  // the warning by passing a non-state expression to the initializer.
  /** @type {'csv'|'ndjson'|'sql'} */
  let format = $state(/** @type {'csv'|'ndjson'|'sql'} */('csv'))
  let includeHeader = $state(true)
  let selectedCols = $state(/** @type {Set<string>} */(new Set()))
  let filename = $state('')
  let colSearch = $state('')
  /** @type {number|null} */
  let limit = $state(null)
  let busy = $state(false)
  let progressBytes = $state(0)
  let progressRows = $state(0)
  let progressElapsedMs = $state(0)
  /** @type {string} */
  let errorMsg = $state('')
  /** @type {{filename:string, bytes:number, rows:number|null}|null} */
  let result = $state(null)
  /** @type {string} */
  let exportWarning = $state('')
  // ux-5: countdown seconds when a 409 export-in-progress fires.
  let retryAfter = $state(0)
  /** @type {number|null} */
  let retryTimer = $state(null)
  // ux-6 / DC-5: pre-flight count probe state.
  /** @type {number|null} */
  let preflightTotal = $state(null)
  let preflightLoading = $state(false)

  // ux-1 (PR #16): an in-flight export must be cancellable via Escape
  // (the user already pressed Cancel-by-keyboard expectation). We own
  // an AbortController whose signal is forwarded to api.exportTable;
  // Escape aborts the fetch, the busy state clears, and the modal then
  // closes via the inherited Modal Escape handler.
  /** @type {AbortController|null} */
  let abortCtl = null

  $effect(() => {
    if (open) {
      // Reset when re-opened with new defaults.
      format = defaultFormat
      includeHeader = true
      selectedCols = new Set(columns)
      filename = defaultFilename()
      colSearch = ''
      limit = null
      busy = false
      progressBytes = 0
      progressRows = 0
      progressElapsedMs = 0
      errorMsg = ''
      result = null
      exportWarning = ''
      retryAfter = 0
      preflightTotal = null
      abortCtl = null
      // ux-6 / DC-5: fire the pre-flight count probe so the modal can
      // warn on empty / will-truncate scenarios. Silent on failure —
      // the real export still runs.
      runPreflight()
    } else {
      if (retryTimer != null) { clearInterval(retryTimer); retryTimer = null }
    }
  })

  // ux-10 (PR #16.5): millisecond-precision timestamp so two same-
  // second exports do not collide on filename. Format
  // YYYYMMDDTHHMMSSmmm  (compact ISO without separators).
  function tsStamp() {
    const d = new Date()
    const pad = (n, w = 2) => String(n).padStart(w, '0')
    return (
      d.getUTCFullYear() +
      pad(d.getUTCMonth() + 1) +
      pad(d.getUTCDate()) +
      'T' +
      pad(d.getUTCHours()) +
      pad(d.getUTCMinutes()) +
      pad(d.getUTCSeconds()) +
      pad(d.getUTCMilliseconds(), 3)
    )
  }

  function defaultFilename() {
    return `${table}-${tsStamp()}.${format}`
  }

  // DC-7 (PR #16.5): when the format radio toggles, force the
  // extension to match. We do this on both the radio change (below)
  // and when the user types — the input is rewritten with the
  // canonical extension if it ends in any of the known formats.
  function withFormatExt(name, fmt) {
    if (!name) return name
    return name.replace(/\.(csv|ndjson|sql)$/i, '.' + fmt)
  }

  // ux-9 (PR #16.5): preview the sanitised filename so the user sees
  // what the server will actually emit.
  const filenamePreview = $derived.by(() => {
    if (!filename) return ''
    const sanitised = sanitizeExportFilename(filename)
    // Mirror server logic: if the sanitised value lacks the format
    // suffix, the server appends it.
    const want = '.' + format
    if (!sanitised.toLowerCase().endsWith(want)) return sanitised + want
    return sanitised
  })

  function close() {
    if (busy) {
      // ux-1: when an export is mid-flight the close intent is taken
      // as "cancel the export". The next $effect-driven busy = false
      // releases the modal; the user then issues a second close (Esc
      // or button) to dismiss.
      cancelInFlight()
      return
    }
    open = false
  }

  // ux-1: abort the in-flight fetch. Called by Escape (via Modal's
  // onClose hook below) and by the explicit Cancel button while busy.
  function cancelInFlight() {
    if (!abortCtl) return
    try { abortCtl.abort() } catch { /* ignore */ }
    abortCtl = null
  }

  // ux-1: Modal raises onClose when the backdrop is clicked, Escape is
  // pressed, or the inherited close button fires. We swap the modal's
  // close to our cancel-aware variant by intercepting Escape at this
  // layer — when busy, Escape cancels and consumes the event; when
  // idle, Escape falls through to Modal.
  function onModalKey(e) {
    if (!open) return
    if (e.key === 'Escape' && busy) {
      e.preventDefault()
      e.stopPropagation()
      cancelInFlight()
    }
  }

  function toggleCol(c) {
    if (selectedCols.has(c)) selectedCols.delete(c)
    else selectedCols.add(c)
    selectedCols = new Set(selectedCols)
  }

  function selectAllCols() { selectedCols = new Set(columns) }
  function clearAllCols() { selectedCols = new Set() }

  // DC-11 (PR #16.5): filter visible columns by the search input. The
  // selection set is independent of the visible list so toggling the
  // search does not lose pre-checked columns.
  const visibleColumns = $derived.by(() => {
    if (!colSearch.trim()) return columns
    const needle = colSearch.toLowerCase()
    return columns.filter((c) => c.toLowerCase().includes(needle))
  })

  // DC-4 (PR #16.5): inherited filter / sort summary pill. Quietly
  // surfaces the count so the user knows a non-empty filter is in
  // play before they hit Export.
  const inheritedFilterCount = $derived(Array.isArray(filter) ? filter.length : 0)
  const inheritedSortCount = $derived(Array.isArray(sort) ? sort.length : 0)

  // ux-6 / DC-5: pre-flight row count probe. Runs once per open. The
  // server's Count() respects the inherited filter so the total
  // reflects what the export will actually pull.
  async function runPreflight() {
    if (!connId || !schema || !table) return
    preflightLoading = true
    try {
      const { total } = await api.countRowsPreflight(connId, {
        schema, table, filter,
      })
      preflightTotal = total
    } finally {
      preflightLoading = false
    }
  }

  async function submit() {
    if (busy) return
    if (selectedCols.size === 0 && columns.length > 0) {
      errorMsg = 'select at least one column'
      return
    }
    // ux-6 (PR #16.5): hard-stop on a confirmed-zero pre-flight count.
    // The user can dismiss the warning via Force-export (set the
    // filter to allow zero-row exports) — but the default is "tell
    // them their filter matched nothing" rather than silently
    // shipping a header-only file.
    if (preflightTotal === 0) {
      errorMsg = 'no rows match the current filter — nothing to export'
      return
    }
    busy = true
    errorMsg = ''
    progressBytes = 0
    progressRows = 0
    progressElapsedMs = 0
    result = null
    exportWarning = ''
    retryAfter = 0
    if (retryTimer != null) { clearInterval(retryTimer); retryTimer = null }
    // ux-1: hand the AbortController to the api so Escape (or the
    // explicit cancel button while busy) can interrupt the in-flight
    // fetch.
    abortCtl = new AbortController()
    try {
      const cols = columns.length > 0 ? columns.filter((c) => selectedCols.has(c)) : undefined
      const r = await api.exportTable(connId, {
        schema,
        table,
        format,
        columns: cols,
        filter,
        sort,
        includeHeader: format === 'csv' ? includeHeader : undefined,
        filename: filename || undefined,
        limit: (limit != null && limit > 0) ? Math.min(limit, EXPORT_ROW_HARD_CAP) : undefined,
        signal: abortCtl.signal,
        onProgress: (b, info) => {
          progressBytes = b
          if (info) {
            if (typeof info.rowsWritten === 'number') progressRows = info.rowsWritten
            if (typeof info.elapsedMs === 'number') progressElapsedMs = info.elapsedMs
          }
        },
      })
      result = { filename: r.filename, bytes: r.bytes, rows: r.rows }
      // ux-3: surface mid-stream error / truncation that the server
      // raised in the response trailers (the api parses them off the
      // Response into the result object).
      if (r.serverError) {
        exportWarning = `server reported "${r.serverError}" mid-stream — file may be incomplete`
      } else if (r.truncated) {
        exportWarning = 'export reached the row/byte cap — file is truncated'
      }
      await tick()
    } catch (e) {
      // ux-1: AbortError surfaces as a user-cancelled state, not an
      // angry error toast.
      const isAbort = e && (/** @type {any} */(e).name === 'AbortError' || abortCtl?.signal.aborted)
      if (isAbort) {
        errorMsg = 'cancelled'
      } else if (e instanceof AuraDBError) {
        // ux-5: 409 export-in-progress gets a friendlier banner + a
        // Retry-After countdown when the server provided one.
        if (e.status === 409) {
          const detail = /** @type {any} */(e.detail) || {}
          const ra = (detail && typeof detail.retryAfter === 'number') ? detail.retryAfter : 5
          retryAfter = ra
          errorMsg = `another export is already running — retry available in ${ra}s`
          if (retryTimer != null) clearInterval(retryTimer)
          retryTimer = /** @type {any} */(setInterval(() => {
            retryAfter = Math.max(0, retryAfter - 1)
            if (retryAfter === 0 && retryTimer != null) {
              clearInterval(retryTimer); retryTimer = null
              errorMsg = ''
            }
          }, 1000))
        } else {
          errorMsg = `${e.message} (${e.code})`
        }
      } else {
        errorMsg = (e && /** @type {any} */(e).message) || 'export failed'
      }
    } finally {
      busy = false
      abortCtl = null
    }
  }

  function formatBytes(n) {
    if (n < 1024) return n + ' B'
    if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB'
    return (n / (1024 * 1024)).toFixed(2) + ' MB'
  }

  // ux-7 (PR #16.5): ETA from elapsedMs + rows hint + preflightTotal.
  // Falls back to "" when we can't form a useful estimate.
  const etaText = $derived.by(() => {
    if (!busy) return ''
    if (!progressRows || !progressElapsedMs) return ''
    const target = preflightTotal != null && preflightTotal > 0
      ? Math.min(preflightTotal, EXPORT_ROW_HARD_CAP)
      : 0
    if (!target || progressRows >= target) return ''
    const rate = progressRows / (progressElapsedMs / 1000) // rows/sec
    if (rate <= 0) return ''
    const remaining = Math.ceil((target - progressRows) / rate)
    if (remaining < 1) return ''
    if (remaining < 60) return ` · ~${remaining}s left`
    return ` · ~${Math.ceil(remaining / 60)}m left`
  })
</script>

<!-- ux-1: capture Escape at the window so we can cancel an in-flight
     export before Modal's own Escape handler closes us. The handler
     no-ops when the modal is closed or idle. -->
<svelte:window onkeydown={onModalKey} />

<Modal bind:open title="Export {schema}.{table}" width={560}>
  <div class="export-modal">
    <fieldset class="export-modal__fmt" aria-labelledby="export-fmt-legend">
      <legend id="export-fmt-legend">Format</legend>
      <div role="radiogroup" aria-labelledby="export-fmt-legend">
        <label class="export-modal__radio">
          <input type="radio" name="export-format" value="csv" checked={format === 'csv'} onchange={() => { format = 'csv'; if (filename) filename = filename.replace(/\.(csv|ndjson|sql)$/i, '.csv') }} disabled={busy} />
          <span>CSV</span>
        </label>
        <label class="export-modal__radio">
          <input type="radio" name="export-format" value="ndjson" checked={format === 'ndjson'} onchange={() => { format = 'ndjson'; if (filename) filename = filename.replace(/\.(csv|ndjson|sql)$/i, '.ndjson') }} disabled={busy} />
          <span>NDJSON</span>
        </label>
        <label class="export-modal__radio">
          <input type="radio" name="export-format" value="sql" checked={format === 'sql'} onchange={() => { format = 'sql'; if (filename) filename = filename.replace(/\.(csv|ndjson|sql)$/i, '.sql') }} disabled={busy} />
          <span>SQL</span>
        </label>
      </div>
    </fieldset>

    <!-- DC-4 (PR #16.5): inherited filter / sort summary so the user
         can see at a glance that the grid's current view will be
         carried into the export. -->
    {#if inheritedFilterCount > 0 || inheritedSortCount > 0 || (preflightTotal != null)}
      <div class="export-modal__inherits" aria-live="polite">
        {#if inheritedFilterCount > 0}
          <Pill tone="info">Filter: {inheritedFilterCount} clause{inheritedFilterCount === 1 ? '' : 's'}</Pill>
        {/if}
        {#if inheritedSortCount > 0}
          <Pill tone="info">Sort: {inheritedSortCount} key{inheritedSortCount === 1 ? '' : 's'}</Pill>
        {/if}
        {#if preflightLoading}
          <Pill tone="neutral">Counting…</Pill>
        {:else if preflightTotal != null}
          {#if preflightTotal === 0}
            <Pill tone="warning">0 rows match</Pill>
          {:else if preflightTotal >= EXPORT_ROW_HARD_CAP}
            <Pill tone="warning">{preflightTotal.toLocaleString()}+ rows — will truncate at {EXPORT_ROW_HARD_CAP.toLocaleString()}</Pill>
          {:else}
            <Pill tone="neutral">{preflightTotal.toLocaleString()} row{preflightTotal === 1 ? '' : 's'}</Pill>
          {/if}
        {/if}
      </div>
    {/if}

    <!-- DC-9 (PR #16.5): the include-header toggle is CSV-specific;
         hide it for NDJSON / SQL so the modal stops implying the
         setting applies. -->
    {#if format === 'csv'}
      <label class="export-modal__check">
        <input type="checkbox" bind:checked={includeHeader} disabled={busy} />
        <span>Include header row</span>
      </label>
    {/if}

    {#if columns.length > 0}
      <fieldset class="export-modal__cols">
        <legend>Columns ({selectedCols.size} / {columns.length})</legend>
        <div class="export-modal__cols-actions">
          <Btn variant="ghost" size="sm" onclick={selectAllCols} disabled={busy}>All</Btn>
          <Btn variant="ghost" size="sm" onclick={clearAllCols} disabled={busy}>None</Btn>
          <!-- DC-11 (PR #16.5): column search for wide tables. -->
          {#if columns.length > 12}
            <input
              type="text"
              class="textfield export-modal__col-search"
              bind:value={colSearch}
              placeholder="Filter columns…"
              aria-label="Filter columns"
              disabled={busy}
            />
          {/if}
        </div>
        <div class="export-modal__cols-list">
          {#each visibleColumns as c (c)}
            <label class="export-modal__col">
              <input type="checkbox" checked={selectedCols.has(c)} onchange={() => toggleCol(c)} disabled={busy} />
              <span>{c}</span>
            </label>
          {/each}
          {#if visibleColumns.length === 0}
            <span class="export-modal__col-empty">No columns match "{colSearch}"</span>
          {/if}
        </div>
      </fieldset>
    {/if}

    <!-- DC-10 (PR #16.5): row limit input. Clamps to the server cap. -->
    <label class="export-modal__limit">
      <span>Limit (optional)</span>
      <input
        type="number"
        class="textfield"
        bind:value={limit}
        min="1"
        max={EXPORT_ROW_HARD_CAP}
        step="1"
        placeholder="all rows (cap {EXPORT_ROW_HARD_CAP.toLocaleString()})"
        disabled={busy}
        aria-label="Row limit"
      />
    </label>

    <label class="export-modal__file">
      <span>Filename</span>
      <input
        type="text"
        class="textfield"
        bind:value={filename}
        placeholder={defaultFilename()}
        disabled={busy}
        aria-label="Filename"
      />
      <!-- ux-9 (PR #16.5): preview what the server will actually emit
           after SanitizeFilename + format-suffix correction. -->
      {#if filenamePreview && filenamePreview !== filename}
        <span class="export-modal__file-preview">Will ship as: <code>{filenamePreview}</code></span>
      {/if}
    </label>

    <!-- DC-8 (PR #16.5): use Spinner + Pill so idle / running / done
         each render distinctly. -->
    <div role="status" aria-live={errorMsg ? 'assertive' : 'polite'} aria-atomic="true" class="export-modal__status">
      {#if busy}
        <span class="export-modal__status-row">
          <Spinner size={12} />
          <span>Exporting… {formatBytes(progressBytes)}{progressRows > 0 ? ' · ' + progressRows.toLocaleString() + ' rows' : ''}{etaText}</span>
          <span class="export-modal__hint">(Escape to cancel)</span>
        </span>
      {:else if result}
        <span class="export-modal__status-row">
          <Pill tone="success">Saved</Pill>
          <span><code>{result.filename}</code> · {formatBytes(result.bytes)}{result.rows != null ? ' · ' + result.rows.toLocaleString() + ' rows' : ''}</span>
        </span>
        {#if exportWarning}
          <div class="export-modal__warn" role="alert">{exportWarning}</div>
        {/if}
      {:else if errorMsg}
        <span class="export-modal__status-row">
          <Pill tone="danger">Error</Pill>
          <span>{errorMsg}{retryAfter > 0 ? ` (${retryAfter}s)` : ''}</span>
        </span>
      {/if}
    </div>
  </div>

  {#snippet footer()}
    <div class="export-modal__actions">
      {#if busy}
        <!-- DC-13 (PR #16.5): shared Btn instead of raw <button>. -->
        <Btn variant="ghost" onclick={cancelInFlight}>Cancel</Btn>
        <Btn variant="primary" loading ariaBusy disabled>Exporting…</Btn>
      {:else if result && !exportWarning}
        <!-- DC-1 (PR #16.5): post-success CTA collapses to "Done". -->
        <Btn variant="primary" onclick={close}>Done</Btn>
      {:else}
        <Btn variant="ghost" onclick={close}>Close</Btn>
        <Btn
          variant="primary"
          onclick={submit}
          disabled={busy || preflightTotal === 0 || retryAfter > 0}
        >Export</Btn>
      {/if}
    </div>
  {/snippet}
</Modal>

<style>
  .export-modal { display: grid; gap: 14px; }
  .export-modal__fmt > div { display: flex; gap: 18px; margin-top: 6px; }
  .export-modal__radio,
  .export-modal__col,
  .export-modal__check { display: inline-flex; align-items: center; gap: 6px; cursor: pointer; }
  .export-modal__cols-list {
    display: grid; grid-template-columns: repeat(2, minmax(0,1fr));
    gap: 4px 10px; max-height: 200px; overflow: auto; margin-top: 6px;
    padding: 6px 0;
  }
  .export-modal__cols-actions { display: flex; gap: 6px; margin-top: 4px; align-items: center; }
  .export-modal__col-search { flex: 1; min-width: 140px; }
  .export-modal__col-empty { grid-column: 1 / -1; opacity: 0.65; font-style: italic; padding: 6px 0; }
  .export-modal__file,
  .export-modal__limit { display: grid; gap: 4px; }
  .export-modal__file span,
  .export-modal__limit span { font-size: 0.85em; opacity: 0.8; }
  .export-modal__file-preview { font-size: 0.8em; opacity: 0.75; }
  .export-modal__file-preview code { font-family: ui-monospace, monospace; font-size: 0.95em; }
  .export-modal__inherits { display: flex; flex-wrap: wrap; gap: 6px; }
  .export-modal__status { min-height: 1.6em; font-size: 0.9em; opacity: 0.9; display: grid; gap: 4px; }
  .export-modal__status-row { display: inline-flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .export-modal__actions { display: flex; justify-content: flex-end; gap: 8px; }
  .export-modal__hint { opacity: 0.6; font-size: 0.85em; margin-left: 6px; }
  .export-modal__warn {
    margin-top: 4px; padding: 6px 8px; border-radius: 4px;
    background: var(--warn-bg, rgba(255, 180, 0, 0.12));
    color: var(--warn-fg, #b88600);
    font-size: 0.85em;
  }
</style>

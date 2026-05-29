<script>
  // ExportModal — table export dialog. Renders format radio group +
  // optional columns checklist + filename input + (when CSV) include-
  // header toggle. On submit, calls api.exportTable() which streams the
  // response and triggers a Blob download.
  //
  // a11y:
  //   - role="dialog" + aria-modal=true via Modal wrapper.
  //   - format radios in a labelled radiogroup.
  //   - progress region role=status aria-live=polite; on error
  //     aria-live=assertive.
  //   - focus trap + Escape + focus restore inherited from Modal.

  import { tick } from 'svelte'
  import Modal from './Modal.svelte'
  import { api, AuraDBError } from '../api.js'

  /** @type {{
   *   open?: boolean,
   *   connId: string,
   *   schema: string,
   *   table: string,
   *   columns?: string[],
   *   filter?: Array<{column:string, op:string, value?:any}>,
   *   sort?: Array<{column:string, descending?:boolean}>,
   *   defaultFormat?: 'csv'|'ndjson'|'sql',
   *   onClose?: ()=>void,
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
    onClose,
  } = $props()

  /** @type {'csv'|'ndjson'|'sql'} */
  let format = $state(defaultFormat)
  let includeHeader = $state(true)
  let selectedCols = $state(/** @type {Set<string>} */(new Set(columns)))
  let filename = $state('')
  let busy = $state(false)
  let progressBytes = $state(0)
  /** @type {string} */
  let errorMsg = $state('')
  /** @type {{filename:string, bytes:number}|null} */
  let result = $state(null)
  /** @type {string} */
  let exportWarning = $state('')

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
      busy = false
      progressBytes = 0
      errorMsg = ''
      result = null
      exportWarning = ''
      abortCtl = null
    }
  })

  function defaultFilename() {
    const ts = new Date().toISOString().slice(0, 10).replace(/-/g, '')
    return `${table}-${ts}.${format}`
  }

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
    onClose?.()
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

  async function submit() {
    if (busy) return
    if (selectedCols.size === 0 && columns.length > 0) {
      errorMsg = 'select at least one column'
      return
    }
    busy = true
    errorMsg = ''
    progressBytes = 0
    result = null
    exportWarning = ''
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
        signal: abortCtl.signal,
        onProgress: (b) => { progressBytes = b },
      })
      result = { filename: r.filename, bytes: r.bytes }
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
        errorMsg = `${e.message} (${e.code})`
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
</script>

<!-- ux-1: capture Escape at the window so we can cancel an in-flight
     export before Modal's own Escape handler closes us. The handler
     no-ops when the modal is closed or idle. -->
<svelte:window onkeydown={onModalKey} />

<Modal bind:open title="Export {schema}.{table}" width={520} onClose={onClose}>
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
          <button type="button" class="btn btn--sm btn--ghost" onclick={selectAllCols} disabled={busy}>All</button>
          <button type="button" class="btn btn--sm btn--ghost" onclick={clearAllCols} disabled={busy}>None</button>
        </div>
        <div class="export-modal__cols-list">
          {#each columns as c (c)}
            <label class="export-modal__col">
              <input type="checkbox" checked={selectedCols.has(c)} onchange={() => toggleCol(c)} disabled={busy} />
              <span>{c}</span>
            </label>
          {/each}
        </div>
      </fieldset>
    {/if}

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
    </label>

    <div role="status" aria-live={errorMsg ? 'assertive' : 'polite'} aria-atomic="true" class="export-modal__status">
      {#if busy}
        Exporting… {formatBytes(progressBytes)} <span class="export-modal__hint">(Escape to cancel)</span>
      {:else if result}
        Saved {result.filename} ({formatBytes(result.bytes)})
        {#if exportWarning}
          <div class="export-modal__warn" role="alert">{exportWarning}</div>
        {/if}
      {:else if errorMsg}
        Error: {errorMsg}
      {/if}
    </div>
  </div>

  {#snippet footer()}
    <div class="export-modal__actions">
      {#if busy}
        <button type="button" class="btn btn--ghost" onclick={cancelInFlight}>Cancel</button>
      {:else}
        <button type="button" class="btn btn--ghost" onclick={close}>Close</button>
      {/if}
      <button type="button" class="btn btn--primary" onclick={submit} disabled={busy}>
        {busy ? 'Exporting…' : 'Start export'}
      </button>
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
  .export-modal__cols-actions { display: flex; gap: 6px; margin-top: 4px; }
  .export-modal__file { display: grid; gap: 4px; }
  .export-modal__file span { font-size: 0.85em; opacity: 0.8; }
  .export-modal__status { min-height: 1.2em; font-size: 0.9em; opacity: 0.85; }
  .export-modal__actions { display: flex; justify-content: flex-end; gap: 8px; }
  .export-modal__hint { opacity: 0.6; font-size: 0.85em; margin-left: 6px; }
  .export-modal__warn {
    margin-top: 4px; padding: 6px 8px; border-radius: 4px;
    background: var(--warn-bg, rgba(255, 180, 0, 0.12));
    color: var(--warn-fg, #b88600);
    font-size: 0.85em;
  }
</style>

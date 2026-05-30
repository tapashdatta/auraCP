<script>
  // ImportModal — table import dialog (v0.3.2-E). Mirrors the
  // ExportModal pattern but in the reverse direction: a file picker
  // accepts a CSV / NDJSON upload, the form posts to
  // /connections/{id}/import as multipart/form-data, and the result
  // envelope (rows imported, skipped, per-row errors) is rendered in
  // the status region.
  //
  // a11y:
  //   - role="dialog" + aria-modal=true via Modal wrapper.
  //   - format radios in a labelled radiogroup.
  //   - progress region role=status aria-live=polite; on error
  //     aria-live=assertive.
  //   - focus trap + Escape + focus restore inherited from Modal.

  import { tick } from 'svelte'
  import Modal from './Modal.svelte'
  import Btn from './Btn.svelte'
  import Spinner from './Spinner.svelte'
  import Pill from './Pill.svelte'
  import { api, AuraDBError } from '../api.js'

  /** @type {{
   *   open?: boolean,
   *   connId: string,
   *   schema: string,
   *   table: string,
   *   defaultFormat?: 'csv'|'ndjson',
   *   defaultOnConflict?: 'error'|'skip'|'update',
   *   onImported?: (result: {rowsImported:number, skipped:number, totalErrors:number}) => void,
   * }} */
  let {
    open = $bindable(false),
    connId,
    schema,
    table,
    defaultFormat = 'csv',
    defaultOnConflict = 'error',
    onImported = undefined,
  } = $props()

  // IMPORT_ROW_HARD_CAP mirrors importMaxRowsHardCap in
  // pkg/dbadmin/httpapi/import_limits.go.
  const IMPORT_ROW_HARD_CAP = 100_000
  // IMPORT_BODY_HARD_CAP mirrors importMaxBodyBytes.
  const IMPORT_BODY_HARD_CAP = 64 * 1024 * 1024

  /** @type {'csv'|'ndjson'} */
  let format = $state(/** @type {'csv'|'ndjson'} */('csv'))
  /** @type {'error'|'skip'|'update'} */
  let onConflict = $state(/** @type {'error'|'skip'|'update'} */('error'))
  /** @type {File|null} */
  let file = $state(/** @type {File|null} */(null))
  let busy = $state(false)
  let uploadBytes = $state(0)
  /** @type {string} */
  let errorMsg = $state('')
  /** @type {{
   *   rowsImported:number, skipped:number, totalErrors:number,
   *   errors:Array<{rowIndex:number, code:string, message:string}>,
   *   bytes:number, truncated:boolean, jobId:string,
   * }|null} */
  let result = $state(null)
  // ux-5: Retry-After countdown banner on 409 import-in-progress.
  let retryAfter = $state(0)
  /** @type {number|null} */
  let retryTimer = $state(null)

  /** @type {AbortController|null} */
  let abortCtl = null

  $effect(() => {
    if (open) {
      format = defaultFormat
      onConflict = defaultOnConflict
      file = null
      busy = false
      uploadBytes = 0
      errorMsg = ''
      result = null
      retryAfter = 0
      abortCtl = null
    } else {
      if (retryTimer != null) { clearInterval(retryTimer); retryTimer = null }
    }
  })

  function close() {
    if (busy) {
      cancelInFlight()
      return
    }
    open = false
  }

  function cancelInFlight() {
    if (!abortCtl) return
    try { abortCtl.abort() } catch { /* ignore */ }
    abortCtl = null
  }

  function onModalKey(e) {
    if (!open) return
    if (e.key === 'Escape' && busy) {
      e.preventDefault()
      e.stopPropagation()
      cancelInFlight()
    }
  }

  // Accept attribute for the file input — the user can override via the
  // OS picker but the default narrows to the chosen format.
  const acceptAttr = $derived(format === 'csv' ? '.csv,text/csv' : '.ndjson,application/x-ndjson,application/json,.json')

  function onPickFile(e) {
    const input = /** @type {HTMLInputElement} */ (e.target)
    const f = input.files && input.files[0]
    if (!f) {
      file = null
      return
    }
    if (f.size > IMPORT_BODY_HARD_CAP) {
      errorMsg = `file too large — max ${formatBytes(IMPORT_BODY_HARD_CAP)}`
      file = null
      input.value = ''
      return
    }
    errorMsg = ''
    file = f
    // Auto-detect format from filename extension when the user picks a
    // file whose extension disagrees with the current radio. Better
    // than silently posting an NDJSON body as CSV.
    const name = f.name.toLowerCase()
    if (name.endsWith('.ndjson') || name.endsWith('.jsonl')) {
      format = 'ndjson'
    } else if (name.endsWith('.csv')) {
      format = 'csv'
    }
  }

  async function submit() {
    if (busy) return
    if (!file) {
      errorMsg = 'select a file to import'
      return
    }
    busy = true
    errorMsg = ''
    uploadBytes = 0
    result = null
    retryAfter = 0
    if (retryTimer != null) { clearInterval(retryTimer); retryTimer = null }
    abortCtl = new AbortController()
    try {
      const r = await api.importTable(connId, {
        schema,
        table,
        format,
        onConflict,
        file,
        signal: abortCtl.signal,
        onProgress: (n) => { uploadBytes = n },
      })
      result = {
        rowsImported: r.rowsImported,
        skipped: r.skipped,
        totalErrors: r.totalErrors,
        errors: r.errors || [],
        bytes: r.bytes,
        truncated: r.truncated,
        jobId: r.jobId,
      }
      if (onImported) {
        try { onImported({ rowsImported: r.rowsImported, skipped: r.skipped, totalErrors: r.totalErrors }) } catch { /* ignore */ }
      }
      await tick()
    } catch (e) {
      const isAbort = e && (/** @type {any} */(e).name === 'AbortError' || abortCtl?.signal.aborted)
      if (isAbort) {
        errorMsg = 'cancelled'
      } else if (e instanceof AuraDBError) {
        if (e.status === 409) {
          const detail = /** @type {any} */(e.detail) || {}
          const ra = (detail && typeof detail.retryAfter === 'number') ? detail.retryAfter : 5
          retryAfter = ra
          errorMsg = `another import is already running — retry available in ${ra}s`
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
        errorMsg = (e && /** @type {any} */(e).message) || 'import failed'
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

<svelte:window onkeydown={onModalKey} />

<Modal bind:open title="Import into {schema}.{table}" width={620}>
  <div class="import-modal">
    <fieldset class="import-modal__fmt" aria-labelledby="import-fmt-legend">
      <legend id="import-fmt-legend">Format</legend>
      <div role="radiogroup" aria-labelledby="import-fmt-legend">
        <label class="import-modal__radio">
          <input type="radio" name="import-format" value="csv" checked={format === 'csv'} onchange={() => { format = 'csv' }} disabled={busy} />
          <span>CSV</span>
        </label>
        <label class="import-modal__radio">
          <input type="radio" name="import-format" value="ndjson" checked={format === 'ndjson'} onchange={() => { format = 'ndjson' }} disabled={busy} />
          <span>NDJSON</span>
        </label>
        <!-- SQL imports intentionally absent — security boundary. The
             server rejects format=sql; replaying a dump goes through
             the SQL editor under its own classifier + ActionQueryWrite. -->
      </div>
    </fieldset>

    <fieldset class="import-modal__conflict" aria-labelledby="import-conflict-legend">
      <legend id="import-conflict-legend">On duplicate primary key</legend>
      <div role="radiogroup" aria-labelledby="import-conflict-legend">
        <label class="import-modal__radio">
          <input type="radio" name="import-conflict" value="error" checked={onConflict === 'error'} onchange={() => { onConflict = 'error' }} disabled={busy} />
          <span>Error (stop on first conflict)</span>
        </label>
        <label class="import-modal__radio">
          <input type="radio" name="import-conflict" value="skip" checked={onConflict === 'skip'} onchange={() => { onConflict = 'skip' }} disabled={busy} />
          <span>Skip (keep existing row)</span>
        </label>
        <label class="import-modal__radio">
          <input type="radio" name="import-conflict" value="update" checked={onConflict === 'update'} onchange={() => { onConflict = 'update' }} disabled={busy} />
          <span>Update (overwrite by primary key)</span>
        </label>
      </div>
    </fieldset>

    <label class="import-modal__file">
      <span>File</span>
      <input
        type="file"
        accept={acceptAttr}
        onchange={onPickFile}
        disabled={busy}
        aria-label="File to import"
      />
      {#if file}
        <span class="import-modal__file-hint"><code>{file.name}</code> · {formatBytes(file.size)}</span>
      {:else}
        <span class="import-modal__file-hint">Max {formatBytes(IMPORT_BODY_HARD_CAP)}. Up to {IMPORT_ROW_HARD_CAP.toLocaleString()} rows per upload.</span>
      {/if}
    </label>

    <div role="status" aria-live={errorMsg ? 'assertive' : 'polite'} aria-atomic="true" class="import-modal__status">
      {#if busy}
        <span class="import-modal__status-row">
          <Spinner size={12} />
          <span>Importing… {uploadBytes > 0 ? formatBytes(uploadBytes) : ''}</span>
          <span class="import-modal__hint">(Escape to cancel)</span>
        </span>
      {:else if result}
        <span class="import-modal__status-row">
          <Pill tone={result.totalErrors > 0 ? 'warning' : 'success'}>Done</Pill>
          <span>
            {result.rowsImported.toLocaleString()} imported
            {#if result.skipped > 0} · {result.skipped.toLocaleString()} skipped{/if}
            {#if result.totalErrors > 0} · {result.totalErrors.toLocaleString()} error{result.totalErrors === 1 ? '' : 's'}{/if}
            · {formatBytes(result.bytes)}
          </span>
        </span>
        {#if result.truncated}
          <div class="import-modal__warn" role="alert">
            Import stopped at {IMPORT_ROW_HARD_CAP.toLocaleString()} rows — file had more. Re-run with the remaining rows or split the file.
          </div>
        {/if}
        {#if result.errors.length > 0}
          <details class="import-modal__errors">
            <summary>Per-row errors ({result.errors.length}{result.totalErrors > result.errors.length ? ` of ${result.totalErrors}` : ''})</summary>
            <ul>
              {#each result.errors as e (e.rowIndex)}
                <li>Row {e.rowIndex}: <code>{e.code}</code> — {e.message}</li>
              {/each}
            </ul>
          </details>
        {/if}
      {:else if errorMsg}
        <span class="import-modal__status-row">
          <Pill tone="danger">Error</Pill>
          <span>{errorMsg}{retryAfter > 0 ? ` (${retryAfter}s)` : ''}</span>
        </span>
      {/if}
    </div>
  </div>

  {#snippet footer()}
    <div class="import-modal__actions">
      {#if busy}
        <Btn variant="ghost" onclick={cancelInFlight}>Cancel</Btn>
        <Btn variant="primary" loading ariaBusy disabled>Importing…</Btn>
      {:else if result}
        <Btn variant="primary" onclick={close}>Done</Btn>
      {:else}
        <Btn variant="ghost" onclick={close}>Close</Btn>
        <Btn
          variant="primary"
          onclick={submit}
          disabled={busy || !file || retryAfter > 0}
        >Import</Btn>
      {/if}
    </div>
  {/snippet}
</Modal>

<style>
  .import-modal { display: grid; gap: 14px; }
  .import-modal__fmt > div,
  .import-modal__conflict > div { display: flex; gap: 18px; margin-top: 6px; flex-wrap: wrap; }
  .import-modal__conflict > div { flex-direction: column; gap: 6px; }
  .import-modal__radio { display: inline-flex; align-items: center; gap: 6px; cursor: pointer; }
  .import-modal__file { display: grid; gap: 4px; }
  .import-modal__file > span { font-size: 0.85em; opacity: 0.8; }
  .import-modal__file-hint { font-size: 0.8em; opacity: 0.7; }
  .import-modal__file-hint code { font-family: ui-monospace, monospace; }
  .import-modal__status { min-height: 1.6em; font-size: 0.9em; opacity: 0.95; display: grid; gap: 6px; }
  .import-modal__status-row { display: inline-flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .import-modal__actions { display: flex; justify-content: flex-end; gap: 8px; }
  .import-modal__hint { opacity: 0.6; font-size: 0.85em; margin-left: 6px; }
  .import-modal__warn {
    margin-top: 4px; padding: 6px 8px; border-radius: 4px;
    background: var(--warn-bg, rgba(255, 180, 0, 0.12));
    color: var(--warn-fg, #b88600);
    font-size: 0.85em;
  }
  .import-modal__errors {
    border: 1px solid var(--border, rgba(0,0,0,0.1));
    border-radius: 4px;
    padding: 6px 10px;
    background: var(--surface-2, rgba(0,0,0,0.02));
  }
  .import-modal__errors > summary { cursor: pointer; user-select: none; font-size: 0.9em; }
  .import-modal__errors ul {
    margin: 6px 0 0 0;
    padding-left: 18px;
    max-height: 180px;
    overflow: auto;
    font-size: 0.85em;
    line-height: 1.4;
  }
  .import-modal__errors code { font-family: ui-monospace, monospace; font-size: 0.95em; }
</style>

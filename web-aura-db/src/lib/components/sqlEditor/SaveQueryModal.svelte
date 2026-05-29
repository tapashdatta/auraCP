<script>
  // PR #13.5 — replaces the previous window.prompt-driven save flow:
  //   - native prompt was a single-line text input with no description,
  //     no duplicate-detection, no AT story (EXEC-9 / a11y-10);
  //   - INT-5: the "saved queries are session-only" caveat had no UI
  //     surface, so operators could lose work on daemon restart;
  // This modal exposes name + description + tags, surfaces duplicate
  // names with a "Replace?" confirmation, and renders the session-only
  // caveat next to the Save button.

  import Modal from '../Modal.svelte'
  import Btn from '../Btn.svelte'
  import TextField from '../TextField.svelte'

  /** @type {{
   *   open?: boolean,
   *   statement?: string,
   *   existingNames?: string[],
   *   sessionOnly?: boolean,
   *   onClose?: ()=>void,
   *   onSave?: (payload: {name:string, description:string, tags:string[], replace:boolean})=>void,
   * }} */
  let {
    open = $bindable(false),
    statement = '',
    existingNames = [],
    sessionOnly = true,
    onClose,
    onSave,
  } = $props()

  let name = $state('')
  let description = $state('')
  let tagsRaw = $state('')
  let err = $state('')

  // Reset fields on open transitions.
  let _wasOpen = false
  $effect(() => {
    if (open && !_wasOpen) {
      _wasOpen = true
      name = ''
      description = ''
      tagsRaw = ''
      err = ''
    } else if (!open && _wasOpen) {
      _wasOpen = false
    }
  })

  const isDuplicate = $derived(
    name.trim() !== '' && existingNames.some((n) => n === name.trim()),
  )
  const emptyStatement = $derived(!statement || statement.trim() === '')
  const canSubmit = $derived(name.trim().length > 0 && !emptyStatement)

  function close() {
    open = false
    onClose?.()
  }

  function submit() {
    if (!canSubmit) {
      if (emptyStatement) err = 'Cannot save an empty query.'
      else if (!name.trim()) err = 'Name is required.'
      return
    }
    const tags = tagsRaw
      .split(',')
      .map((t) => t.trim())
      .filter((t) => t.length > 0)
    onSave?.({
      name: name.trim(),
      description: description.trim(),
      tags,
      replace: isDuplicate,
    })
  }
</script>

<Modal bind:open title="Save query" width={520} onClose={onClose}>
  <div class="sq">
    {#if emptyStatement}
      <p class="sq__err" role="alert">The editor buffer is empty — type a statement first.</p>
    {/if}
    <label class="sq__row">
      <span class="sq__label">Name</span>
      <TextField bind:value={name} placeholder="e.g. orders-by-day" />
    </label>
    {#if isDuplicate}
      <p class="sq__warn" role="status">
        A saved query named <strong>{name}</strong> already exists. Saving will replace it.
      </p>
    {/if}
    <label class="sq__row">
      <span class="sq__label">Description <span class="sq__hint">(optional)</span></span>
      <TextField bind:value={description} placeholder="What does this query do?" />
    </label>
    <label class="sq__row">
      <span class="sq__label">Tags <span class="sq__hint">(comma-separated)</span></span>
      <TextField bind:value={tagsRaw} placeholder="reports, daily" />
    </label>
    {#if err}<p class="sq__err" role="alert">{err}</p>{/if}
    {#if sessionOnly}
      <p class="sq__note" aria-live="polite">
        <strong>Heads up:</strong> saved queries are kept in the panel's session
        store for now and are <em>not</em> persisted across panel restarts.
      </p>
    {/if}
  </div>
  {#snippet footer()}
    <Btn variant="ghost" onclick={close}>Cancel</Btn>
    <Btn
      variant="primary"
      onclick={submit}
      disabled={!canSubmit}
      reason={emptyStatement ? 'Editor buffer is empty' : (!name.trim() ? 'Name is required' : '')}
    >{isDuplicate ? 'Replace' : 'Save'}</Btn>
  {/snippet}
</Modal>

<style>
  .sq { display: flex; flex-direction: column; gap: 12px; }
  .sq__row { display: flex; flex-direction: column; gap: 4px; }
  .sq__label { font-size: 0.85em; color: var(--text-dim, #888); }
  .sq__hint { color: var(--text-dim, #888); font-weight: normal; }
  .sq__warn { margin: 0; padding: 8px 10px; border-radius: 4px; background: rgba(217, 119, 6, 0.12); color: var(--warning, #d97706); font-size: 0.9em; }
  .sq__err { margin: 0; padding: 8px 10px; border-radius: 4px; background: rgba(220, 38, 38, 0.12); color: var(--danger, #dc2626); font-size: 0.9em; }
  .sq__note { margin: 0; padding: 8px 10px; border-radius: 4px; background: rgba(37, 99, 235, 0.08); color: var(--text-dim, #888); font-size: 0.85em; }
</style>


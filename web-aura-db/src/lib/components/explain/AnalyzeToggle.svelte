<script>
  import Modal from '../Modal.svelte'
  import Btn from '../Btn.svelte'

  /**
   * @type {{
   *   value?: boolean,
   *   stmt?: string,
   *   connId?: string,
   *   currentClass?: string,
   *   onChange?: (next: boolean) => void,
   * }}
   */
  let {
    value = false,
    stmt = '',
    connId = '',
    currentClass = 'read',
    onChange,
  } = $props()

  let confirmOpen = $state(false)
  let confirmText = $state('')
  /** @type {HTMLInputElement|undefined} */
  let confirmInputEl = $state(undefined)

  // FIX A11Y-1 / A11Y-2 (PR #14):
  //   - The typed-ANALYZE input now lives *inside* the dialog body (Modal
  //     slot), so dismissing the modal also dismisses the input. The
  //     Confirm button is bound to `confirmText === "ANALYZE"` so the
  //     user gets visual feedback that the typed gate is required.
  //   - The toggle never mutates `analyze` directly — clicking the
  //     toggle for a non-read statement opens the dialog and waits for a
  //     typed confirmation. The toggle for read statements still flips
  //     immediately (no gate needed).
  //   - The inner styled toggle is a display-only indicator now; the
  //     entire control surface is one button so the two-controls-desync
  //     problem cannot occur.

  const canConfirm = $derived(confirmText.trim() === 'ANALYZE')
  const partialHint = $derived(
    confirmText.length > 0 && !canConfirm
      ? 'Type ANALYZE (uppercase) to confirm'
      : '',
  )

  function onToggle() {
    const next = !value
    if (next && currentClass !== 'read') {
      // Non-read + turning ON → require typed confirmation.
      confirmText = ''
      confirmOpen = true
      // Focus the input after the modal mounts.
      queueMicrotask(() => { confirmInputEl?.focus() })
      return
    }
    // Read statements (or turning OFF) flip directly.
    onChange?.(next)
  }

  function onConfirm() {
    if (!canConfirm) return
    confirmOpen = false
    confirmText = ''
    onChange?.(true)
  }

  function onCancel() {
    confirmOpen = false
    confirmText = ''
  }
</script>

<div class="analyze-toggle">
  <button
    type="button"
    class="toggle analyze-toggle__display {value ? 'toggle--on' : ''}"
    aria-pressed={value}
    aria-label="Toggle EXPLAIN ANALYZE"
    title="Cmd+Shift+E"
    onclick={onToggle}
  >
    <span class="toggle__rail" aria-hidden="true"></span>
    <span class="toggle__label">ANALYZE</span>
    <span class="analyze-toggle__kbd" aria-hidden="true">⌘⇧E</span>
  </button>
</div>

<Modal bind:open={confirmOpen} title={`Run analyze on a ${currentClass} statement?`} width={460}>
  {#snippet footer()}
    <Btn variant="ghost" onclick={onCancel}>Cancel</Btn>
    <Btn variant="danger" onclick={onConfirm} disabled={!canConfirm}>Confirm ANALYZE</Btn>
  {/snippet}
  <div class="analyze-toggle__dialogBody">
    <p class="analyze-toggle__dialogP">
      EXPLAIN ANALYZE executes the statement. For non-read statements this is destructive.
      Type <strong>ANALYZE</strong> to confirm.
    </p>
    <label class="analyze-toggle__confirmInput">
      Type ANALYZE to confirm:
      <input
        bind:this={confirmInputEl}
        type="text"
        class="input analyze-toggle__confirm"
        data-testid="analyze-confirm-input"
        bind:value={confirmText}
        autocomplete="off"
        spellcheck="false"
      />
    </label>
    {#if partialHint}
      <p
        class="analyze-toggle__hint analyze-toggle__partialHint"
        data-testid="analyze-confirm-hint"
      >
        {partialHint}
      </p>
    {/if}
  </div>
</Modal>

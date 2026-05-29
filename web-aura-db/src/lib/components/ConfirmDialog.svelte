<script>
  import Modal from './Modal.svelte'
  import Btn from './Btn.svelte'
  import { t } from '../strings.js'

  /** @type {{
   *   open?: boolean,
   *   title?: string,
   *   message?: string,
   *   confirmLabel?: string,
   *   cancelLabel?: string,
   *   tone?: 'danger'|'normal',
   *   onConfirm?: ()=>void,
   *   onCancel?: ()=>void,
   * }} */
  let {
    open = $bindable(false),
    title = '',
    message = '',
    confirmLabel = t('action.confirm'),
    cancelLabel = t('action.cancel'),
    tone = 'normal',
    onConfirm,
    onCancel,
  } = $props()

  function cancel() { open = false; onCancel?.() }
  function confirm() { open = false; onConfirm?.() }
</script>

<Modal bind:open {title} width={460}>
  {#snippet footer()}
    <Btn variant="ghost" onclick={cancel}>{cancelLabel}</Btn>
    <Btn variant={tone === 'danger' ? 'danger' : 'primary'} onclick={confirm}>{confirmLabel}</Btn>
  {/snippet}
  <p class="confirm__msg">{message}</p>
</Modal>

<style>
  .confirm__msg { margin: 0; color: var(--text-dim); }
</style>

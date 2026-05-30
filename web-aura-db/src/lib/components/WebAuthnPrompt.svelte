<script>
  // v0.3.2-D: Step-up modal asking the user to tap their security key.
  //
  // Wraps the existing Modal.svelte. The host passes:
  //   open      — bindable boolean controlling visibility.
  //   username  — currently signed-in user, for the WebAuthn allow-list.
  //   action    — dbadmin Action class the caller wants to unlock.
  //   request   — fetch wrapper (typically api.request) shared with
  //               lib/webauthn.js so the chain stays mockable in tests.
  //   onVerified — called with the /step-up/verify response on success.
  //   onCancel   — optional; called when the user dismisses the modal.
  //
  // This component does NOT decide whether WebAuthn or TOTP is the
  // right factor; that gate lives in the host. When invoked we just
  // run the assertion ceremony and report the outcome.

  import Modal from './Modal.svelte'
  import Btn from './Btn.svelte'
  import { stepUpWithWebAuthn, supported } from '../webauthn.js'

  /** @type {{
   *   open?: boolean,
   *   username: string,
   *   action: string,
   *   request: (path: string, init?: object) => Promise<any>,
   *   onVerified?: (resp: any) => void,
   *   onCancel?: () => void,
   * }} */
  let { open = $bindable(false), username, action, request, onVerified, onCancel } = $props()

  let busy = $state(false)
  let err = $state('')

  async function tap() {
    err = ''
    busy = true
    try {
      const resp = await stepUpWithWebAuthn(request, username, action)
      onVerified?.(resp)
      open = false
    } catch (e) {
      err = (e && e.message) ? e.message : String(e)
    } finally {
      busy = false
    }
  }

  function dismiss() {
    onCancel?.()
    open = false
  }
</script>

<Modal bind:open title="Verify with security key" width={420} onClose={dismiss}>
  {#snippet children()}
    <div class="wa-body">
      {#if !supported()}
        <p class="wa-error">
          Your browser does not expose the WebAuthn API. Fall back to a TOTP code or recovery code instead.
        </p>
      {:else}
        <p>To continue, tap your registered security key (or use Touch ID / Windows Hello).</p>
        {#if err}
          <p class="wa-error" role="alert">{err}</p>
        {/if}
      {/if}
    </div>
  {/snippet}
  {#snippet footer()}
    <div class="wa-foot">
      <Btn onclick={dismiss} disabled={busy}>Cancel</Btn>
      <Btn variant="primary" onclick={tap} disabled={busy || !supported()}>
        {busy ? 'Waiting for tap…' : 'Tap key'}
      </Btn>
    </div>
  {/snippet}
</Modal>

<style>
  .wa-body { padding: 0.5rem 1rem 1rem; line-height: 1.45; }
  .wa-foot { display: flex; gap: 0.5rem; justify-content: flex-end; padding: 0.5rem 1rem 1rem; }
  .wa-error { color: var(--danger, #c0392b); margin-top: 0.5rem; }
</style>

<script>
  import { go } from '../lib/store.svelte.js'
  import { session, mfaSetup, mfaEnable, mfaDisable } from '../lib/auth.svelte.js'

  let setup = $state(null)   // { secret, uri }
  let code = $state('')
  let msg = $state('')
  let busy = $state(false)

  async function startSetup() { setup = await mfaSetup(); msg = '' }
  async function enable() {
    busy = true
    const ok = await mfaEnable(setup.secret, code)
    msg = ok ? '' : 'Invalid code — try again'
    if (ok) { setup = null; code = '' }
    busy = false
  }
  async function disable() {
    busy = true
    const ok = await mfaDisable(code)
    msg = ok ? '' : 'Invalid code — try again'
    if (ok) code = ''
    busy = false
  }
</script>

<div class="wrap form-wrap fade">
  <button type="button" class="back" onclick={() => go('sites')}>
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M15 18l-6-6 6-6"/></svg> Back to Sites
  </button>
  <div class="ph"><div><h1>Account &amp; Security</h1><div class="sub">{session.user?.email} · {session.user?.role}</div></div></div>

  <div class="section"><div class="section-h"><div><h3>Two-Factor Authentication</h3>
      <p>TOTP via an authenticator app (Google Authenticator, 1Password, …)</p></div>
      <span class="status"><span class="sdot {session.user?.mfaEnabled ? 's-up' : 's-down'}"></span>{session.user?.mfaEnabled ? 'Enabled' : 'Disabled'}</span>
    </div>
    <div class="section-b">
      {#if session.user?.mfaEnabled}
        <p style="color:var(--txt-2);margin-bottom:14px">Enter a current code to disable 2FA.</p>
        <div class="input-row" style="max-width:320px">
          <input class="input" inputmode="numeric" maxlength="6" bind:value={code} placeholder="000000" style="text-align:center;letter-spacing:.3em">
          <button class="btn btn-ghost" onclick={disable} disabled={busy}>Disable</button>
        </div>
      {:else if !setup}
        <button class="btn btn-primary" onclick={startSetup}>Enable 2FA</button>
      {:else}
        <p style="color:var(--txt-2);margin-bottom:10px">Add this secret to your authenticator, then enter a code to confirm.</p>
        <div class="kv"><span class="k">Secret</span><span class="v">{setup.secret}</span></div>
        <div class="kv"><span class="k">otpauth URI</span><span class="v" style="font-size:11px;word-break:break-all;max-width:60%">{setup.uri}</span></div>
        <div class="input-row" style="max-width:320px;margin-top:14px">
          <input class="input" inputmode="numeric" maxlength="6" bind:value={code} placeholder="000000" style="text-align:center;letter-spacing:.3em">
          <button class="btn btn-primary" onclick={enable} disabled={busy}>Confirm</button>
        </div>
      {/if}
      {#if msg}<div class="login-err" style="margin-top:12px">{msg}</div>{/if}
    </div>
  </div>
</div>

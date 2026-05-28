<script>
  import { session, login, verifyMfa, setupAdmin } from '../lib/auth.svelte.js'

  let email = $state('')
  let password = $state('')
  let confirm = $state('')
  let code = $state('')
  let busy = $state(false)

  async function submit(e) {
    e.preventDefault(); busy = true; await login(email, password); busy = false
  }
  async function submitCode(e) {
    e.preventDefault(); busy = true; await verifyMfa(code); busy = false
  }
  async function submitSetup(e) {
    e.preventDefault()
    if (password.length < 8) { session.error = 'Password must be at least 8 characters'; return }
    if (password !== confirm) { session.error = 'Passwords do not match'; return }
    busy = true; await setupAdmin(email, password); busy = false
  }
</script>

<div class="login-wrap fade">
  <div class="login-card">
    <div class="login-brand"><span class="gem"></span>aura<span>CP</span></div>

    {#if session.setupRequired}
      <h2>Create your admin account</h2>
      <p class="login-sub">First-time setup — this becomes the panel administrator</p>
      <form onsubmit={submitSetup}>
        <div class="field"><label>
          <span class="label-text">Email</span>
          <input class="input" type="email" autocomplete="username" bind:value={email} placeholder="you@example.com">
        </label></div>
        <div class="field"><label>
          <span class="label-text">Password <span class="hint">min 8 characters</span></span>
          <input class="input" type="password" autocomplete="new-password" bind:value={password} placeholder="••••••••">
        </label></div>
        <div class="field"><label>
          <span class="label-text">Confirm password</span>
          <input class="input" type="password" autocomplete="new-password" bind:value={confirm} placeholder="••••••••">
        </label></div>
        {#if session.error}<div class="login-err">{session.error}</div>{/if}
        <button class="btn btn-primary login-btn" disabled={busy}>{busy ? 'Creating…' : 'Create account'}</button>
      </form>
    {:else if !session.mfaRequired}
      <h2>Sign in</h2>
      <p class="login-sub">Control panel access</p>
      <form onsubmit={submit}>
        <div class="field"><label>
          <span class="label-text">Email</span>
          <input class="input" type="email" autocomplete="username" bind:value={email} placeholder="you@example.com">
        </label></div>
        <div class="field"><label>
          <span class="label-text">Password</span>
          <input class="input" type="password" autocomplete="current-password" bind:value={password} placeholder="••••••••">
        </label></div>
        {#if session.error}<div class="login-err">{session.error}</div>{/if}
        <button class="btn btn-primary login-btn" disabled={busy}>{busy ? 'Signing in…' : 'Sign in'}</button>
      </form>
    {:else}
      <h2>Two-factor code</h2>
      <p class="login-sub">Enter the 6-digit code from your authenticator app</p>
      <form onsubmit={submitCode}>
        <div class="field">
          <label class="sr-only" for="mfa-code">Two-factor verification code</label>
          <input id="mfa-code" class="input" inputmode="numeric" maxlength="6" bind:value={code}
            placeholder="000000" style="text-align:center;font-size:22px;letter-spacing:.4em">
        </div>
        {#if session.error}<div class="login-err">{session.error}</div>{/if}
        <button class="btn btn-primary login-btn" disabled={busy}>{busy ? 'Verifying…' : 'Verify'}</button>
      </form>
    {/if}
  </div>
</div>

<script>
  import { session, login, verifyMfa } from '../lib/auth.svelte.js'

  let email = $state('')
  let password = $state('')
  let code = $state('')
  let busy = $state(false)

  async function submit(e) {
    e.preventDefault()
    busy = true
    await login(email, password)
    busy = false
  }
  async function submitCode(e) {
    e.preventDefault()
    busy = true
    await verifyMfa(code)
    busy = false
  }
</script>

<div class="login-wrap fade">
  <div class="login-card">
    <div class="login-brand"><span class="gem"></span>aura<span>CP</span></div>

    {#if !session.mfaRequired}
      <h2>Sign in</h2>
      <p class="login-sub">Control panel access</p>
      <form onsubmit={submit}>
        <div class="field"><label>Email</label>
          <input class="input" type="email" autocomplete="username" bind:value={email} placeholder="you@example.com"></div>
        <div class="field"><label>Password</label>
          <input class="input" type="password" autocomplete="current-password" bind:value={password} placeholder="••••••••"></div>
        {#if session.error}<div class="login-err">{session.error}</div>{/if}
        <button class="btn btn-primary login-btn" disabled={busy}>{busy ? 'Signing in…' : 'Sign in'}</button>
      </form>
    {:else}
      <h2>Two-factor code</h2>
      <p class="login-sub">Enter the 6-digit code from your authenticator app</p>
      <form onsubmit={submitCode}>
        <div class="field">
          <input class="input" inputmode="numeric" maxlength="6" bind:value={code}
            placeholder="000000" style="text-align:center;font-size:22px;letter-spacing:.4em">
        </div>
        {#if session.error}<div class="login-err">{session.error}</div>{/if}
        <button class="btn btn-primary login-btn" disabled={busy}>{busy ? 'Verifying…' : 'Verify'}</button>
      </form>
    {/if}
  </div>
</div>

<script>
  import { onMount } from 'svelte'
  import { go, ui } from '../store.svelte.js'
  import { toggleTheme, getTheme } from '../theme.js'
  import { session, logout } from '../auth.svelte.js'
  import { apiFetch } from '../api.js'

  let theme = $state(getTheme())
  let menu = $state(false)
  let host = $state('')
  let updateAvailable = $state(false)
  let updateLatest = $state('')
  let updateCurrent = $state('')
  let upgrading = $state(false)
  let upgradeMsg = $state('')
  function flip() { theme = toggleTheme() }
  const initials = $derived((session.user?.email || 'A')[0].toUpperCase())
  const isAdmin = $derived(session.user?.role === 'ROLE_ADMIN')
  async function doLogout() { menu = false; await logout() }
  function openAccount() { menu = false; go('account') }

  // One-click in-place upgrade. Same flow the Updates card on Instance uses,
  // but reachable from any screen — no detour through Settings.
  async function applyUpdate() {
    if (!updateAvailable || upgrading) return
    if (!confirm(`Upgrade auracpd from ${updateCurrent || 'current'} to ${updateLatest}?\nThe panel will restart automatically.`)) return
    upgrading = true
    upgradeMsg = `Upgrading to ${updateLatest}…`
    await apiFetch('/api/instance/update', { method: 'POST' })
    let tries = 0
    const tick = setInterval(async () => {
      tries++
      try {
        const h = await fetch('/api/health', { cache: 'no-store' })
        if (h.ok) { clearInterval(tick); upgradeMsg = 'Upgraded. Reloading…'; setTimeout(() => location.reload(), 400); return }
      } catch {}
      if (tries > 60) { clearInterval(tick); upgradeMsg = 'Panel did not come back within 60s. Check journalctl -u auracpd.'; upgrading = false }
    }, 1000)
  }

  onMount(async () => {
    const r = await apiFetch('/api/instance')
    if (r.ok) { const d = await r.json(); host = d.hostname || d.os || '' }
    // Cheap check (server caches 1h) so the badge appears on first paint.
    const u = await apiFetch('/api/instance/update')
    if (u.ok) {
      const d = await u.json()
      updateAvailable = !!d.available
      updateLatest = d.latestPlain || ''
      updateCurrent = d.current || ''
    }
  })
</script>

<div class="topbar">
  <button class="brand" type="button" onclick={() => go('sites')} aria-label="auraCP — home">
    <span class="gem"></span>aura<span>CP</span>
  </button>
  <nav class="nav" aria-label="Primary">
    <button type="button" class:active={ui.view === 'sites'}    onclick={() => go('sites')}>
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true" style="width:14px;height:14px;vertical-align:-2px;margin-right:6px"><circle cx="12" cy="12" r="10"/><line x1="2" y1="12" x2="22" y2="12"/><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/></svg>
      Sites
    </button>
    {#if isAdmin}
      <button type="button" class:active={ui.view === 'users'}  onclick={() => go('users')}>
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true" style="width:14px;height:14px;vertical-align:-2px;margin-right:6px"><path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M23 21v-2a4 4 0 0 0-3-3.87M16 3.13a4 4 0 0 1 0 7.75"/></svg>
        Users
      </button>
    {/if}
    <button type="button" class:active={ui.view === 'instance'} onclick={() => go('instance')}>
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true" style="width:14px;height:14px;vertical-align:-2px;margin-right:6px"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>
      Settings
    </button>
  </nav>
  <div class="spacer"></div>
  {#if updateAvailable}
    <button type="button" class="update-badge" onclick={applyUpdate} disabled={upgrading}
            title={upgrading ? upgradeMsg : `auraCP ${updateLatest} is available — click to upgrade in place`}
            aria-label={upgrading ? upgradeMsg : `Upgrade to auraCP ${updateLatest}`}>
      <span class="sdot {upgrading ? 's-warn' : 's-warn'}" class:spinning={upgrading}></span>
      <span>{upgrading ? 'Upgrading…' : `Update ${updateLatest}`}</span>
    </button>
  {/if}
  {#if host}<div class="instance-pill"><span class="sdot s-up"></span><span class="mono">{host}</span></div>{/if}
  <button class="icon-btn" onclick={flip} title="Toggle theme" aria-label="Toggle theme">
    {#if theme === 'dark'}
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M5 5l1.5 1.5M17.5 17.5L19 19M2 12h2M20 12h2M5 19l1.5-1.5M17.5 6.5L19 5"/></svg>
    {:else}
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/></svg>
    {/if}
  </button>
  <div class="avatar-wrap">
    <button class="avatar" onclick={() => menu = !menu} aria-label="Account menu">{initials}{initials === 'I' ? 'L' : ''}</button>
    {#if menu}
      <div class="menu">
        <div class="menu-head">
          <div class="menu-email">{session.user?.email}</div>
          <div class="menu-role mono">{session.user?.role}</div>
        </div>
        <button class="menu-item" onclick={openAccount}>Account &amp; Security</button>
        <button class="menu-item danger" onclick={doLogout}>Log out</button>
      </div>
    {/if}
  </div>
</div>

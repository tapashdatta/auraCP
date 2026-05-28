<script>
  import { onMount } from 'svelte'
  import { go, ui } from '../store.svelte.js'
  import { toggleTheme, getTheme } from '../theme.js'
  import { session, logout } from '../auth.svelte.js'
  import { apiFetch } from '../api.js'

  let theme = $state(getTheme())
  let menu = $state(false)
  let host = $state('')
  function flip() { theme = toggleTheme() }
  const initials = $derived((session.user?.email || 'A')[0].toUpperCase())
  const isAdmin = $derived(session.user?.role === 'ROLE_ADMIN')
  async function doLogout() { menu = false; await logout() }
  function openAccount() { menu = false; go('account') }

  onMount(async () => {
    const r = await apiFetch('/api/instance')
    if (r.ok) { const d = await r.json(); host = d.hostname || d.os || '' }
  })
</script>

<div class="topbar">
  <button class="brand" type="button" onclick={() => go('sites')} aria-label="auraCP — home">
    <span class="gem"></span>aura<span>CP</span>
  </button>
  <nav class="nav" aria-label="Primary">
    <button type="button" class:active={ui.view === 'sites'}    onclick={() => go('sites')}>Sites</button>
    {#if isAdmin}
      <button type="button" class:active={ui.view === 'users'}  onclick={() => go('users')}>Users</button>
    {/if}
    <button type="button" class:active={ui.view === 'instance'} onclick={() => go('instance')}>Instance</button>
  </nav>
  <div class="spacer"></div>
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

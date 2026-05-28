<script>
  import { onMount } from 'svelte'
  import { go } from '../lib/store.svelte.js'
  import { apiFetch } from '../lib/api.js'

  const RES = ['sites', 'databases', 'backups', 'cron', 'files', 'ssh_users', 'users', 'settings']
  const ACT = ['create', 'read', 'update', 'delete']
  const roleLabel = { ROLE_ADMIN: 'Admin', ROLE_SITE_MANAGER: 'Site Manager', ROLE_USER: 'User' }

  function defaultsFor(role) {
    const all = () => ({ create: true, read: true, update: true, delete: true })
    const read = () => ({ create: false, read: true, update: false, delete: false })
    const none = () => ({ create: false, read: false, update: false, delete: false })
    const s = {}
    if (role === 'ROLE_ADMIN') { RES.forEach(r => s[r] = all()); return s }
    if (role === 'ROLE_SITE_MANAGER') {
      RES.forEach(r => s[r] = all()); s.users = none(); s.settings = read(); return s
    }
    RES.forEach(r => s[r] = read()); s.ssh_users = none(); s.users = none(); s.settings = none(); return s
  }

  let users = $state([])
  let editing = $state(null) // null = add mode, else email being edited
  let f = $state({ email: '', role: 'ROLE_USER', password: '' })
  let perms = $state(defaultsFor('ROLE_USER'))
  let busy = $state(false), notice = $state(''), error = $state('')

  async function load() {
    const r = await apiFetch('/api/admin/users')
    users = r.ok ? await r.json() : []
  }
  onMount(load)

  function onRole() { perms = defaultsFor(f.role) }
  function resetForm() { editing = null; f = { email: '', role: 'ROLE_USER', password: '' }; perms = defaultsFor('ROLE_USER'); error = '' }
  function edit(u) {
    editing = u.email
    f = { email: u.email, role: u.role, password: '' }
    try { perms = u.permissions ? JSON.parse(u.permissions) : defaultsFor(u.role) }
    catch { perms = defaultsFor(u.role) }
    // ensure all resources present
    for (const r of RES) if (!perms[r]) perms[r] = { create: false, read: false, update: false, delete: false }
    window.scrollTo({ top: 9999, behavior: 'smooth' })
  }

  async function save() {
    error = ''; notice = ''
    if (!editing && !f.email) { error = 'Email required'; return }
    busy = true
    const body = { role: f.role, permissions: JSON.stringify(perms) }
    let r
    if (editing) {
      r = await apiFetch('/api/admin/users/' + encodeURIComponent(editing), { method: 'PUT', body: JSON.stringify(body) })
    } else {
      r = await apiFetch('/api/admin/users', { method: 'POST', body: JSON.stringify({ ...body, email: f.email, password: f.password }) })
    }
    const d = await r.json().catch(() => ({}))
    busy = false
    if (!r.ok) { error = d.error || 'Failed'; return }
    notice = editing ? `Updated ${editing}` : `Created ${d.email}${d.password ? ' · password: ' + d.password : ''}`
    resetForm(); load()
  }
  async function del(email) {
    if (!confirm(`Delete ${email}?`)) return
    const r = await apiFetch('/api/admin/users/' + encodeURIComponent(email), { method: 'DELETE' })
    if (r.ok) load(); else { const d = await r.json().catch(() => ({})); error = d.error || 'Failed' }
  }
</script>

<div class="wrap fade">
  <button type="button" class="back" onclick={() => go('sites')}><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M15 18l-6-6 6-6"/></svg> Back to Sites</button>
  <div class="ph"><div><h1>Users</h1><div class="sub">Panel accounts, roles &amp; permissions</div></div></div>

  <div class="card" style="margin-bottom:18px">
    {#if users.length === 0}<div class="empty">No users.</div>
    {:else}
      <table><thead><tr><th>Email</th><th>Role</th><th>2FA</th><th></th></tr></thead><tbody>
        {#each users as u}
          <tr><td><span class="mono">{u.email}</span></td><td>{roleLabel[u.role] || u.role}</td>
            <td><span class="status"><span class="sdot {u.mfaEnabled ? 's-up' : 's-down'}"></span>{u.mfaEnabled ? 'On' : 'Off'}</span></td>
            <td style="text-align:right">
              <button type="button" class="manage" onclick={() => edit(u)}>Edit</button>
              <button type="button" class="manage" onclick={() => del(u.email)}>Delete</button>
            </td></tr>
        {/each}
      </tbody></table>
    {/if}
  </div>

  <div class="card"><div class="section-h"><div><h3>{editing ? 'Edit ' + editing : 'Add user'}</h3><p>Role sets defaults; fine-tune CRUD per resource below</p></div>
    {#if editing}<button class="btn btn-ghost" style="padding:7px 13px" onclick={resetForm}>Cancel</button>{/if}</div>
    <div class="section-b">
      <div class="two">
        {#if !editing}<div class="field"><label>
          <span class="label-text">Email</span>
          <input class="input" bind:value={f.email} placeholder="user@example.com">
        </label></div>{/if}
        <div class="field"><label>
          <span class="label-text">Role</span>
          <select class="select ui" bind:value={f.role} onchange={onRole}>
            <option value="ROLE_ADMIN">Admin</option><option value="ROLE_SITE_MANAGER">Site Manager</option><option value="ROLE_USER">User</option>
          </select>
        </label></div>
      </div>
      {#if !editing}<div class="field"><label>
        <span class="label-text">Password <span class="hint">blank = auto-generate</span></span>
        <input class="input" bind:value={f.password}>
      </label></div>{/if}

      {#if f.role === 'ROLE_ADMIN'}
        <div class="note"><div>Admins have full access to every resource.</div></div>
      {:else}
        <fieldset class="perm-fieldset">
          <legend>Permissions</legend>
          <div style="overflow-x:auto"><table class="perm-grid">
            <thead><tr><th>Resource</th>{#each ACT as a}<th style="text-align:center;text-transform:capitalize">{a}</th>{/each}</tr></thead>
            <tbody>
              {#each RES as r}
                <tr><td><span class="mono">{r}</span></td>
                  {#each ACT as a}
                    <td style="text-align:center;vertical-align:middle">
                      <button type="button" role="switch" aria-checked={!!perms[r][a]} aria-label="{r} {a}"
                              class="toggle toggle-sm" class:on={!!perms[r][a]}
                              onclick={() => perms[r][a] = !perms[r][a]}></button>
                    </td>
                  {/each}
                </tr>
              {/each}
            </tbody>
          </table></div>
        </fieldset>
      {/if}

      <div class="form-actions">
        <button class="btn btn-primary" onclick={save} disabled={busy}>{editing ? 'Save changes' : 'Add user'}</button>
      </div>
      {#if notice}<div class="note" style="margin-top:6px"><div>{notice}</div></div>{/if}
      {#if error}<div class="login-err" style="margin-top:6px">{error}</div>{/if}
    </div>
  </div>
</div>

<style>
  .perm-grid td, .perm-grid th { padding: 9px 12px; }
  .perm-grid td:not(:first-child) { text-align: center }
  .perm-grid .toggle { margin: 0 auto }
  /* fieldset + legend replace the orphan <label> for the permissions matrix. */
  .perm-fieldset { border: 0; padding: 0; margin: 0 0 18px; }
  .perm-fieldset legend { font-weight: 600; font-size: 13px; margin: 6px 0 10px; padding: 0; }
</style>

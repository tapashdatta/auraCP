<script>
  import { onMount } from 'svelte'
  import { go } from '../lib/store.svelte.js'
  import { apiFetch } from '../lib/api.js'
  import { brandIcons } from '../lib/icons.js'
  import { confirmDialog } from '../lib/dialog.svelte.js'

  const RES = ['sites', 'databases', 'backups', 'cron', 'files', 'ssh_users', 'users', 'settings']
  const ACT = ['create', 'read', 'update', 'delete']
  const roleLabel = { ROLE_ADMIN: 'Admin', ROLE_SITE_MANAGER: 'Site Manager', ROLE_USER: 'User' }
  const roleHint = {
    ROLE_ADMIN: 'Full access to everything. Cannot be scoped to a subset of sites.',
    ROLE_SITE_MANAGER: 'Full CRUD on assigned sites + their databases, backups, cron, files, SSH. Settings: read-only. Users: none.',
    ROLE_USER: 'Read-only on assigned sites + their resources. No SSH/FTP, no users, no settings.',
  }

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

  // ---- state --------------------------------------------------------------
  let tab = $state('list')           // 'list' | 'add' | 'roles'
  let users = $state([])
  let sites = $state([])             // for the site-assignment multi-select
  let editing = $state(null)         // null = add mode, else the email being edited
  let f = $state({ email: '', role: 'ROLE_USER', password: '' })
  let perms = $state(defaultsFor('ROLE_USER'))
  let allSitesScope = $state(true)   // true = grant all sites; false = pick from below
  let scopedSites = $state({})       // {domain: true}
  let busy = $state(false), notice = $state(''), error = $state('')

  // v0.2.21: editable role defaults. State shape mirrors the API:
  //   roles[ROLE] = { permissions: {res: {create:bool,...}}, customized: bool, editable: bool }
  let roles = $state({})
  let rolesDirty = $state({})
  let rolesBusy = $state({})
  let rolesMsg = $state('')

  async function loadRoles() {
    const r = await apiFetch('/api/admin/roles')
    if (r.ok) { roles = await r.json(); rolesDirty = {} }
  }

  function toggleRolePerm(role, res, act) {
    if (!roles[role]?.editable) return
    const next = { ...roles }
    next[role] = { ...next[role], permissions: { ...next[role].permissions } }
    const cur = next[role].permissions[res] || { create:false, read:false, update:false, delete:false }
    next[role].permissions[res] = { ...cur, [act]: !cur[act] }
    roles = next
    rolesDirty = { ...rolesDirty, [role]: true }
    rolesMsg = ''
  }

  async function saveRole(role) {
    rolesBusy = { ...rolesBusy, [role]: true }
    const r = await apiFetch('/api/admin/roles/' + encodeURIComponent(role), {
      method: 'PUT', body: JSON.stringify(roles[role].permissions)
    })
    const d = await r.json().catch(() => ({}))
    rolesBusy = { ...rolesBusy, [role]: false }
    if (!r.ok) { rolesMsg = d.error || 'Save failed'; return }
    rolesMsg = `${roleLabel[role]} permissions updated.`
    rolesDirty = { ...rolesDirty, [role]: false }
    await loadRoles()
  }

  async function resetRole(role) {
    if (!(await confirmDialog({
      title: `Reset ${roleLabel[role]} permissions?`,
      message: 'Reverts the per-resource CRUD matrix to the compiled defaults. New users created afterward use the new defaults; existing users keep their per-user overrides.',
      confirmText: 'Reset', danger: true,
    }))) return
    const r = await apiFetch('/api/admin/roles/' + encodeURIComponent(role), { method: 'DELETE' })
    if (!r.ok) { const d = await r.json().catch(() => ({})); rolesMsg = d.error || 'Reset failed'; return }
    rolesMsg = `${roleLabel[role]} reverted to defaults.`
    await loadRoles()
  }

  async function load() {
    const r = await apiFetch('/api/admin/users')
    users = r.ok ? await r.json() : []
    const sr = await apiFetch('/api/sites')
    sites = sr.ok ? await sr.json() : []
    await loadRoles()
  }
  onMount(load)

  function onRole() {
    perms = defaultsFor(f.role)
    if (f.role === 'ROLE_ADMIN') { allSitesScope = true; scopedSites = {} }
  }

  function startAdd() {
    editing = null
    f = { email: '', role: 'ROLE_USER', password: '' }
    perms = defaultsFor('ROLE_USER')
    allSitesScope = true
    scopedSites = {}
    error = ''; notice = ''
    tab = 'add'
  }

  function edit(u) {
    editing = u.email
    f = { email: u.email, role: u.role, password: '' }
    try { perms = u.permissions ? JSON.parse(u.permissions) : defaultsFor(u.role) }
    catch { perms = defaultsFor(u.role) }
    for (const r of RES) if (!perms[r]) perms[r] = { create: false, read: false, update: false, delete: false }
    // Parse sites scope ("" = all; "[]" = none; array of domains otherwise).
    if (!u.sitesScope) { allSitesScope = true; scopedSites = {} }
    else {
      try {
        const arr = JSON.parse(u.sitesScope)
        if (Array.isArray(arr)) {
          if (arr.includes('*')) { allSitesScope = true; scopedSites = {} }
          else { allSitesScope = false; scopedSites = {}; arr.forEach(d => scopedSites[d] = true) }
        } else { allSitesScope = true; scopedSites = {} }
      } catch { allSitesScope = true; scopedSites = {} }
    }
    error = ''; notice = ''
    tab = 'add'
  }

  function buildScopeJSON() {
    if (f.role === 'ROLE_ADMIN' || allSitesScope) return ''  // empty = all sites
    const picked = sites.map(s => s.domain).filter(d => scopedSites[d])
    return JSON.stringify(picked)  // [] (zero sites) is honoured intentionally
  }

  async function save() {
    error = ''; notice = ''
    if (!editing && !f.email) { error = 'Email required'; return }
    busy = true
    const body = {
      role: f.role,
      permissions: JSON.stringify(perms),
      sitesScope: buildScopeJSON(),
    }
    let r
    if (editing) {
      r = await apiFetch('/api/admin/users/' + encodeURIComponent(editing), {
        method: 'PUT', body: JSON.stringify(body),
      })
    } else {
      r = await apiFetch('/api/admin/users', {
        method: 'POST', body: JSON.stringify({ ...body, email: f.email, password: f.password }),
      })
    }
    const d = await r.json().catch(() => ({}))
    busy = false
    if (!r.ok) { error = d.error || 'Failed'; return }
    notice = editing
      ? `Updated ${editing}.`
      : `Created ${d.email}${d.password ? ' · initial password: ' + d.password : ''}`
    await load()
    if (!editing) startAdd()       // keep adding more
    else { editing = null; tab = 'list' }
  }

  async function del(email) {
    if (!(await confirmDialog({
      title: `Delete ${email}?`,
      message: 'Their panel access is revoked immediately. The user record is removed; any sites they were scoped to remain.',
      confirmText: 'Delete', danger: true,
    }))) return
    const r = await apiFetch('/api/admin/users/' + encodeURIComponent(email), { method: 'DELETE' })
    if (r.ok) load()
    else { const d = await r.json().catch(() => ({})); error = d.error || 'Failed' }
  }

  function scopeSummary(u) {
    if (!u.sitesScope) return 'All sites'
    try {
      const arr = JSON.parse(u.sitesScope)
      if (!Array.isArray(arr) || arr.includes('*')) return 'All sites'
      if (arr.length === 0) return 'No sites'
      if (arr.length === 1) return arr[0]
      return `${arr.length} sites`
    } catch { return 'All sites' }
  }

  const pickedCount = $derived(Object.values(scopedSites).filter(Boolean).length)
</script>

<div class="wrap fade">
  <button type="button" class="back" onclick={() => go('sites')}>
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M15 18l-6-6 6-6"/></svg> Back to Sites
  </button>
  <div class="ph"><div><h1>Users</h1><div class="sub">Panel accounts, roles &amp; per-site access</div></div></div>

  <!-- Tab bar: List · Add · Roles & Permissions. Pill-rail matches site tabs. -->
  <div class="tabrail" role="tablist" aria-label="Users sections">
    <button type="button" role="tab" aria-selected={tab === 'list'} class:active={tab === 'list'} onclick={() => { tab = 'list'; editing = null }}>
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true"><path d="M3 6h18M3 12h18M3 18h18"/></svg>
      Users <span class="count">{users.length}</span>
    </button>
    <button type="button" role="tab" aria-selected={tab === 'add'} class:active={tab === 'add'} onclick={startAdd}>
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true"><path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M19 8v6M22 11h-6"/></svg>
      {editing ? `Edit ${editing}` : 'Add User'}
    </button>
    <button type="button" role="tab" aria-selected={tab === 'roles'} class:active={tab === 'roles'} onclick={() => tab = 'roles'}>
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true"><path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/></svg>
      Roles &amp; Permissions
    </button>
  </div>

  {#if tab === 'list'}
    <div class="card fade">
      {#if users.length === 0}<div class="empty">No users yet — click <b>Add User</b>.</div>
      {:else}
        <table>
          <thead><tr><th>Email</th><th>Role</th><th>Scope</th><th>2FA</th><th></th></tr></thead>
          <tbody>
            {#each users as u}
              <tr>
                <td><span class="mono">{u.email}</span></td>
                <td><span class="role-chip role-{u.role}">{roleLabel[u.role] || u.role}</span></td>
                <td><span class="mono" style="font-size:12px;color:var(--txt-2)">{scopeSummary(u)}</span></td>
                <td><span class="status"><span class="sdot {u.mfaEnabled ? 's-up' : 's-down'}"></span>{u.mfaEnabled ? 'On' : 'Off'}</span></td>
                <td style="text-align:right">
                  <button type="button" class="manage" onclick={() => edit(u)}>Edit</button>
                  <button type="button" class="manage" onclick={() => del(u.email)}>Delete</button>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    </div>

  {:else if tab === 'add'}
    <!-- Two-column add/edit layout: identity on the left, permissions + site
         scope on the right. Stacks below 920px so the perm matrix stays usable. -->
    <div class="user-grid fade">
      <div class="card">
        <div class="section-h"><div>
          <h3>{editing ? 'Edit account' : 'New account'}</h3>
          <p>{editing ? 'Update role, scope, and permissions for this user.' : 'Set login, role, and which sites they can access.'}</p>
        </div>
          {#if editing}<button class="btn btn-ghost" style="padding:7px 13px" onclick={() => { editing = null; tab = 'list' }}>Cancel</button>{/if}
        </div>
        <div class="section-b">
          {#if !editing}
            <div class="field"><label>
              <span class="label-text">Email</span>
              <input class="input" bind:value={f.email} placeholder="user@example.com" autocomplete="off">
            </label></div>
            <div class="field"><label>
              <span class="label-text">Password <span class="hint">leave blank to auto-generate one (shown once after save)</span></span>
              <input class="input" type="text" bind:value={f.password} autocomplete="new-password">
            </label></div>
          {:else}
            <div class="field"><label>
              <span class="label-text">Email</span>
              <input class="input" value={editing} disabled>
            </label></div>
          {/if}

          <div class="field"><label>
            <span class="label-text">Role</span>
            <select class="select ui" bind:value={f.role} onchange={onRole}>
              <option value="ROLE_USER">User — read-only</option>
              <option value="ROLE_SITE_MANAGER">Site Manager — full CRUD on assigned sites</option>
              <option value="ROLE_ADMIN">Admin — full access, all sites</option>
            </select>
          </label></div>
          <p class="role-blurb">{roleHint[f.role]}</p>

          <div class="form-actions">
            <button class="btn btn-primary" onclick={save} disabled={busy}>{editing ? 'Save changes' : 'Add user'}</button>
          </div>
          {#if notice}<div class="note" style="margin-top:6px"><div>{notice}</div></div>{/if}
          {#if error}<div class="login-err" style="margin-top:6px">{error}</div>{/if}
        </div>
      </div>

      <div class="card">
        <div class="section-h"><div>
          <h3>Site assignment</h3>
          <p>Which sites this user can see and act on.</p>
        </div></div>
        <div class="section-b">
          {#if f.role === 'ROLE_ADMIN'}
            <div class="note"><div>Admins have access to every site by default. Switch the role to scope this.</div></div>
          {:else}
            <div class="scope-pick">
              <label class="scope-opt" class:active={allSitesScope}>
                <input type="radio" name="scope" checked={allSitesScope} onchange={() => allSitesScope = true}>
                <div><b>All sites</b><span>Including any sites added in the future.</span></div>
              </label>
              <label class="scope-opt" class:active={!allSitesScope}>
                <input type="radio" name="scope" checked={!allSitesScope} onchange={() => allSitesScope = false}>
                <div><b>Specific sites</b><span>{pickedCount} of {sites.length} selected.</span></div>
              </label>
            </div>
            {#if !allSitesScope}
              {#if sites.length === 0}
                <div class="empty" style="margin-top:14px">No sites exist yet — create one first.</div>
              {:else}
                <div class="site-pick">
                  {#each sites as s}
                    <label class="site-row" class:on={!!scopedSites[s.domain]}>
                      <input type="checkbox" bind:checked={scopedSites[s.domain]}>
                      <span class="site-ic brand">{#if brandIcons[s.type]}{@html brandIcons[s.type]}{:else}{s.ic || '◆'}{/if}</span>
                      <span class="mono">{s.domain}</span>
                      <span class="site-meta">{s.user}</span>
                    </label>
                  {/each}
                </div>
              {/if}
            {/if}
          {/if}
        </div>

        {#if f.role !== 'ROLE_ADMIN'}
          <div class="section-h" style="border-top:1px solid var(--line)"><div>
            <h3>Permissions</h3>
            <p>Role sets defaults — toggle CRUD bits per resource to fine-tune.</p>
          </div></div>
          <div class="section-b" style="padding-top:6px">
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
          </div>
        {/if}
      </div>
    </div>

  {:else if tab === 'roles'}
    <!-- v0.2.21: role permissions are now editable defaults (was read-only).
         ROLE_ADMIN is intentionally locked — admins always have everything,
         a property of the role rather than a tunable. SITE_MANAGER and USER
         can be customised; changes apply to NEW users + existing users whose
         per-user permissions haven't been overridden in the user form. -->
    <div class="role-cards fade">
      {#if rolesMsg}<div class="card" style="grid-column:1/-1;padding:12px 16px;background:var(--surface-1)"><span class="mono" style="color:var(--txt)">{rolesMsg}</span></div>{/if}
      {#each ['ROLE_ADMIN', 'ROLE_SITE_MANAGER', 'ROLE_USER'] as role}
        <div class="card role-card">
          <div class="section-h">
            <div>
              <h3>
                <span class="role-chip role-{role}">{roleLabel[role]}</span>
                {#if roles[role]?.customized}<span class="custom-pill">customised</span>{/if}
              </h3>
              <p>{roleHint[role]}</p>
            </div>
            {#if role !== 'ROLE_ADMIN'}
              <div style="display:flex;gap:6px;flex-wrap:wrap">
                {#if roles[role]?.customized}
                  <button class="btn btn-ghost" style="padding:6px 10px" onclick={() => resetRole(role)} title="Reset to compiled defaults">Reset</button>
                {/if}
                <button class="btn btn-primary" style="padding:6px 12px" onclick={() => saveRole(role)}
                        disabled={!rolesDirty[role] || rolesBusy[role]}>
                  {rolesBusy[role] ? 'Saving…' : 'Save'}
                </button>
              </div>
            {/if}
          </div>
          <div class="section-b" style="padding-top:6px">
            <table class="perm-grid">
              <thead><tr><th>Resource</th>{#each ACT as a}<th style="text-align:center;text-transform:capitalize">{a}</th>{/each}</tr></thead>
              <tbody>
                {#each RES as r}
                  {@const p = roles[role]?.permissions?.[r] || { create:false, read:false, update:false, delete:false }}
                  <tr><td><span class="mono">{r}</span></td>
                    {#each ACT as a}
                      <td style="text-align:center;vertical-align:middle">
                        {#if role === 'ROLE_ADMIN'}
                          <span class="perm-dot on" aria-label="always allowed"></span>
                        {:else}
                          <button type="button" role="switch" aria-checked={!!p[a]} aria-label="{r} {a}"
                                  class="toggle toggle-sm" class:on={!!p[a]}
                                  onclick={() => toggleRolePerm(role, r, a)}></button>
                        {/if}
                      </td>
                    {/each}
                  </tr>
                {/each}
              </tbody>
            </table>
            {#if role === 'ROLE_ADMIN'}
              <p style="margin:10px 6px 0;font-size:12px;color:var(--txt-3)">Admins always have full access — the matrix is shown for reference, not for editing.</p>
            {/if}
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>

<style>
  /* Local styles for the Users page — kept inline so app.css stays focused on
     shared primitives. */
  .tabrail{display:flex;gap:4px;background:var(--surface-1);border:1px solid var(--line);border-radius:var(--radius-pill);padding:4px;margin-bottom:18px;width:fit-content;flex-wrap:wrap}
  .tabrail button{display:inline-flex;align-items:center;gap:8px;padding:7px 14px;border-radius:var(--radius-pill);font-size:13px;font-weight:500;color:var(--txt-2);transition:.1s;background:transparent;border:none;cursor:pointer}
  .tabrail button:hover{color:var(--txt)}
  .tabrail button.active{background:var(--solid-bg);color:var(--solid-fg)}
  .tabrail button svg{width:14px;height:14px}
  .tabrail .count{display:inline-flex;align-items:center;justify-content:center;min-width:18px;height:18px;padding:0 5px;border-radius:9px;font-size:11px;font-weight:600;background:var(--surface-2);color:var(--txt-2)}
  .tabrail button.active .count{background:color-mix(in srgb, var(--solid-fg) 22%, transparent);color:var(--solid-fg)}

  /* Pro two-column layout for the Add/Edit screen. Stacks under 920px. */
  .user-grid{display:grid;grid-template-columns:minmax(0,360px) minmax(0,1fr);gap:18px;align-items:start}
  @media (max-width:920px){ .user-grid{grid-template-columns:1fr} }

  /* v0.2.27: denser permission matrix — was 9×12 px padding which made
     8 resources × 4 actions feel sprawling. New padding pairs with the
     shrunk .toggle-sm so the whole grid drops in height significantly. */
  .perm-grid{width:100%;font-size:12.5px}
  .perm-grid td, .perm-grid th{padding:5px 10px}
  .perm-grid thead th{font-size:10.5px;text-transform:uppercase;letter-spacing:.05em;color:var(--txt-3);font-weight:600;padding-bottom:2px}
  .perm-grid tbody td:first-child{color:var(--txt);font-weight:500}
  .perm-grid td:not(:first-child){text-align:center}
  .perm-grid .toggle{margin:0 auto}
  /* Hover row highlight makes it obvious which resource you're toggling. */
  .perm-grid tbody tr:hover{background:var(--surface-1)}

  .role-blurb{margin:-6px 0 14px;padding:10px 14px;border-radius:8px;background:var(--surface-1);border:1px solid var(--line);font-size:12.5px;color:var(--txt-2);line-height:1.5}

  /* Role chips give a fast visual marker in the list and the matrix headers. */
  .role-chip{display:inline-flex;align-items:center;padding:3px 10px;border-radius:var(--radius-pill);font-size:11.5px;font-weight:600;letter-spacing:.01em}
  .role-chip.role-ROLE_ADMIN{background:color-mix(in srgb, var(--down) 14%, var(--surface-1));color:var(--down);border:1px solid color-mix(in srgb, var(--down) 28%, transparent)}
  .role-chip.role-ROLE_SITE_MANAGER{background:color-mix(in srgb, var(--info) 14%, var(--surface-1));color:var(--info);border:1px solid color-mix(in srgb, var(--info) 28%, transparent)}
  .role-chip.role-ROLE_USER{background:var(--surface-2);color:var(--txt-2);border:1px solid var(--line)}

  /* Scope picker: two radio cards, then the site checklist if "Specific" is chosen. */
  .scope-pick{display:grid;grid-template-columns:1fr 1fr;gap:10px;margin-bottom:6px}
  @media (max-width:560px){ .scope-pick{grid-template-columns:1fr} }
  .scope-opt{display:flex;gap:10px;padding:12px 14px;border:1.5px solid var(--line);border-radius:10px;cursor:pointer;transition:.1s;background:var(--surface-0)}
  .scope-opt:hover{border-color:var(--line-2);background:var(--surface-1)}
  .scope-opt.active{border-color:var(--aura);background:var(--aura-glow)}
  .scope-opt input{margin-top:3px;accent-color:var(--aura-strong)}
  .scope-opt b{display:block;font-size:13.5px;font-weight:600;color:var(--txt);margin-bottom:2px}
  .scope-opt span{display:block;font-size:12px;color:var(--txt-3)}

  .site-pick{margin-top:14px;border:1px solid var(--line);border-radius:10px;overflow:hidden;max-height:340px;overflow-y:auto}
  .site-row{display:flex;align-items:center;gap:12px;padding:9px 14px;border-bottom:1px solid var(--line);cursor:pointer;transition:.08s}
  .site-row:last-child{border-bottom:none}
  .site-row:hover{background:var(--surface-1)}
  .site-row.on{background:var(--aura-glow)}
  .site-row input{accent-color:var(--aura-strong);width:15px;height:15px}
  .site-row .site-ic{width:24px;height:24px;border-radius:6px;display:grid;place-items:center;background:var(--surface-2);font-size:11px}
  .site-row .mono{flex:1;color:var(--txt);font-size:13px}
  .site-row .site-meta{color:var(--txt-3);font-size:11.5px;font-family:var(--fs-mono)}

  /* Read-only role docs: 3 cards in a row that stack on narrow viewports. */
  .role-cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(310px,1fr));gap:18px}

  /* Static yes/no dot for the role matrix. */
  .perm-dot{display:inline-block;width:8px;height:8px;border-radius:50%;background:var(--surface-2);border:1px solid var(--line)}
  .perm-dot.on{background:var(--up);border-color:var(--up)}
  /* v0.2.21: "customised" pill next to a role title — signals admin override. */
  .custom-pill{display:inline-flex;align-items:center;margin-left:8px;padding:2px 8px;border-radius:var(--radius-pill);font-size:10.5px;font-weight:600;letter-spacing:.04em;text-transform:uppercase;background:color-mix(in srgb, var(--warn) 16%, var(--surface-1));color:var(--warn);border:1px solid color-mix(in srgb, var(--warn) 32%, transparent)}
</style>

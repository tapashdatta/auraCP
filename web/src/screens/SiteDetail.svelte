<script>
  import { go, ui } from '../lib/store.svelte.js'
  import { detailTabs } from '../lib/data.js'
  import { apiFetch } from '../lib/api.js'

  const site = ui.site || { domain: '', user: '', app: '', node: null, root: '' }
  let active = $state('settings')

  // live data per tab
  let dbs = $state([])
  let cron = $state([])
  let logs = $state([])
  let logKind = $state('access')
  let files = $state([])
  let filePath = $state('')
  let backups = $state([])
  let busy = $state(false)
  let notice = $state('')

  // new-resource form models
  let newDb = $state({ engine: 'mariadb', name: '', user: '' })
  let newCron = $state({ schedule: '', command: '' })
  let config = $state({})
  let sslStatus = $state(null)
  let sshUsers = $state([])
  let nodeRuntimes = $state([])
  let nodePick = $state(site.node || 'default')
  let newSSH = $state({ username: '', type: 'sftp', password: '' })
  let basicAuth = $state({ user: '', password: '' })

  const base = $derived(`/api/sites/${encodeURIComponent(site.domain)}`)
  const isOn = (k) => config[k] === 'true'

  async function load(tab) {
    notice = ''
    if (tab === 'databases') dbs = await getJSON(`${base}/databases`, [])
    else if (tab === 'cron') cron = await getJSON(`${base}/cron`, [])
    else if (tab === 'logs') logs = (await getJSON(`${base}/logs?kind=${logKind}`, { lines: [] })).lines
    else if (tab === 'files') files = (await getJSON(`${base}/files?path=${encodeURIComponent(filePath)}`, { entries: [] })).entries
    else if (tab === 'settings') {
      backups = await getJSON(`${base}/backups`, [])
      if (site.type === 'nodejs') nodeRuntimes = await getJSON('/api/instance/node-versions', [])
    }
    else if (tab === 'cache' || tab === 'ssl' || tab === 'security') config = await getJSON(`${base}/config`, {})
    else if (tab === 'sshftp') sshUsers = await getJSON(`${base}/ssh-users`, [])
    if (tab === 'ssl') sslStatus = await getJSON(`${base}/ssl`, null)
  }

  async function setConfig(patch) {
    busy = true
    await apiFetch(`${base}/config`, { method: 'PATCH', body: JSON.stringify(patch) })
    busy = false
    config = await getJSON(`${base}/config`, {})
  }
  function toggleConfig(k) { setConfig({ [k]: isOn(k) ? 'false' : 'true' }) }
  async function saveBasicAuth() {
    await setConfig({ basic_auth: 'true', basic_auth_user: basicAuth.user, basic_auth_password: basicAuth.password })
    basicAuth = { user: '', password: '' }
  }
  async function addSSH() {
    busy = true
    const r = await apiFetch(`${base}/ssh-users`, { method: 'POST', body: JSON.stringify(newSSH) })
    const d = await r.json().catch(() => ({}))
    busy = false
    if (!r.ok) { notice = d.error || 'Failed'; return }
    notice = `Created ${d.username}. Password: ${d.password}`
    newSSH = { username: '', type: 'sftp', password: '' }
    load('sshftp')
  }
  async function delSSH(username) {
    await apiFetch(`${base}/ssh-users/${encodeURIComponent(username)}`, { method: 'DELETE' })
    load('sshftp')
  }
  async function getJSON(url, fallback) {
    try { const r = await apiFetch(url); return r.ok ? await r.json() : fallback } catch { return fallback }
  }
  function setTab(t) { active = t; load(t) }
  $effect(() => { load('settings') })

  async function addDb() {
    busy = true
    const r = await apiFetch(`${base}/databases`, { method: 'POST', body: JSON.stringify(newDb) })
    const d = await r.json().catch(() => ({}))
    busy = false
    if (!r.ok) { notice = d.error || 'Failed'; return }
    notice = `Created ${d.name}. Password: ${d.password}`
    newDb = { engine: 'mariadb', name: '', user: '' }
    load('databases')
  }
  async function addCron() {
    busy = true
    const r = await apiFetch(`${base}/cron`, { method: 'POST', body: JSON.stringify(newCron) })
    busy = false
    if (r.ok) { newCron = { schedule: '', command: '' }; load('cron') }
  }
  async function delCron(id) { await apiFetch(`${base}/cron/${id}`, { method: 'DELETE' }); load('cron') }
  async function makeBackup() {
    busy = true; await apiFetch(`${base}/backups`, { method: 'POST' }); busy = false; load('settings')
  }
  async function saveNodeVersion() {
    busy = true
    const r = await apiFetch(`${base}/node-version`, { method: 'PUT', body: JSON.stringify({ version: nodePick }) })
    const d = await r.json().catch(() => ({}))
    busy = false
    notice = r.ok ? `Site now runs on Node ${d.version}.` : (d.error || 'Failed')
  }
  function openDir(name) { filePath = filePath ? `${filePath}/${name}` : name; load('files') }
  function upDir() { filePath = filePath.split('/').slice(0, -1).join('/'); load('files') }
  function setLogKind(k) { logKind = k; load('logs') }
  function fmtSize(n) { return n > 1<<20 ? (n/(1<<20)).toFixed(1)+' MB' : (n/1024).toFixed(1)+' KB' }
</script>

<div class="wrap fade">
  <span class="back" onclick={() => go('sites')}>
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M15 18l-6-6 6-6"/></svg> Back to Sites
  </span>

  <div class="site-head">
    <div class="fav">{site.ic || '◆'}</div>
    <div>
      <h1>{site.domain}</h1>
      <div class="status" style="margin-top:4px"><span class="sdot s-{site.status || 'up'}"></span>{site.statusText || 'Online'}</div>
    </div>
  </div>

  <div class="site-meta">
    <div class="m"><span class="k">App</span><span class="v">{site.app}</span></div>
    <div class="sep"></div>
    <div class="m"><span class="k">Site User</span><span class="v">{site.user}</span></div>
    {#if site.node}<div class="sep"></div><div class="m"><span class="k">Node</span><span class="v">v{site.node} LTS</span></div>{/if}
    <div class="sep"></div>
    <div class="m"><span class="k">Document Root</span><span class="v">{site.root}</span></div>
  </div>

  <div class="tabs">
    {#each detailTabs as t}
      <span class="tab" class:active={active === t.id} onclick={() => setTab(t.id)}>{t.label}</span>
    {/each}
  </div>

  {#if active === 'settings'}
    <div class="fade">
      <div class="section"><div class="section-h"><div><h3>General</h3><p>Core configuration</p></div></div><div class="section-b" style="padding-top:4px">
        <div class="kv"><span class="k">Domain</span><span class="v">{site.domain}</span></div>
        <div class="kv"><span class="k">Application</span><span class="v">{site.app}</span></div>
        <div class="kv"><span class="k">Document root</span><span class="v">{site.root}</span></div>
        <div class="kv"><span class="k">Force HTTPS redirect</span><span class="v">enabled (auto)</span></div>
        <div class="kv"><span class="k">HTTP/3 (QUIC)</span><span class="v">enabled (auto)</span></div>
      </div></div>
      {#if site.type === 'nodejs'}
        <div class="section"><div class="section-h"><div><h3>Node.js runtime</h3>
          <p>Pin this site to a specific Node version. Manage installed versions in <b>Instance → Node.js Runtimes</b>.</p></div></div>
          <div class="section-b">
            <div class="kv"><span class="k">Current</span><span class="v">{site.node || 'default'}</span></div>
            <div class="two">
              <div class="field"><label>Use Node version</label>
                <select class="select ui" bind:value={nodePick}>
                  <option value="default">default (auracp-managed)</option>
                  {#each nodeRuntimes as n}<option value={n.version}>{n.version}{n.isDefault ? ' (default)' : ''}</option>{/each}
                </select></div>
            </div>
            <button class="btn btn-primary" onclick={saveNodeVersion} disabled={busy}>Apply &amp; restart backend</button>
          </div>
        </div>
      {/if}
      <div class="section"><div class="section-h"><div><h3>Backups</h3><p>Local site archive (document root + databases)</p></div>
        <button class="btn btn-primary" style="padding:8px 14px" onclick={makeBackup} disabled={busy}>{busy ? 'Working…' : 'Create Backup'}</button></div>
        {#if backups.length === 0}
          <div class="empty">No backups yet.</div>
        {:else}
          <table><thead><tr><th>Created</th><th>Kind</th><th>Size</th><th>Path</th></tr></thead><tbody>
            {#each backups as b}
              <tr><td><span class="mono">{b.createdAt}</span></td><td>{b.kind}</td><td><span class="mono">{fmtSize(b.size)}</span></td><td><span class="mono" style="color:var(--txt-3)">{b.path}</span></td></tr>
            {/each}
          </tbody></table>
        {/if}
      </div>
    </div>

  {:else if active === 'vhost'}
    <div class="section fade"><div class="section-h"><div><h3>Caddyfile</h3><p>Auto-generated; reloads on save</p></div></div><div class="section-b">
      <div class="code">{site.domain} {'{'}
  encode zstd br gzip
  <span class="c"># automatic HTTPS via Let's Encrypt / Cloudflare</span>
  root * {site.root}
  log {'{'} output file /home/{site.user}/logs/access.log {'}'}
{'}'}</div>
    </div></div>

  {:else if active === 'databases'}
    <div class="fade">
      <div class="section"><div class="section-h"><div><h3>Databases</h3><p>Choose MariaDB or PostgreSQL per database</p></div></div>
        {#if dbs.length === 0}<div class="empty">No databases yet.</div>
        {:else}
          <table><thead><tr><th>Name</th><th>Engine</th><th>User</th></tr></thead><tbody>
            {#each dbs as d}
              <tr><td><span class="mono">{d.name}</span></td>
                <td><span class="pill-eng {d.engine === 'postgres' ? 'eng-pg' : 'eng-maria'}">{d.engine === 'postgres' ? '⬢ PostgreSQL' : '⬡ MariaDB'}</span></td>
                <td><span class="mono" style="color:var(--txt-2)">{d.user}</span></td></tr>
            {/each}
          </tbody></table>
        {/if}
        <div class="section-b" style="border-top:1px solid var(--line)">
          <div class="two">
            <div class="field"><label>Engine</label><select class="select ui" bind:value={newDb.engine}><option value="mariadb">MariaDB</option><option value="postgres">PostgreSQL</option></select></div>
            <div class="field"><label>Database name</label><input class="input" bind:value={newDb.name} placeholder="app_db"></div>
          </div>
          <div class="field"><label>Database user</label><input class="input" bind:value={newDb.user} placeholder="app_user"></div>
          <button class="btn btn-primary" onclick={addDb} disabled={busy || !newDb.name || !newDb.user}>Add Database</button>
          {#if notice}<div class="note" style="margin-top:12px"><div>{notice}</div></div>{/if}
        </div>
      </div>
    </div>

  {:else if active === 'cache'}
    <div class="section fade"><div class="section-h"><div><h3>Cache</h3><p>Souin full-page cache (in Caddy)</p></div></div><div class="section-b" style="padding-top:4px">
      <div class="kv"><span class="k">Full-page cache</span><div class="toggle" class:on={isOn('cache')} onclick={() => toggleConfig('cache')}></div></div>
      <div class="kv"><span class="k">Default TTL</span><span class="v">{config.cache_ttl || '600s'}</span></div>
    </div></div>

  {:else if active === 'ssl'}
    <div class="section fade"><div class="section-h"><div><h3>SSL/TLS Certificate</h3><p>Managed automatically by Caddy (Let's Encrypt)</p></div>
      {#if sslStatus}<span class="status"><span class="sdot {sslStatus.status === 'active' ? 's-up' : sslStatus.status === 'pending' ? 's-warn' : 's-down'}"></span>{sslStatus.status}</span>{/if}</div>
      <div class="section-b" style="padding-top:4px">
        {#if sslStatus && sslStatus.status === 'active'}
          <div class="kv"><span class="k">Issuer</span><span class="v">{sslStatus.issuer || '—'}</span></div>
          <div class="kv"><span class="k">Domains</span><span class="v">{(sslStatus.domains || []).join(', ') || '—'}</span></div>
          <div class="kv"><span class="k">Expires</span><span class="v">{sslStatus.expires ? new Date(sslStatus.expires).toLocaleString() : '—'}</span></div>
        {:else}
          <div class="kv"><span class="k">Status</span><span class="v">{sslStatus?.message || 'checking…'}</span></div>
          <div class="kv"><span class="k">Provider</span><span class="v">Let's Encrypt (auto)</span></div>
        {/if}
        <div class="kv"><span class="k">Cloudflare DNS-01 (wildcard)</span><div class="toggle" class:on={isOn('cloudflare_dns')} onclick={() => toggleConfig('cloudflare_dns')}></div></div>
        <div class="note" style="margin-top:6px"><div>DNS-01 requires a Cloudflare API token under <b>Instance → Cloudflare</b>.</div></div>
      </div></div>

  {:else if active === 'security'}
    <div class="section fade"><div class="section-h"><div><h3>Security</h3><p>Access controls</p></div></div><div class="section-b" style="padding-top:4px">
      <div class="kv"><span class="k">Basic authentication</span><div class="toggle" class:on={isOn('basic_auth')} onclick={() => toggleConfig('basic_auth')}></div></div>
      {#if isOn('basic_auth')}
        <div class="two" style="margin-top:8px">
          <div class="field"><label>Username</label><input class="input" bind:value={basicAuth.user}></div>
          <div class="field"><label>Password</label><input class="input" type="password" bind:value={basicAuth.password}></div>
        </div>
        <button class="btn btn-ghost" onclick={saveBasicAuth} disabled={busy || !basicAuth.user || !basicAuth.password}>Set credentials</button>
      {/if}
      <div class="kv"><span class="k">Block bad bots</span><div class="toggle" class:on={isOn('block_bots')} onclick={() => toggleConfig('block_bots')}></div></div>
    </div></div>

  {:else if active === 'sshftp'}
    <div class="section fade"><div class="section-h"><div><h3>SSH / FTP Users</h3><p>Chroot-jailed to the site home</p></div></div>
      <table><thead><tr><th>User</th><th>Type</th><th></th></tr></thead><tbody>
        <tr><td><span class="mono">{site.user}</span></td><td><span class="badge b-node">owner · SSH+SFTP</span></td><td></td></tr>
        {#each sshUsers as u}
          <tr><td><span class="mono">{u.username}</span></td><td><span class="badge b-proxy">{u.type === 'ssh' ? 'SSH + SFTP' : 'SFTP only'}</span></td>
            <td style="text-align:right"><span class="manage" onclick={() => delSSH(u.username)}>Delete</span></td></tr>
        {/each}
      </tbody></table>
      <div class="section-b" style="border-top:1px solid var(--line)">
        <div class="two">
          <div class="field"><label>Username</label><input class="input" bind:value={newSSH.username} placeholder="editor"></div>
          <div class="field"><label>Access</label><select class="select ui" bind:value={newSSH.type}><option value="sftp">SFTP only</option><option value="ssh">SSH + SFTP</option></select></div>
        </div>
        <div class="field"><label>Password <span class="hint">blank = auto-generate</span></label><input class="input" bind:value={newSSH.password}></div>
        <button class="btn btn-primary" onclick={addSSH} disabled={busy || !newSSH.username}>Add User</button>
        {#if notice}<div class="note" style="margin-top:12px"><div>{notice}</div></div>{/if}
      </div>
    </div>

  {:else if active === 'files'}
    <div class="section fade"><div class="section-h"><div><h3>File Manager</h3><p class="mono">{site.root}{filePath ? '/' + filePath : ''}</p></div>
      {#if filePath}<button class="btn btn-ghost" style="padding:7px 13px" onclick={upDir}>↑ Up</button>{/if}</div>
      <div class="section-b" style="padding:0">
        {#if files.length === 0}<div class="empty">Empty.</div>
        {:else}
          {#each files as f}
            <div class="kv" style="padding:12px 18px">
              <span class="k" style="cursor:{f.dir ? 'pointer' : 'default'}" onclick={() => f.dir && openDir(f.name)}>{f.dir ? '📁' : '📄'} {f.name}</span>
              <span class="v" style="color:var(--txt-3)">{f.mode} · {fmtSize(f.size)}</span>
            </div>
          {/each}
        {/if}
      </div>
    </div>

  {:else if active === 'cron'}
    <div class="fade">
      <div class="section"><div class="section-h"><div><h3>Cron Jobs</h3><p>Run as {site.user}</p></div></div>
        {#if cron.length === 0}<div class="empty">No cron jobs.</div>
        {:else}
          <table><thead><tr><th>Schedule</th><th>Command</th><th></th></tr></thead><tbody>
            {#each cron as c}
              <tr><td><span class="mono" style="color:var(--aura-strong)">{c.schedule}</span></td><td><span class="mono" style="color:var(--txt-2)">{c.command}</span></td><td style="text-align:right"><span class="manage" onclick={() => delCron(c.id)}>Delete</span></td></tr>
            {/each}
          </tbody></table>
        {/if}
        <div class="section-b" style="border-top:1px solid var(--line)">
          <div class="two">
            <div class="field"><label>Schedule</label><input class="input" bind:value={newCron.schedule} placeholder="*/5 * * * *"></div>
            <div class="field"><label>Command</label><input class="input" bind:value={newCron.command} placeholder="php /htdocs/cron.php"></div>
          </div>
          <button class="btn btn-primary" onclick={addCron} disabled={busy || !newCron.schedule || !newCron.command}>Add Cron Job</button>
        </div>
      </div>
    </div>

  {:else if active === 'logs'}
    <div class="section fade"><div class="section-h"><div><h3>Logs</h3><p>Last lines</p></div>
      <div style="display:flex;gap:8px">
        {#each ['access','error','app'] as k}<span class="chip" class:on={logKind === k} onclick={() => setLogKind(k)}>{k}</span>{/each}
      </div></div>
      <div class="section-b">
        {#if logs.length === 0}<div class="empty">No log entries.</div>
        {:else}<div class="code">{#each logs as l}{l}
{/each}</div>{/if}
      </div>
    </div>
  {/if}
</div>

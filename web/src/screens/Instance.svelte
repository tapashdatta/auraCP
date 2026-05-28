<script>
  import { onMount } from 'svelte'
  import { go } from '../lib/store.svelte.js'
  import { apiFetch } from '../lib/api.js'

  let info = $state(null)
  let services = $state({})
  let cf = $state({ configured: false, token: '' })
  let cfMsg = $state('')
  let panel = $state({ domain: '', input: '' })
  let panelMsg = $state('')
  let remote = $state({ configured: false, type: '', kind: 's3', params: '', target: '' })
  let remoteMsg = $state('')
  let audit = $state([])
  let nodes = $state([])
  let newNode = $state({ version: '', makeDefault: false })
  let nodeMsg = $state('')
  let update = $state({ current: '', latestPlain: '', available: false, releaseUrl: '', checkedAt: '', error: '' })
  let updateMsg = $state('')
  let updateBusy = $state(false)

  async function load() {
    const r = await apiFetch('/api/instance')
    if (r.ok) info = await r.json()
    const s = await apiFetch('/api/instance/services')
    if (s.ok) services = await s.json()
    const c = await apiFetch('/api/cloudflare')
    if (c.ok) cf.configured = (await c.json()).configured
    const rb = await apiFetch('/api/backups/remote')
    if (rb.ok) { const d = await rb.json(); remote.configured = d.configured; remote.type = d.type || '' }
    const a = await apiFetch('/api/audit')
    if (a.ok) audit = await a.json()
    const pd = await apiFetch('/api/settings/panel-domain')
    if (pd.ok) { panel.domain = (await pd.json()).domain || ''; panel.input = panel.domain }
    const nv = await apiFetch('/api/instance/node-versions')
    if (nv.ok) nodes = await nv.json()
    const u = await apiFetch('/api/instance/update')
    if (u.ok) update = await u.json()
  }
  onMount(load)

  async function checkUpdate() {
    updateMsg = 'Checking GitHub…'
    const r = await apiFetch('/api/instance/update?refresh=1')
    if (r.ok) { update = await r.json(); updateMsg = update.error ? update.error : '' }
    else updateMsg = 'Check failed'
  }

  async function applyUpdate() {
    if (!update.available || updateBusy) return
    if (!confirm(`Upgrade auracpd from ${update.current} to ${update.latestPlain}? The panel will restart automatically.`)) return
    updateBusy = true
    updateMsg = `Upgrading to ${update.latestPlain}…`
    await apiFetch('/api/instance/update', { method: 'POST' })
    // Poll /api/health until the new daemon answers; then reload.
    let tries = 0
    const tick = setInterval(async () => {
      tries++
      try {
        const h = await fetch('/api/health', { cache: 'no-store' })
        if (h.ok) { clearInterval(tick); updateMsg = 'Upgraded. Reloading…'; setTimeout(() => location.reload(), 400); return }
      } catch {}
      if (tries > 60) { clearInterval(tick); updateMsg = 'Panel did not come back within 60 seconds. Check journalctl -u auracpd.'; updateBusy = false }
    }, 1000)
  }

  async function installNode() {
    nodeMsg = `Installing Node ${newNode.version}…`
    const r = await apiFetch('/api/instance/node-versions', {
      method: 'POST', body: JSON.stringify({ version: newNode.version, makeDefault: newNode.makeDefault }),
    })
    const d = await r.json().catch(() => ({}))
    nodeMsg = r.ok ? `Installed Node ${d.version}.` : (d.error || 'Failed')
    if (r.ok) { newNode.version = ''; load() }
  }
  async function makeDefaultNode(v) {
    await apiFetch(`/api/instance/node-versions/${encodeURIComponent(v)}/default`, { method: 'POST' })
    load()
  }
  async function removeNode(v) {
    if (!confirm(`Remove Node ${v}? (sites pinned to it will be rejected)`)) return
    const r = await apiFetch(`/api/instance/node-versions/${encodeURIComponent(v)}`, { method: 'DELETE' })
    const d = await r.json().catch(() => ({}))
    if (!r.ok) nodeMsg = d.error || 'Failed'
    load()
  }

  // v0.2.18: per-service restart. Backend whitelists which units the panel
  // can restart (everything we manage *except* auracpd itself). Disable the
  // button mid-flight; refresh just that row once systemctl returns.
  let restarting = $state({})    // {service: true} while in-flight
  async function restartService(name) {
    if (restarting[name]) return
    if (!confirm(`Restart ${name}? Any in-flight requests handled by this service will be dropped.`)) return
    restarting = { ...restarting, [name]: true }
    const r = await apiFetch(`/api/instance/services/${encodeURIComponent(name)}/restart`, { method: 'POST' })
    const d = await r.json().catch(() => ({}))
    restarting = { ...restarting, [name]: false }
    if (!r.ok) { alert(d.error || `Restart failed: HTTP ${r.status}`); return }
    if (d.state) services = { ...services, [name]: d.state }
  }
  // Non-restartable units (e.g. auracpd itself) get no button — we don't want
  // the UI to suggest an action that can't be taken.
  const RESTARTABLE = new Set([
    'nginx', 'php8.3-fpm', 'php8.4-fpm', 'php8.5-fpm',
    'mariadb', 'postgresql', 'redis-server', 'docker',
    'typesense-server', 'fail2ban',
  ])

  async function savePanelDomain() {
    panelMsg = 'Applying… (auracpd will obtain the Let\'s Encrypt cert)'
    const r = await apiFetch('/api/settings/panel-domain', { method: 'POST', body: JSON.stringify({ domain: panel.input.trim() }) })
    const d = await r.json().catch(() => ({}))
    if (!r.ok) { panelMsg = d.error || 'Failed'; return }
    panel.domain = d.domain
    panelMsg = d.domain ? `Panel now fronted at https://${d.domain}` : 'Reverted to IP access.'
  }

  async function saveCf() {
    const r = await apiFetch('/api/cloudflare', { method: 'POST', body: JSON.stringify({ token: cf.token }) })
    cfMsg = r.ok ? 'Saved.' : 'Failed'
    if (r.ok) { cf.token = ''; cf.configured = true }
  }

  async function saveRemote() {
    // params textarea: one "key value" per line
    const params = {}
    for (const line of remote.params.split('\n')) {
      const m = line.trim().match(/^(\S+)\s+(.+)$/)
      if (m) params[m[1]] = m[2]
    }
    const r = await apiFetch('/api/backups/remote', {
      method: 'POST', body: JSON.stringify({ type: remote.kind, params, target: remote.target }),
    })
    const d = await r.json().catch(() => ({}))
    remoteMsg = r.ok ? 'Saved.' : (d.error || 'Failed')
    if (r.ok) { remote.configured = true; remote.type = remote.kind; remote.params = '' }
  }

  const memPct = $derived(info && info.memTotalMB ? Math.round(info.memUsedMB / info.memTotalMB * 100) : 0)
  const diskPct = $derived(info && info.diskTotalGB ? Math.round(info.diskUsedGB / info.diskTotalGB * 100) : 0)
  function stateClass(v) { return v === 'active' ? 's-up' : v === 'inactive' || v === 'failed' ? 's-down' : 's-warn' }
</script>

<div class="wrap fade">
  <button type="button" class="back" onclick={() => go('sites')}><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M15 18l-6-6 6-6"/></svg> Back to Sites</button>
  <div class="ph"><div><h1>Settings</h1><div class="sub">{info?.os || ''} · {info?.hostname || ''}</div></div></div>

  {#if info}
    <div class="stats">
      <div class="stat"><div class="lbl">Load (1m)</div><div class="val">{info.load1.toFixed(2)}</div><div class="bar"><i style="width:{Math.min(100, info.load1 / (info.cores||1) * 100)}%"></i></div></div>
      <div class="stat" class:warn={memPct > 75}><div class="lbl">Memory</div><div class="val">{memPct}<small>%</small></div><div class="bar"><i style="width:{memPct}%"></i></div></div>
      <div class="stat" class:warn={diskPct > 75}><div class="lbl">Disk</div><div class="val">{info.diskUsedGB}<small>/ {info.diskTotalGB} GB</small></div><div class="bar"><i style="width:{diskPct}%"></i></div></div>
      <div class="stat"><div class="lbl">CPU cores</div><div class="val">{info.cores}</div></div>
    </div>
  {/if}

  <!-- Two-column responsive grid for the management cards. Only Recent
       Activity (full audit log table) spans both columns; everything else
       sits in one of the two columns so the page uses horizontal space. -->
  <div class="instance-grid">
    <!-- Updates card — current vs latest from GitHub Releases. -->
    <div class="card"><div class="section-h"><div><h3>auraCP Updates</h3><p>Checks GitHub Releases hourly · in-place upgrade via dpkg</p></div>
      {#if update.available}
        <span class="pill-cat warn">Update available</span>
      {:else if update.error}
        <span class="pill-cat danger">Check failed</span>
      {:else}
        <span class="pill-cat ok">Up to date</span>
      {/if}</div>
      <div class="section-b">
        <div class="kv"><span class="k">Installed</span><span class="v">{update.current || '—'}</span></div>
        <div class="kv"><span class="k">Latest release</span><span class="v">{update.latestPlain || '—'}</span></div>
        <div class="kv"><span class="k">Last checked</span><span class="v">{update.checkedAt ? new Date(update.checkedAt).toLocaleString() : 'never'}</span></div>
        <div style="display:flex;gap:8px;margin-top:14px;flex-wrap:wrap">
          <button class="btn btn-ghost" onclick={checkUpdate} disabled={updateBusy}>Check now</button>
          {#if update.available}
            <button class="btn btn-primary" onclick={applyUpdate} disabled={updateBusy}>Upgrade to {update.latestPlain}</button>
          {/if}
          {#if update.releaseUrl}
            <a class="btn btn-ghost" href={update.releaseUrl} target="_blank" rel="noopener">Release notes</a>
          {/if}
        </div>
        {#if updateMsg}<div class="note" style="margin-top:12px"><div>{updateMsg}</div></div>{/if}
      </div>
    </div>

    <!-- Cloudflare card -->
    <div class="card"><div class="section-h"><div><h3>Cloudflare</h3><p>API token for DNS-01 (wildcard SSL) &amp; cache purge</p></div>
      <span class="status"><span class="sdot {cf.configured ? 's-up' : 's-down'}"></span>{cf.configured ? 'Configured' : 'Not set'}</span></div>
      <div class="section-b">
        <div class="field"><label>
          <span class="label-text">API Token <span class="hint">stored encrypted</span></span>
          <input class="input" type="password" bind:value={cf.token} placeholder={cf.configured ? '•••••••• (replace)' : 'cloudflare API token'}>
        </label></div>
        <button class="btn btn-primary" onclick={saveCf} disabled={!cf.token}>Save Token</button>
        {#if cfMsg}<span style="margin-left:12px;color:var(--txt-2);font-size:13px">{cfMsg}</span>{/if}
      </div>
    </div>

    <!-- Panel domain card -->
    <div class="card"><div class="section-h"><div><h3>Panel Domain</h3><p>Front the panel on a domain; auracpd issues a Let's Encrypt certificate automatically.</p></div>
      <span class="status"><span class="sdot {panel.domain ? 's-up' : 's-down'}"></span>{panel.domain || 'IP:8443'}</span></div>
      <div class="section-b">
        <div class="field"><label>
          <span class="label-text">Domain / subdomain <span class="hint">point its DNS A record to this server first</span></span>
          <input class="input" style="font-family:var(--fs-ui)" bind:value={panel.input} placeholder="panel.example.com">
        </label></div>
        <button class="btn btn-primary" onclick={savePanelDomain}>Save</button>
        {#if panel.domain}<button class="btn btn-ghost" style="margin-left:8px" onclick={() => { panel.input=''; savePanelDomain() }}>Revert to IP</button>{/if}
        {#if panelMsg}<div class="note" style="margin-top:12px"><div>{panelMsg}</div></div>{/if}
      </div>
    </div>

    <!-- Remote backups card -->
    <div class="card"><div class="section-h"><div><h3>Remote Backups</h3><p>rclone destination for off-site backup copies</p></div>
      <span class="status"><span class="sdot {remote.configured ? 's-up' : 's-down'}"></span>{remote.configured ? remote.type || 'configured' : 'Not set'}</span></div>
      <div class="section-b">
        <div class="two">
          <div class="field"><label>
            <span class="label-text">Provider</span>
            <select class="select ui" bind:value={remote.kind}>
              <option value="s3">Amazon S3</option><option value="b2">Backblaze B2</option><option value="dropbox">Dropbox</option>
              <option value="drive">Google Drive</option><option value="sftp">SFTP</option><option value="swift">OpenStack Swift</option>
            </select>
          </label></div>
          <div class="field"><label>
            <span class="label-text">Target <span class="hint">remote:path</span></span>
            <input class="input" bind:value={remote.target} placeholder="auracp:my-bucket/backups">
          </label></div>
        </div>
        <div class="field"><label>
          <span class="label-text">Parameters <span class="hint">one "key value" per line (e.g. access_key_id AKIA…)</span></span>
          <textarea class="input" rows="4" style="font-family:var(--fs-mono)" bind:value={remote.params}></textarea>
        </label></div>
        <button class="btn btn-primary" onclick={saveRemote} disabled={!remote.target}>Save Remote</button>
        {#if remoteMsg}<span style="margin-left:12px;color:var(--txt-2);font-size:13px">{remoteMsg}</span>{/if}
      </div>
    </div>

    <!-- Node runtimes -->
    <div class="card"><div class="section-h"><div><h3>Node.js Runtimes</h3>
      <p>Installed under <span class="mono">/opt/auracp/node/&lt;version&gt;</span> · sites can pin to any of these</p></div></div>
      {#if nodes.length === 0}<div class="empty">No managed Node runtimes yet. Install one below.</div>
      {:else}
        <table><thead><tr><th>Version</th><th>Default</th><th></th></tr></thead><tbody>
          {#each nodes as n}
            <tr><td><span class="mono">{n.version}</span></td>
              <td><span class="status"><span class="sdot {n.isDefault ? 's-up' : 's-down'}"></span>{n.isDefault ? 'default' : '—'}</span></td>
              <td style="text-align:right">
                {#if !n.isDefault}<button type="button" class="manage" onclick={() => makeDefaultNode(n.version)}>Make default</button>{/if}
                <button type="button" class="manage" onclick={() => removeNode(n.version)}>Remove</button>
              </td></tr>
          {/each}
        </tbody></table>
      {/if}
      <div class="section-b" style="border-top:1px solid var(--line)">
        <div class="two">
          <div class="field"><label>
            <span class="label-text">Install Node version <span class="hint">e.g. 22.11.0, 20.18.0, 18.20.4</span></span>
            <input class="input" bind:value={newNode.version} placeholder="22.11.0">
          </label></div>
          <div class="field" style="display:flex;align-items:end"><label style="display:flex;gap:8px;align-items:center;font-weight:500"><input type="checkbox" bind:checked={newNode.makeDefault}> Make this the default</label></div>
        </div>
        <button class="btn btn-primary" onclick={installNode} disabled={!newNode.version}>Install</button>
        {#if nodeMsg}<span style="margin-left:12px;color:var(--txt-2);font-size:13px">{nodeMsg}</span>{/if}
      </div>
    </div>

    <!-- v0.2.18: Services moved here (was at top) so the page reads as
         "configure stuff, then status / recent activity". Per-row Restart
         issues `systemctl restart` via the panel, whitelisted server-side. -->
    <div class="card span-2"><div class="section-h"><div><h3>Services</h3><p>auraCP-managed system units · click Restart to bounce one</p></div></div>
      <table><thead><tr><th>Service</th><th>Status</th><th style="text-align:right">Actions</th></tr></thead><tbody>
        {#each Object.entries(services) as [name, state]}
          <tr>
            <td><span class="mono">{name}</span></td>
            <td><span class="status"><span class="sdot {stateClass(state)}"></span>{state || 'unknown'}</span></td>
            <td style="text-align:right">
              {#if RESTARTABLE.has(name)}
                <button type="button" class="manage" onclick={() => restartService(name)} disabled={restarting[name]}>
                  {restarting[name] ? 'Restarting…' : 'Restart'}
                </button>
              {:else}
                <span style="color:var(--txt-3);font-size:12px">—</span>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody></table>
    </div>

    <!-- Recent activity — full width; tabular event log -->
    <div class="card span-2"><div class="section-h"><div><h3>Recent Activity</h3><p>Audit log</p></div></div>
      {#if audit.length === 0}<div class="empty">No activity recorded.</div>
      {:else}
        <table><thead><tr><th>Time</th><th>Actor</th><th>Action</th><th>Target</th></tr></thead><tbody>
          {#each audit as a}
            <tr><td><span class="mono" style="color:var(--txt-3)">{a.ts}</span></td><td><span class="mono">{a.actor}</span></td>
              <td><span class="mono" style="color:var(--aura-strong)">{a.action}</span></td><td><span class="mono" style="color:var(--txt-2)">{a.target}</span></td></tr>
          {/each}
        </tbody></table>
      {/if}
    </div>
  </div>

<style>
  /* Small "did you know" hint block — neutral surface, not an alert. */
  .hint-block{margin-top:14px;padding:12px 14px;background:var(--surface-1);border:1px solid var(--line);border-left:3px solid var(--info);border-radius:8px;font-size:12.5px;color:var(--txt-2);line-height:1.55}
  .hint-block b{color:var(--txt)}
  .hint-block i{font-style:normal;color:var(--txt)}

  /* 2-column Instance dashboard. Only .span-2 cards (Recent Activity) take
     the full row; everything else lands in one of two columns. Breakpoint
     lowered to 760px so the layout splits on most laptop browsers. */
  .instance-grid{display:grid;grid-template-columns:repeat(2, minmax(0, 1fr));gap:18px;align-items:start}
  .instance-grid .span-2{grid-column:1 / -1}
  @media (max-width: 760px){
    .instance-grid{grid-template-columns:1fr}
    .instance-grid .span-2{grid-column:auto}
  }
</style>
</div>

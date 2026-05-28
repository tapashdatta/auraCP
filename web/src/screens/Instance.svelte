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
  }
  onMount(load)

  async function savePanelDomain() {
    panelMsg = 'Applying… (Caddy will obtain the certificate)'
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
  <span class="back" onclick={() => go('sites')}><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M15 18l-6-6 6-6"/></svg> Back to Sites</span>
  <div class="ph"><div><h1>Instance</h1><div class="sub">{info?.os || ''} · {info?.hostname || ''}</div></div></div>

  {#if info}
    <div class="stats">
      <div class="stat"><div class="lbl">Load (1m)</div><div class="val">{info.load1.toFixed(2)}</div><div class="bar"><i style="width:{Math.min(100, info.load1 / (info.cores||1) * 100)}%"></i></div></div>
      <div class="stat" class:warn={memPct > 75}><div class="lbl">Memory</div><div class="val">{memPct}<small>%</small></div><div class="bar"><i style="width:{memPct}%"></i></div></div>
      <div class="stat" class:warn={diskPct > 75}><div class="lbl">Disk</div><div class="val">{info.diskUsedGB}<small>/ {info.diskTotalGB} GB</small></div><div class="bar"><i style="width:{diskPct}%"></i></div></div>
      <div class="stat"><div class="lbl">CPU cores</div><div class="val">{info.cores}</div></div>
    </div>
  {/if}

  <div class="card" style="margin-bottom:18px"><div class="section-h"><div><h3>Services</h3><p>Managed system services</p></div></div>
    <table><thead><tr><th>Service</th><th>Status</th></tr></thead><tbody>
      {#each Object.entries(services) as [name, state]}
        <tr><td><span class="mono">{name}</span></td><td><span class="status"><span class="sdot {stateClass(state)}"></span>{state || 'unknown'}</span></td></tr>
      {/each}
    </tbody></table>
  </div>

  <div class="card"><div class="section-h"><div><h3>Cloudflare</h3><p>API token for DNS-01 (wildcard SSL) &amp; cache purge</p></div>
    <span class="status"><span class="sdot {cf.configured ? 's-up' : 's-down'}"></span>{cf.configured ? 'Configured' : 'Not set'}</span></div>
    <div class="section-b">
      <div class="field"><label>API Token <span class="hint">stored encrypted</span></label>
        <input class="input" type="password" bind:value={cf.token} placeholder={cf.configured ? '•••••••• (replace)' : 'cloudflare API token'}></div>
      <button class="btn btn-primary" onclick={saveCf} disabled={!cf.token}>Save Token</button>
      {#if cfMsg}<span style="margin-left:12px;color:var(--txt-2);font-size:13px">{cfMsg}</span>{/if}
    </div>
  </div>

  <div class="card" style="margin-top:18px"><div class="section-h"><div><h3>Panel Domain</h3><p>Access the panel at a domain (Caddy gets a real SSL cert; no port needed)</p></div>
    <span class="status"><span class="sdot {panel.domain ? 's-up' : 's-down'}"></span>{panel.domain || 'IP:8443'}</span></div>
    <div class="section-b">
      <div class="field"><label>Domain / subdomain <span class="hint">point its DNS A record to this server first</span></label>
        <input class="input" style="font-family:var(--fs-ui)" bind:value={panel.input} placeholder="panel.example.com"></div>
      <button class="btn btn-primary" onclick={savePanelDomain}>Save</button>
      {#if panel.domain}<button class="btn btn-ghost" style="margin-left:8px" onclick={() => { panel.input=''; savePanelDomain() }}>Revert to IP</button>{/if}
      {#if panelMsg}<div class="note" style="margin-top:12px"><div>{panelMsg}</div></div>{/if}
    </div>
  </div>

  <div class="card" style="margin-top:18px"><div class="section-h"><div><h3>Remote Backups</h3><p>rclone destination for off-site backup copies</p></div>
    <span class="status"><span class="sdot {remote.configured ? 's-up' : 's-down'}"></span>{remote.configured ? remote.type || 'configured' : 'Not set'}</span></div>
    <div class="section-b">
      <div class="two">
        <div class="field"><label>Provider</label><select class="select ui" bind:value={remote.kind}>
          <option value="s3">Amazon S3</option><option value="b2">Backblaze B2</option><option value="dropbox">Dropbox</option>
          <option value="drive">Google Drive</option><option value="sftp">SFTP</option><option value="swift">OpenStack Swift</option>
        </select></div>
        <div class="field"><label>Target <span class="hint">remote:path</span></label><input class="input" bind:value={remote.target} placeholder="auracp:my-bucket/backups"></div>
      </div>
      <div class="field"><label>Parameters <span class="hint">one "key value" per line (e.g. access_key_id AKIA…)</span></label>
        <textarea class="input" rows="4" style="font-family:var(--fs-mono)" bind:value={remote.params}></textarea></div>
      <button class="btn btn-primary" onclick={saveRemote} disabled={!remote.target}>Save Remote</button>
      {#if remoteMsg}<span style="margin-left:12px;color:var(--txt-2);font-size:13px">{remoteMsg}</span>{/if}
    </div>
  </div>

  <div class="card" style="margin-top:18px"><div class="section-h"><div><h3>Recent Activity</h3><p>Audit log</p></div></div>
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

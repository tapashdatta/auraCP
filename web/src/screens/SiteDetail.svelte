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
  function randPw() { return Math.random().toString(36).slice(2, 12) + '-' + Math.random().toString(36).slice(2, 6) }
  let newDb = $state({ engine: 'mariadb', name: '', user: '', password: randPw() })
  let newCron = $state({ schedule: '', command: '' })
  let config = $state({})
  let sslStatus = $state(null)
  let sshUsers = $state([])
  let nodeRuntimes = $state([])
  let nodePick = $state(site.node || 'default')
  let newSSH = $state({ username: '', type: 'sftp', password: '' })
  let basicAuth = $state({ user: '', password: '' })
  let vhost = $state({ content: '', path: '', loaded: false, dirty: false })
  let docRoot = $state(site.root || '')
  let docRootDirty = $state(false)

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
    else if (tab === 'vhost') {
      const v = await getJSON(`${base}/vhost`, null)
      if (v) {
        vhost = { content: v.content || '', path: v.path || '', loaded: true, dirty: false }
        if (!v.content && v.note) notice = v.note
      } else {
        // Server returned nothing — surface a clear failure instead of leaving
        // 'Loading vhost…' on screen forever.
        vhost = { content: '', path: '', loaded: true, dirty: false }
        notice = 'Could not load the vhost. Save anything in Settings to trigger a reload, or check `journalctl -u auracpd`.'
      }
    }
    else if (tab === 'cache' || tab === 'ssl' || tab === 'security') config = await getJSON(`${base}/config`, {})
    else if (tab === 'sshftp') sshUsers = await getJSON(`${base}/ssh-users`, [])
    if (tab === 'ssl') sslStatus = await getJSON(`${base}/ssl`, null)
  }

  async function saveVhost() {
    busy = true
    const r = await apiFetch(`${base}/vhost`, { method: 'PUT', body: JSON.stringify({ content: vhost.content }) })
    const d = await r.json().catch(() => ({}))
    busy = false
    if (!r.ok) { notice = d.error || 'nginx rejected the config — fix the syntax and save again.'; return }
    notice = 'Vhost saved and nginx reloaded.'
    vhost.dirty = false
  }
  async function revertVhost() {
    busy = true
    const r = await apiFetch(`${base}/vhost`, { method: 'PUT', body: JSON.stringify({ content: '' }) })
    const d = await r.json().catch(() => ({}))
    busy = false
    if (!r.ok) { notice = d.error || `Could not regenerate: ${r.status}`; return }
    notice = 'Reverted to auto-generated vhost; nginx reloaded.'
    load('vhost')
  }
  async function generateVhost() {
    // Same as revert — explicit "create now" when the file is missing entirely.
    busy = true
    const r = await apiFetch(`${base}/vhost`, { method: 'PUT', body: JSON.stringify({ content: '' }) })
    const d = await r.json().catch(() => ({}))
    busy = false
    if (!r.ok) { notice = d.error || `Could not generate: ${r.status}`; return }
    notice = 'Vhost generated and nginx reloaded.'
    load('vhost')
  }
  async function saveDocRoot() {
    busy = true
    const r = await apiFetch(`${base}`, { method: 'PATCH', body: JSON.stringify({ root: docRoot }) })
    const d = await r.json().catch(() => ({}))
    busy = false
    if (!r.ok) { notice = d.error || 'Could not save document root'; return }
    notice = 'Document root updated; nginx reloaded.'
    docRootDirty = false
    site.root = docRoot
  }

  async function setConfig(patch) {
    busy = true
    // Optimistic: flip the local state immediately so the toggle feels snappy
    // even on a slow PATCH (nginx reload can take 100-300ms).
    config = { ...config, ...patch }
    const r = await apiFetch(`${base}/config`, { method: 'PATCH', body: JSON.stringify(patch) })
    busy = false
    if (!r.ok) {
      // Roll back the optimistic update and surface the error so the operator
      // knows why the toggle didn't stick (typically: nginx -t failed because
      // an upstream is missing, or basic_auth without credentials).
      const d = await r.json().catch(() => ({}))
      notice = d.error || `Could not save: ${r.status}`
    }
    // Re-fetch authoritative state regardless of success.
    const fresh = await getJSON(`${base}/config`, null)
    if (fresh) config = fresh
  }
  function toggleConfig(k) { setConfig({ [k]: isOn(k) ? 'false' : 'true' }) }
  async function saveBasicAuth() {
    if (!basicAuth.user || !basicAuth.password) { notice = 'Username and password are required.'; return }
    notice = ''
    await setConfig({ basic_auth: 'true', basic_auth_user: basicAuth.user, basic_auth_password: basicAuth.password })
    // setConfig sets `notice` only on error. If still empty, we succeeded.
    if (!notice) notice = `Basic auth credentials saved. Visitors will now be prompted as ${basicAuth.user}.`
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
    notice = ''
    busy = true
    const r = await apiFetch(`${base}/databases`, { method: 'POST', body: JSON.stringify(newDb) })
    const d = await r.json().catch(() => ({}))
    busy = false
    if (!r.ok) {
      // Surface the actual MariaDB / PostgreSQL message verbatim — typical
      // reasons: db name reserved, user already exists, engine not running.
      notice = d.error
        ? `Could not create the database: ${d.error}`
        : `Database create failed (HTTP ${r.status}); check journalctl -u auracpd.`
      return
    }
    notice = `Created ${d.name}. Password: ${d.password} — copy it now, it's only shown once.`
    newDb = { engine: 'mariadb', name: '', user: '', password: randPw() }
    load('databases')
  }
  async function addCron() {
    busy = true
    const r = await apiFetch(`${base}/cron`, { method: 'POST', body: JSON.stringify(newCron) })
    const d = await r.json().catch(() => ({}))
    busy = false
    if (!r.ok) { notice = d.error || `Could not add cron job: ${r.status}`; return }
    notice = `Cron job added; ${site.user}'s crontab refreshed.`
    newCron = { schedule: '', command: '' }
    load('cron')
  }
  async function delCron(id) {
    const r = await apiFetch(`${base}/cron/${id}`, { method: 'DELETE' })
    const d = await r.json().catch(() => ({}))
    if (!r.ok) { notice = d.error || `Could not delete: ${r.status}`; return }
    load('cron')
  }
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
  async function togglePM2(enabled) {
    busy = true
    const r = await apiFetch(`${base}/pm2`, { method: 'PUT', body: JSON.stringify({ enabled }) })
    const d = await r.json().catch(() => ({}))
    busy = false
    notice = r.ok ? (d.enabled ? 'PM2 enabled — backend restarted via pm2-runtime.' : 'PM2 disabled — back to plain node.') : (d.error || 'Failed')
  }
  function openDir(name) { filePath = filePath ? `${filePath}/${name}` : name; load('files') }
  function upDir() { filePath = filePath.split('/').slice(0, -1).join('/'); load('files') }
  function setLogKind(k) { logKind = k; load('logs') }
  function fmtSize(n) { return n > 1<<20 ? (n/(1<<20)).toFixed(1)+' MB' : (n/1024).toFixed(1)+' KB' }

  // File-manager upload state — drag-over highlight, in-flight progress, and
  // the file input ref so the 'Upload' button can trigger it programmatically.
  let dragOver = $state(false)
  let uploadBusy = $state(false)
  let uploadMsg = $state('')
  let fileInput = $state(null)   // refs the hidden <input type=file>

  async function uploadFiles(fileList) {
    if (!fileList || fileList.length === 0) return
    uploadBusy = true
    uploadMsg = `Uploading ${fileList.length} file${fileList.length > 1 ? 's' : ''}…`
    const fd = new FormData()
    fd.append('path', filePath)
    for (const f of fileList) fd.append('files', f, f.name)
    try {
      const r = await apiFetch(`${base}/files`, { method: 'POST', body: fd })
      const d = await r.json().catch(() => ({}))
      if (!r.ok) { uploadMsg = d.error || `Upload failed: ${r.status}`; uploadBusy = false; return }
      const errs = Array.isArray(d.errors) ? d.errors.length : 0
      uploadMsg = errs > 0
        ? `Uploaded ${d.saved}; ${errs} failed: ${d.errors.join(', ')}`
        : `Uploaded ${d.saved} file${d.saved > 1 ? 's' : ''}.`
      load('files')
    } catch (e) {
      uploadMsg = 'Upload aborted: ' + (e?.message || 'unknown error')
    }
    uploadBusy = false
  }
  function onDrop(e) {
    e.preventDefault(); dragOver = false
    uploadFiles(e.dataTransfer?.files)
  }
  function onDragOver(e) { e.preventDefault(); dragOver = true }
  function onDragLeave() { dragOver = false }
  async function deleteFile(name) {
    if (!confirm(`Delete ${name}? This cannot be undone.`)) return
    const sub = filePath ? `${filePath}/${name}` : name
    const r = await apiFetch(`${base}/files?path=${encodeURIComponent(sub)}`, { method: 'DELETE' })
    const d = await r.json().catch(() => ({}))
    if (!r.ok) { uploadMsg = d.error || 'Could not delete'; return }
    uploadMsg = `Deleted ${name}.`
    load('files')
  }
  function downloadFile(name) {
    // Build the URL with credentials = sessionid cookie; same-origin so a
    // plain <a download> works for download (no CORS shenanigans).
    const sub = filePath ? `${filePath}/${name}` : name
    const a = document.createElement('a')
    a.href = `${base}/files/download?path=${encodeURIComponent(sub)}`
    a.download = name
    a.click()
  }

  // ─── v0.2.15: new file-manager surface ─────────────────────────────────
  // Breadcrumb segments derived from filePath. "" → just [], so the breadcrumb
  // renders only the "home" pip when at the docroot.
  const crumbs = $derived(filePath ? filePath.split('/').filter(Boolean) : [])
  function jumpCrumb(idx) {
    // idx=-1 → home; otherwise rebuild the path up to (and including) idx.
    filePath = idx < 0 ? '' : crumbs.slice(0, idx + 1).join('/')
    load('files')
  }

  async function mkdir() {
    const name = prompt('New folder name:')
    if (!name) return
    const r = await apiFetch(`${base}/files/mkdir`, {
      method: 'POST', body: JSON.stringify({ path: filePath, name: name.trim() })
    })
    const d = await r.json().catch(() => ({}))
    if (!r.ok) { uploadMsg = d.error || 'Could not create folder'; return }
    uploadMsg = `Created ${name}.`
    load('files')
  }

  async function touchFile() {
    const name = prompt('New file name (e.g. index.html, .env, robots.txt):')
    if (!name) return
    const r = await apiFetch(`${base}/files/touch`, {
      method: 'POST', body: JSON.stringify({ path: filePath, name: name.trim() })
    })
    const d = await r.json().catch(() => ({}))
    if (!r.ok) { uploadMsg = d.error || 'Could not create file'; return }
    uploadMsg = `Created ${name}.`
    load('files')
  }

  async function renameItem(oldName) {
    const next = prompt(`Rename ${oldName} to:`, oldName)
    if (!next || next.trim() === oldName) return
    const sub = filePath ? `${filePath}/${oldName}` : oldName
    const r = await apiFetch(`${base}/files/rename`, {
      method: 'POST', body: JSON.stringify({ path: sub, newName: next.trim() })
    })
    const d = await r.json().catch(() => ({}))
    if (!r.ok) { uploadMsg = d.error || 'Could not rename'; return }
    uploadMsg = `Renamed to ${next}.`
    load('files')
  }

  // ─── in-browser text editor ────────────────────────────────────────────
  let editor = $state({ open: false, name: '', sub: '', content: '', original: '', busy: false, err: '' })
  // Conservative extension whitelist for "open in editor"; everything else
  // downloads. Operators can always rename a file with a textual extension
  // to edit it if they really mean to.
  const TEXT_EXT = /\.(txt|md|html?|css|scss|less|js|mjs|cjs|ts|tsx|jsx|json|ya?ml|toml|ini|conf|cfg|env|env\.\w+|sh|bash|zsh|py|rb|php|go|rs|java|kt|swift|sql|xml|svg|log|csv|tsv|gitignore|htaccess|nginx|service)$/i

  function isTextish(name) {
    // No extension or a known-text extension → treat as editable.
    if (TEXT_EXT.test(name)) return true
    if (!name.includes('.')) return true
    return false
  }

  async function openEditor(name) {
    const sub = filePath ? `${filePath}/${name}` : name
    editor = { open: true, name, sub, content: '', original: '', busy: true, err: '' }
    const r = await apiFetch(`${base}/files/text?path=${encodeURIComponent(sub)}`)
    const d = await r.json().catch(() => ({}))
    if (!r.ok) {
      editor.err = d.error || 'Could not open file'
      editor.busy = false
      return
    }
    editor.content = d.content || ''
    editor.original = editor.content
    editor.busy = false
  }
  function closeEditor() {
    if (editor.content !== editor.original && !confirm('Discard unsaved changes?')) return
    editor = { open: false, name: '', sub: '', content: '', original: '', busy: false, err: '' }
  }
  async function saveEditor() {
    editor.busy = true
    editor.err = ''
    const r = await apiFetch(`${base}/files/text`, {
      method: 'PUT', body: JSON.stringify({ path: editor.sub, content: editor.content })
    })
    const d = await r.json().catch(() => ({}))
    editor.busy = false
    if (!r.ok) { editor.err = d.error || 'Save failed'; return }
    editor.original = editor.content
    uploadMsg = `Saved ${editor.name}.`
    load('files')
  }

  // Empty-state SFTP hint uses the current page's hostname — i.e. how the
  // operator reached the panel, which is also a valid SFTP host for the box.
  const sftpHost = $derived(typeof location !== 'undefined' ? location.hostname : '')
</script>

<div class="wrap fade">
  <button type="button" class="back" onclick={() => go('sites')}>
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M15 18l-6-6 6-6"/></svg> Back to Sites
  </button>

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

  <div class="tabs" role="tablist">
    {#each detailTabs as t}
      <button type="button" role="tab" aria-selected={active === t.id} class="tab" class:active={active === t.id} onclick={() => setTab(t.id)}>{t.label}</button>
    {/each}
  </div>

  {#if active === 'settings'}
    <div class="fade">
      <div class="section"><div class="section-h"><div><h3>General</h3><p>Domain, runtime, and HTTPS</p></div></div><div class="section-b" style="padding-top:4px">
        <div class="kv"><span class="k">Domain</span><span class="v">{site.domain}</span></div>
        <div class="kv"><span class="k">Application</span><span class="v">{site.app}</span></div>
        <div class="kv"><span class="k">Force HTTPS redirect</span><span class="v">enabled (auto)</span></div>
        <div class="kv"><span class="k">HTTP/2</span><span class="v">enabled (auto)</span></div>
        <div class="field" style="margin-top:14px"><label>
          <span class="label-text">Document root <span class="hint">Point at a subdirectory (e.g. <span class="mono">/home/{site.user}/htdocs/{site.domain}/public</span>) for Laravel / Statamic / Symfony</span></span>
          <div class="input-row">
            <input class="input" bind:value={docRoot} oninput={() => docRootDirty = true}>
            <button type="button" class="btn btn-ghost" onclick={saveDocRoot} disabled={!docRootDirty || busy}>Save</button>
          </div>
        </label></div>
      </div></div>
      {#if site.type === 'nodejs'}
        <div class="section"><div class="section-h"><div><h3>Node.js runtime</h3>
          <p>Pin this site to a specific Node version. Manage installed versions in <b>Instance → Node.js Runtimes</b>.</p></div></div>
          <div class="section-b">
            <div class="kv"><span class="k">Current</span><span class="v">{site.node || 'default'}</span></div>
            <div class="two">
              <div class="field"><label>
                <span class="label-text">Node version</span>
                <select class="select ui" bind:value={nodePick}>
                  <option value="default">default (auracp-managed)</option>
                  {#each nodeRuntimes as n}<option value={n.version}>{n.version}{n.isDefault ? ' (default)' : ''}</option>{/each}
                </select>
              </label></div>
            </div>
            <button class="btn btn-primary" onclick={saveNodeVersion} disabled={busy}>Apply &amp; restart backend</button>
            <div class="kv" style="margin-top:14px">
              <span class="k">Run via PM2 (pm2-runtime)</span>
              <button type="button" role="switch" aria-checked={!!site.pm2} aria-label="Toggle PM2" class="toggle" class:on={!!site.pm2} onclick={() => togglePM2(!site.pm2)}></button>
            </div>
            <span class="hint" style="display:block;margin-top:-4px">PM2 process name = the domain (<span class="mono">{site.domain}</span>). systemd unit stays <span class="mono">auracp-site-{site.domain}</span>.</span>
          </div>
        </div>
      {/if}
      <div class="section"><div class="section-h"><div><h3>Backups</h3><p>Document root + databases, stored locally</p></div>
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
    <div class="section fade"><div class="section-h"><div>
      <h3>nginx vhost</h3>
      <p>Auto-generated from your settings · validated via <span class="mono">nginx -t</span> on save</p></div>
      <span class="mono" style="color:var(--txt-3);font-size:12px">{vhost.path || ''}</span></div>
      <div class="section-b">
        {#if !vhost.loaded}
          <div class="empty">Loading vhost…</div>
        {:else if !vhost.content.trim()}
          <div class="empty">
            <p style="margin-bottom:14px"><b>No vhost on disk yet</b> at <span class="mono">{vhost.path}</span>.</p>
            <p style="margin-bottom:14px">This usually means the site was created but a later edit or service restart left the file missing. Click below to re-render from the template — nothing else is touched.</p>
            <button class="btn btn-primary" onclick={generateVhost} disabled={busy}>{busy ? 'Generating…' : 'Generate vhost now'}</button>
          </div>
        {:else}
          <textarea class="input vhost-editor" rows="22" spellcheck="false"
                    bind:value={vhost.content} oninput={() => vhost.dirty = true}></textarea>
          <div style="display:flex;gap:8px;margin-top:14px;flex-wrap:wrap">
            <button class="btn btn-primary" onclick={saveVhost} disabled={!vhost.dirty || busy || !vhost.content.trim()}>Save &amp; reload</button>
            <button class="btn btn-ghost" onclick={revertVhost} disabled={busy}>Revert to auto-generated</button>
          </div>
        {/if}
        {#if notice}<div class="note" style="margin-top:12px"><div>{notice}</div></div>{/if}
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
            <div class="field"><label>
              <span class="label-text">Engine</span>
              <select class="select ui" bind:value={newDb.engine}><option value="mariadb">MariaDB</option><option value="postgres">PostgreSQL</option></select>
            </label></div>
            <div class="field"><label>
              <span class="label-text">Database name</span>
              <input class="input" bind:value={newDb.name} placeholder="app_db">
            </label></div>
          </div>
          <div class="field"><label>
            <span class="label-text">Database user</span>
            <input class="input" bind:value={newDb.user} placeholder="app_user">
          </label></div>
          <div class="field"><label>
            <span class="label-text">Password <span class="hint">auto-generated · editable · shown after creation</span></span>
            <div class="input-row">
              <input class="input" bind:value={newDb.password}>
              <button type="button" class="gen" onclick={() => newDb.password = randPw()}>Regenerate</button>
            </div>
          </label></div>
          <button class="btn btn-primary" onclick={addDb} disabled={busy || !newDb.name || !newDb.user || !newDb.password}>Add Database</button>
          {#if notice}<div class="note" style="margin-top:12px"><div>{notice}</div></div>{/if}
        </div>
      </div>
    </div>

  {:else if active === 'cache'}
    <div class="section fade"><div class="section-h"><div><h3>Cache</h3><p>nginx fastcgi_cache / proxy_cache (per-site, opt-in)</p></div></div><div class="section-b" style="padding-top:4px">
      <div class="kv"><span class="k">Full-page cache</span><button type="button" role="switch" aria-checked={isOn('cache')} aria-label="Toggle full-page cache" class="toggle" class:on={isOn('cache')} onclick={() => toggleConfig('cache')}></button></div>
      <div class="kv"><span class="k">Default TTL</span><span class="v">{config.cache_ttl || '600s'}</span></div>
    </div></div>

  {:else if active === 'ssl'}
    <div class="section fade"><div class="section-h"><div><h3>SSL/TLS Certificate</h3><p>Issued + renewed by auracpd via <span class="mono">go-acme/lego</span>. HTTP-01 by default; Cloudflare DNS-01 below.</p></div>
      {#if sslStatus}<span class="status"><span class="sdot {sslStatus.status === 'active' ? 's-up' : sslStatus.status === 'pending' ? 's-warn' : 's-down'}"></span>{sslStatus.status}</span>{/if}</div>
      <div class="section-b" style="padding-top:4px">
        {#if sslStatus === null}
          <div class="kv"><span class="k">Status</span><span class="v">checking…</span></div>
        {:else if sslStatus.status === 'active'}
          <div class="kv"><span class="k">Issuer</span><span class="v">{sslStatus.issuer || '—'}</span></div>
          <div class="kv"><span class="k">Domains</span><span class="v">{(sslStatus.domains || []).join(', ') || '—'}</span></div>
          <div class="kv"><span class="k">Expires</span><span class="v">{sslStatus.expires ? new Date(sslStatus.expires).toLocaleString() : '—'}</span></div>
        {:else}
          <div class="kv"><span class="k">Status</span><span class="v">{sslStatus?.message || 'no certificate served yet'}</span></div>
          <div class="kv"><span class="k">Provider</span><span class="v">Let's Encrypt (auto)</span></div>
          <div class="hint" style="margin-left:0;margin-top:8px">
            Cert issuance runs in the background after a site is created.
            Watch <span class="mono">journalctl -u auracpd</span> for <span class="mono">acme: issued cert</span>.
            If your DNS goes through Cloudflare with the orange cloud, enable DNS-01 below.
          </div>
        {/if}
        <div style="margin-top:14px;display:flex;gap:8px">
          <button class="btn btn-ghost" onclick={() => load('ssl')}>Re-check now</button>
        </div>
        <div class="kv" style="margin-top:14px"><span class="k">Cloudflare DNS-01 (wildcard / proxied)</span><button type="button" role="switch" aria-checked={isOn('cloudflare_dns')} aria-label="Toggle Cloudflare DNS-01 challenge" class="toggle" class:on={isOn('cloudflare_dns')} onclick={() => toggleConfig('cloudflare_dns')}></button></div>
        <div class="hint" style="margin-left:0">Requires a Cloudflare API token under <b>Instance → Cloudflare</b>. Use this for wildcards, or when CF orange-cloud is blocking HTTP-01.</div>
      </div></div>

  {:else if active === 'security'}
    <div class="section fade"><div class="section-h"><div><h3>Security</h3><p>Access controls</p></div></div><div class="section-b" style="padding-top:4px">
      <div class="kv"><span class="k">Basic authentication</span><button type="button" role="switch" aria-checked={isOn('basic_auth')} aria-label="Toggle basic authentication" class="toggle" class:on={isOn('basic_auth')} onclick={() => toggleConfig('basic_auth')}></button></div>
      {#if isOn('basic_auth')}
        <div class="two" style="margin-top:8px">
          <div class="field"><label>
            <span class="label-text">Username</span>
            <input class="input" bind:value={basicAuth.user}>
          </label></div>
          <div class="field"><label>
            <span class="label-text">Password</span>
            <input class="input" type="password" bind:value={basicAuth.password}>
          </label></div>
        </div>
        <button class="btn btn-ghost" onclick={saveBasicAuth} disabled={busy || !basicAuth.user || !basicAuth.password}>Set credentials</button>
      {/if}
      <div class="kv"><span class="k">Block bad bots</span><button type="button" role="switch" aria-checked={isOn('block_bots')} aria-label="Toggle bot blocking" class="toggle" class:on={isOn('block_bots')} onclick={() => toggleConfig('block_bots')}></button></div>
      <div class="hint" style="margin-left:0">
        Blocks the SEO scraper set by User-Agent: <span class="mono">AhrefsBot</span>, <span class="mono">SemrushBot</span>, <span class="mono">MJ12bot</span>, <span class="mono">DotBot</span>, <span class="mono">PetalBot</span>.
        Returns <span class="mono">403</span> at the nginx layer — no PHP / app workload spent on them.
      </div>
    </div></div>

  {:else if active === 'sshftp'}
    <div class="section fade"><div class="section-h"><div><h3>SSH / FTP Users</h3><p>Chroot-jailed to the site home</p></div></div>
      <table><thead><tr><th>User</th><th>Type</th><th></th></tr></thead><tbody>
        <tr><td><span class="mono">{site.user}</span></td><td><span class="badge b-node">owner · SSH+SFTP</span></td><td></td></tr>
        {#each sshUsers as u}
          <tr><td><span class="mono">{u.username}</span></td><td><span class="badge b-proxy">{u.type === 'ssh' ? 'SSH + SFTP' : 'SFTP only'}</span></td>
            <td style="text-align:right"><button type="button" class="manage" onclick={() => delSSH(u.username)}>Delete</button></td></tr>
        {/each}
      </tbody></table>
      <div class="section-b" style="border-top:1px solid var(--line)">
        <div class="two">
          <div class="field"><label>
            <span class="label-text">Username</span>
            <input class="input" bind:value={newSSH.username} placeholder="editor">
          </label></div>
          <div class="field"><label>
            <span class="label-text">Access</span>
            <select class="select ui" bind:value={newSSH.type}><option value="sftp">SFTP only</option><option value="ssh">SSH + SFTP</option></select>
          </label></div>
        </div>
        <div class="field"><label>
          <span class="label-text">Password <span class="hint">blank = auto-generate</span></span>
          <input class="input" bind:value={newSSH.password}>
        </label></div>
        <button class="btn btn-primary" onclick={addSSH} disabled={busy || !newSSH.username}>Add User</button>
        {#if notice}<div class="note" style="margin-top:12px"><div>{notice}</div></div>{/if}
      </div>
    </div>

  {:else if active === 'files'}
    <div class="section fade" role="region" aria-label="File manager"
         ondragover={onDragOver} ondragleave={onDragLeave} ondrop={onDrop}
         class:drop-active={dragOver}>
      <div class="section-h">
        <div>
          <h3>File Manager</h3>
          <!-- Breadcrumb: clickable segments. Each jumps to that depth. -->
          <div class="crumbs">
            <button type="button" class="crumb home" onclick={() => jumpCrumb(-1)} aria-label="Document root">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true"><path d="M3 11l9-7 9 7v9a2 2 0 0 1-2 2h-4v-6h-6v6H5a2 2 0 0 1-2-2v-9z"/></svg>
            </button>
            {#each crumbs as seg, i}
              <span class="crumb-sep">/</span>
              <button type="button" class="crumb" onclick={() => jumpCrumb(i)}>{seg}</button>
            {/each}
          </div>
          <p class="mono crumb-path">{site.root}{filePath ? '/' + filePath : ''}</p>
        </div>
        <div style="display:flex;gap:8px;flex-wrap:wrap">
          <button class="btn btn-ghost" style="padding:7px 14px" onclick={mkdir} title="New folder">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true" style="width:14px;height:14px;vertical-align:-2px;margin-right:6px"><path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7z"/><path d="M12 11v6M9 14h6"/></svg>
            New Folder
          </button>
          <button class="btn btn-ghost" style="padding:7px 14px" onclick={touchFile} title="New file">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true" style="width:14px;height:14px;vertical-align:-2px;margin-right:6px"><path d="M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9z"/><path d="M14 3v6h6M12 13v6M9 16h6"/></svg>
            New File
          </button>
          <button class="btn btn-primary" style="padding:7px 14px" onclick={() => fileInput.click()} disabled={uploadBusy}>
            {uploadBusy ? 'Uploading…' : 'Upload'}
          </button>
          <input type="file" multiple bind:this={fileInput}
                 onchange={(e) => { uploadFiles(e.target.files); e.target.value = '' }}
                 style="display:none">
        </div>
      </div>

      {#if dragOver}
        <div class="drop-overlay">Drop files to upload to <span class="mono">{filePath || '/'}</span></div>
      {/if}

      <div class="section-b" style="padding:0">
        {#if files.length === 0}
          <!-- CloudPanel-style empty state with concrete SFTP details. The
               operator gets the exact host/user/port/path they need rather
               than a vague "use SFTP" hint. -->
          <div class="empty-fm">
            <div class="empty-fm-ic" aria-hidden="true">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"><path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7z"/></svg>
            </div>
            <h4>This directory is empty.</h4>
            <p>Drop files anywhere in this panel to upload, click <b>Upload</b> above, or use SFTP:</p>
            <dl class="sftp-info">
              <div><dt>Host</dt><dd class="mono">{sftpHost || site.domain}</dd></div>
              <div><dt>Port</dt><dd class="mono">22</dd></div>
              <div><dt>User</dt><dd class="mono">{site.user}</dd></div>
              <div><dt>Path</dt><dd class="mono">{site.root}{filePath ? '/' + filePath : ''}</dd></div>
            </dl>
            <p class="empty-fm-hint">No password? Add one in the <b>SSH/FTP</b> tab.</p>
          </div>
        {:else}
          {#each files as f}
            <div class="file-row-grid">
              {#if f.dir}
                <button type="button" class="file-row k" onclick={() => openDir(f.name)} title="Open folder">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" class="file-ic"><path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7z"/></svg>
                  {f.name}
                </button>
              {:else if isTextish(f.name)}
                <button type="button" class="file-row k" onclick={() => openEditor(f.name)} title="Open in editor">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" class="file-ic"><path d="M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9z"/><path d="M14 3v6h6"/></svg>
                  {f.name}
                </button>
              {:else}
                <button type="button" class="file-row k" onclick={() => downloadFile(f.name)} title="Download (binary)">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" class="file-ic"><path d="M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9z"/><path d="M14 3v6h6"/></svg>
                  {f.name}
                </button>
              {/if}
              <span class="file-meta">{f.mode} · {fmtSize(f.size)}</span>
              <div class="file-actions">
                {#if !f.dir}
                  <button type="button" class="file-act" onclick={() => downloadFile(f.name)} title="Download" aria-label="Download {f.name}">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M12 3v12m0 0l-4-4m4 4l4-4M4 21h16"/></svg>
                  </button>
                {/if}
                <button type="button" class="file-act" onclick={() => renameItem(f.name)} title="Rename" aria-label="Rename {f.name}">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M12 20h9M16.5 3.5a2.12 2.12 0 1 1 3 3L7 19l-4 1 1-4L16.5 3.5z"/></svg>
                </button>
                <button type="button" class="file-del" onclick={() => deleteFile(f.name)} aria-label="Delete {f.name}" title="Delete">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2m3 0v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6"/></svg>
                </button>
              </div>
            </div>
          {/each}
        {/if}
      </div>

      {#if uploadMsg}
        <div class="note" style="margin:14px 18px"><div>{uploadMsg}</div></div>
      {/if}
    </div>

    <!-- In-browser editor modal. Plain <textarea> with monospace styling —
         deliberately lightweight (no CodeMirror; no Monaco). Files ≤ 1 MiB. -->
    {#if editor.open}
      <div class="modal-back" onclick={closeEditor} role="presentation"></div>
      <div class="modal-card" role="dialog" aria-label="Edit {editor.name}">
        <div class="modal-head">
          <div>
            <h3>{editor.name}</h3>
            <p class="mono" style="margin:0;color:var(--txt-2);font-size:12px">{site.root}/{editor.sub}</p>
          </div>
          <div style="display:flex;gap:8px">
            <button type="button" class="btn btn-ghost" onclick={closeEditor}>Cancel</button>
            <button type="button" class="btn btn-primary" onclick={saveEditor}
                    disabled={editor.busy || editor.content === editor.original}>
              {editor.busy ? 'Saving…' : (editor.content === editor.original ? 'No changes' : 'Save')}
            </button>
          </div>
        </div>
        {#if editor.err}<div class="note" style="margin:0 18px 12px"><div>{editor.err}</div></div>{/if}
        <textarea class="editor-area" bind:value={editor.content} spellcheck="false" autocomplete="off"
                  placeholder={editor.busy ? 'Loading…' : ''} disabled={editor.busy}
                  onkeydown={(e) => { if ((e.ctrlKey || e.metaKey) && e.key === 's') { e.preventDefault(); saveEditor() } }}></textarea>
        <div class="modal-foot">
          <span class="mono">{editor.content.length} bytes · {editor.content.split('\n').length} lines</span>
          <span style="color:var(--txt-2)">Ctrl/Cmd+S to save</span>
        </div>
      </div>
    {/if}

  {:else if active === 'cron'}
    <div class="fade">
      <div class="section"><div class="section-h"><div><h3>Cron Jobs</h3><p>Run as {site.user} · written to <span class="mono">crontab -u {site.user}</span></p></div></div>
        {#if cron.length === 0}<div class="empty">No cron jobs yet. Add one below — schedules follow standard crontab syntax (<span class="mono">*/5 * * * *</span>, <span class="mono">@daily</span>, etc.).</div>
        {:else}
          <table><thead><tr><th>Schedule</th><th>Command</th><th></th></tr></thead><tbody>
            {#each cron as c}
              <tr><td><span class="mono" style="color:var(--aura-strong)">{c.schedule}</span></td><td><span class="mono" style="color:var(--txt-2)">{c.command}</span></td><td style="text-align:right"><button type="button" class="manage" onclick={() => delCron(c.id)}>Delete</button></td></tr>
            {/each}
          </tbody></table>
        {/if}
        <div class="section-b" style="border-top:1px solid var(--line)">
          <div class="two">
            <div class="field"><label>
              <span class="label-text">Schedule</span>
              <input class="input" bind:value={newCron.schedule} placeholder="*/5 * * * *">
            </label></div>
            <div class="field"><label>
              <span class="label-text">Command</span>
              <input class="input" bind:value={newCron.command} placeholder="php /htdocs/cron.php">
            </label></div>
          </div>
          <button class="btn btn-primary" onclick={addCron} disabled={busy || !newCron.schedule || !newCron.command}>Add Cron Job</button>
        </div>
      </div>
    </div>

  {:else if active === 'logs'}
    <div class="section fade"><div class="section-h"><div><h3>Logs</h3><p>Tail of the last ~250 lines · live data; refresh by clicking a kind</p></div>
      <div style="display:flex;gap:8px">
        {#each ['access','error','app'] as k}<button type="button" class="chip" class:on={logKind === k} onclick={() => setLogKind(k)}>{k}</button>{/each}
        <button type="button" class="btn btn-ghost" style="padding:6px 14px;font-size:12.5px" onclick={() => load('logs')}>Refresh</button>
      </div></div>
      <div class="section-b">
        {#if logs.length === 0}
          <div class="empty">
            No <span class="mono">{logKind}</span> log entries yet for <span class="mono">{site.domain}</span>.
            {#if logKind === 'access'}This site has not served any requests yet — hit it from a browser and refresh.
            {:else if logKind === 'error'}No errors recorded — quiet is good.
            {:else}Application stdout/stderr is captured to <span class="mono">/home/{site.user}/logs/app.log</span>; some runtimes need to be configured to write there.{/if}
          </div>
        {:else}<pre class="code">{logs.join('\n')}</pre>{/if}
      </div>
    </div>
  {/if}
</div>

<script>
  import { go, ui } from '../lib/store.svelte.js'
  import { detailTabs } from '../lib/data.js'
  import { apiFetch } from '../lib/api.js'
  import { brandIcons, tabIcons } from '../lib/icons.js'
  import { confirmDialog, promptDialog, alertDialog } from '../lib/dialog.svelte.js'
  import { toast, toastSuccess, toastError } from '../lib/toast.svelte.js'

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
  let sslBusy = $state(false)
  let sshUsers = $state([])
  let nodeRuntimes = $state([])
  let nodePick = $state(site.node || 'default')
  let newSSH = $state({ username: '', type: 'sftp', password: randPw() })
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

  // v0.2.23: per-toggle save indicator. savedFlash[key] becomes true for
  // ~1.6s after a successful save; the UI shows a small green ✓ next to that
  // toggle. Lets the operator confirm a save without parsing a notice strip.
  let savedFlash = $state({})
  function flashSaved(key) {
    savedFlash = { ...savedFlash, [key]: true }
    setTimeout(() => { savedFlash = { ...savedFlash, [key]: false } }, 1600)
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
    } else {
      // Per-key flash so the operator sees confirmation at the toggle itself.
      for (const k of Object.keys(patch)) flashSaved(k)
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
  // v0.2.25: open Adminer for the chosen database. Mints a one-time SSO
  // token server-side, then opens the returned URL in a new tab. The PHP
  // wrapper at /_adminer/ consumes the token (single-use) and seeds
  // Adminer's session with the credentials — no login form, no typing.
  // v0.2.40: kick off an ACME re-issuance for this site's cert. lego runs
  // synchronously inside the panel request — typical issuance is 5–15s for
  // HTTP-01, ~30–60s for DNS-01 (TXT propagation), so we show a 'busy'
  // state on the button. On success we refresh the status to reflect the
  // new cert in the UI immediately.
  async function renewCert() {
    sslBusy = true
    const r = await apiFetch(`${base}/ssl/renew`, { method: 'POST' })
    const d = await r.json().catch(() => ({}))
    sslBusy = false
    if (!r.ok) {
      toastError(d.error ? `Issuance failed: ${d.error}` : `Issuance failed: HTTP ${r.status}`)
      // Still refresh — the certs table now has lastError populated.
      load('ssl')
      return
    }
    toastSuccess('Certificate issued. Reloading status…')
    load('ssl')
  }

  async function manageDb(engine, name) {
    const r = await apiFetch(`${base}/databases/${encodeURIComponent(engine)}/${encodeURIComponent(name)}/manage`, { method: 'POST' })
    const d = await r.json().catch(() => ({}))
    if (!r.ok) { notice = d.error || 'Could not start a manage session'; return }
    if (!d.url) { notice = 'Manage session returned no URL'; return }
    // open in a new tab so the panel stays as-is in the background
    window.open(d.url, '_blank', 'noopener')
  }

  // v0.2.23: drop a database + its user from the engine and the store.
  async function deleteDb(engine, name) {
    if (!(await confirmDialog({
      title: `Drop database "${name}"?`,
      message: `Engine: ${engine === 'postgres' ? 'PostgreSQL' : 'MariaDB'}\n\nThis drops the database AND its dedicated user. The change is immediate and cannot be undone.`,
      confirmText: 'Drop database', danger: true,
    }))) return
    const r = await apiFetch(`${base}/databases/${encodeURIComponent(engine)}/${encodeURIComponent(name)}`, { method: 'DELETE' })
    const d = await r.json().catch(() => ({}))
    if (!r.ok) { notice = d.error || 'Could not drop database'; return }
    notice = `Dropped ${name}.`
    load('databases')
  }
  // v0.2.23: site delete from Settings tab. Confirms by typing the domain.
  async function deleteSite() {
    const typed = await promptDialog({
      title: `Delete site ${site.domain}?`,
      message: `Type the domain below to confirm.\n\nThis will:\n• Remove the nginx vhost\n• Remove the PHP-FPM pool / Node systemd unit\n• Delete the site user (and their docroot + SFTP access)\n• Delete the SSL certificate record\n\nDatabases are kept — drop them on the Databases tab first if you want a complete teardown.`,
      placeholder: site.domain,
      confirmText: 'Delete site', danger: true,
    })
    if (typed === null) return
    if (typed !== site.domain) {
      await alertDialog({ title: 'Domain did not match', message: 'Site not deleted.', danger: true })
      return
    }
    const r = await apiFetch(`/api/sites/${encodeURIComponent(site.domain)}`, { method: 'DELETE' })
    if (!r.ok) { const d = await r.json().catch(() => ({})); notice = d.error || 'Delete failed'; return }
    go('sites')
  }
  async function addSSH() {
    busy = true
    const r = await apiFetch(`${base}/ssh-users`, { method: 'POST', body: JSON.stringify(newSSH) })
    const d = await r.json().catch(() => ({}))
    busy = false
    if (!r.ok) { notice = d.error || 'Failed'; return }
    notice = `Created ${d.username}. Password: ${d.password}`
    newSSH = { username: '', type: 'sftp', password: randPw() }
    load('sshftp')
  }
  async function delSSH(username) {
    if (!(await confirmDialog({
      title: `Delete SSH/FTP user "${username}"?`,
      message: 'Their access is revoked immediately.',
      confirmText: 'Delete', danger: true,
    }))) return
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
    notice = ''
    busy = true
    try {
      const r = await apiFetch(`${base}/cron`, { method: 'POST', body: JSON.stringify(newCron) })
      const d = await r.json().catch(() => ({}))
      if (!r.ok) {
        toastError(d.error
          ? `Could not add cron job: ${d.error}`
          : `Could not add cron job (HTTP ${r.status}). The cron daemon may not be installed — try: sudo apt install cron`)
        return
      }
      toastSuccess(`Cron job added; ${site.user}'s crontab refreshed`)
      newCron = { schedule: '', command: '' }
      load('cron')
    } catch (e) {
      toastError('Add failed: ' + (e?.message || 'unknown error'))
    } finally {
      busy = false
    }
  }
  async function delCron(id) {
    const r = await apiFetch(`${base}/cron/${id}`, { method: 'DELETE' })
    const d = await r.json().catch(() => ({}))
    if (!r.ok) { toastError(d.error || `Could not delete: ${r.status}`); return }
    toastSuccess('Cron job removed')
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
  function openDir(name) { selected = {}; filePath = filePath ? `${filePath}/${name}` : name; load('files') }
  function upDir() { selected = {}; filePath = filePath.split('/').slice(0, -1).join('/'); load('files') }
  function setLogKind(k) { logKind = k; load('logs') }
  function fmtSize(n) { return n > 1<<20 ? (n/(1<<20)).toFixed(1)+' MB' : (n/1024).toFixed(1)+' KB' }

  // File-manager upload state — drag-over highlight, in-flight progress, and
  // the file input ref so the 'Upload' button can trigger it programmatically.
  let dragOver = $state(false)
  let uploadBusy = $state(false)
  let uploadMsg = $state('')
  let fileInput = $state(null)   // refs the hidden <input type=file>

  // v0.2.18: per-upload progress. uploadProg tracks bytes-sent / bytes-total
  // for the in-flight upload so we can render a real progress bar + ETA. We
  // use XMLHttpRequest because fetch() still doesn't expose upload progress
  // events in any browser (the body is a stream the browser owns; XHR's
  // upload.onprogress is the only portable way).
  let uploadProg = $state({ active: false, loaded: 0, total: 0, files: 0, name: '' })
  let uploadXHR = $state(null)  // exposes Cancel button

  function csrf() {
    const m = document.cookie.match(/(?:^|;\s*)auracp_csrf=([^;]+)/)
    return m ? decodeURIComponent(m[1]) : ''
  }
  function fmtBytes(n) {
    if (n < 1024) return n + ' B'
    if (n < 1<<20) return (n/1024).toFixed(1) + ' KB'
    if (n < 1<<30) return (n/(1<<20)).toFixed(1) + ' MB'
    return (n/(1<<30)).toFixed(2) + ' GB'
  }

  // v0.2.23: recursive folder upload. When the drop's DataTransferItemList is
  // available with webkitGetAsEntry, walk every dropped item depth-first and
  // collect {file, relPath} pairs. Subdirectories show up in relPath as
  // 'parent/child/file.ext'; the server splits on '/' and creates intermediate
  // directories before saving each file.
  async function walkEntry(entry, prefix, out) {
    if (entry.isFile) {
      const file = await new Promise((res, rej) => entry.file(res, rej))
      out.push({ file, relPath: prefix + entry.name })
    } else if (entry.isDirectory) {
      const reader = entry.createReader()
      // readEntries returns ≤ ~100 per call; loop until it returns empty.
      while (true) {
        const batch = await new Promise((res, rej) => reader.readEntries(res, rej))
        if (batch.length === 0) break
        for (const child of batch) await walkEntry(child, prefix + entry.name + '/', out)
      }
    }
  }

  async function flattenDataTransfer(dt) {
    if (dt?.items?.length && dt.items[0].webkitGetAsEntry) {
      const out = []
      for (const item of dt.items) {
        const e = item.webkitGetAsEntry?.()
        if (e) await walkEntry(e, '', out)
        else { const f = item.getAsFile?.(); if (f) out.push({ file: f, relPath: f.name }) }
      }
      return out
    }
    return Array.from(dt?.files || []).map(f => ({ file: f, relPath: f.name }))
  }

  // Accept either a FileList (from <input type=file>) or an array of
  // {file, relPath} from the folder walker. Normalises to the second shape.
  async function uploadFiles(input) {
    let list = []
    if (input instanceof FileList) {
      list = Array.from(input).map(f => ({ file: f, relPath: f.name }))
    } else if (Array.isArray(input)) {
      list = input
    } else {
      return
    }
    if (list.length === 0) return

    const total = list.reduce((s, x) => s + x.file.size, 0)
    const fd = new FormData()
    fd.append('path', filePath)
    for (const { file, relPath } of list) fd.append('files', file, relPath)

    uploadBusy = true
    uploadProg = { active: true, loaded: 0, total, files: list.length, name: list[0].relPath }
    uploadMsg = ''

    await new Promise((resolve) => {
      const xhr = new XMLHttpRequest()
      uploadXHR = xhr
      xhr.open('POST', `${base}/files`)
      xhr.setRequestHeader('X-CSRF-Token', csrf())
      xhr.upload.onprogress = (e) => {
        if (e.lengthComputable) uploadProg = { ...uploadProg, loaded: e.loaded, total: e.total || total }
      }
      xhr.onload = () => {
        uploadProg = { ...uploadProg, active: false }
        let d = {}
        try { d = JSON.parse(xhr.responseText) } catch {}
        if (xhr.status < 200 || xhr.status >= 300) {
          uploadMsg = d.error || `Upload failed: HTTP ${xhr.status}`
        } else {
          const errs = Array.isArray(d.errors) ? d.errors.length : 0
          uploadMsg = errs > 0
            ? `Uploaded ${d.saved}; ${errs} failed: ${d.errors.join(', ')}`
            : `Uploaded ${d.saved} file${d.saved > 1 ? 's' : ''} (${fmtBytes(total)}).`
          load('files')
          refreshTreeAt(filePath)   // a new file might be a new folder we should show
        }
        resolve()
      }
      xhr.onerror = () => { uploadMsg = 'Upload aborted (network error).'; uploadProg = { ...uploadProg, active: false }; resolve() }
      xhr.onabort = () => { uploadMsg = 'Upload cancelled.'; uploadProg = { ...uploadProg, active: false }; resolve() }
      xhr.send(fd)
    })
    uploadXHR = null
    uploadBusy = false
  }
  function cancelUpload() { if (uploadXHR) uploadXHR.abort() }

  // ─── v0.2.18: folder tree (lazy-loaded) ────────────────────────────────
  // The root node represents the document root itself. Children are loaded
  // on first expand and cached; subsequent expands toggle visibility without
  // re-fetching. Only directories are stored — files belong on the right pane.
  let tree = $state({ name: site.domain || '/', path: '', children: null, expanded: true, loading: false })

  async function fetchDirs(path) {
    const r = await apiFetch(`${base}/files?path=${encodeURIComponent(path)}`)
    if (!r.ok) return []
    const d = await r.json()
    return (d.entries || [])
      .filter(e => e.dir)
      .map(e => ({
        name: e.name,
        path: path ? `${path}/${e.name}` : e.name,
        children: null,
        expanded: false,
        loading: false,
      }))
  }

  // Find a node by its path. Returns null if not in the loaded portion of the
  // tree (which is fine — the caller just won't refresh that subtree).
  function findNode(path, node = tree) {
    if (node.path === path) return node
    if (!node.children) return null
    for (const c of node.children) {
      const hit = findNode(path, c)
      if (hit) return hit
    }
    return null
  }

  async function toggleNode(node) {
    if (node.expanded) { node.expanded = false; return }
    if (node.children === null) {
      node.loading = true
      node.children = await fetchDirs(node.path)
      node.loading = false
    }
    node.expanded = true
  }

  async function selectTreeNode(node) {
    selected = {}
    filePath = node.path
    // Ensure the clicked node is also expanded so its subtree is visible.
    if (node.children === null) {
      node.loading = true
      node.children = await fetchDirs(node.path)
      node.loading = false
    }
    node.expanded = true
    load('files')
  }

  // After an upload / mkdir / unzip / rename, the tree underneath `path`
  // might have grown. Refresh just that node's children — cheap.
  async function refreshTreeAt(path) {
    const node = findNode(path)
    if (!node) return
    node.children = await fetchDirs(node.path)
    node.expanded = true
  }

  // Lazy-load the tree's root the first time the Files tab is opened.
  $effect(() => {
    if (active === 'files' && tree.children === null && !tree.loading) {
      tree.loading = true
      fetchDirs('').then(c => { tree.children = c; tree.loading = false })
    }
  })
  async function onDrop(e) {
    e.preventDefault(); dragOver = false
    const list = await flattenDataTransfer(e.dataTransfer)
    uploadFiles(list)
  }
  function onDragOver(e) { e.preventDefault(); dragOver = true }
  function onDragLeave() { dragOver = false }
  async function deleteFile(name) {
    if (!(await confirmDialog({
      title: `Delete ${name}?`,
      message: 'This cannot be undone.',
      confirmText: 'Delete', danger: true,
    }))) return
    const sub = filePath ? `${filePath}/${name}` : name
    const r = await apiFetch(`${base}/files?path=${encodeURIComponent(sub)}`, { method: 'DELETE' })
    const d = await r.json().catch(() => ({}))
    if (!r.ok) { uploadMsg = d.error || 'Could not delete'; return }
    uploadMsg = `Deleted ${name}.`
    load('files')
    refreshTreeAt(filePath)
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
    selected = {}
    filePath = idx < 0 ? '' : crumbs.slice(0, idx + 1).join('/')
    load('files')
  }

  async function mkdir() {
    const name = await promptDialog({ title: 'New folder', message: 'Name:', confirmText: 'Create', placeholder: 'images' })
    if (!name) return
    const r = await apiFetch(`${base}/files/mkdir`, {
      method: 'POST', body: JSON.stringify({ path: filePath, name: name.trim() })
    })
    const d = await r.json().catch(() => ({}))
    if (!r.ok) { uploadMsg = d.error || 'Could not create folder'; return }
    uploadMsg = `Created ${name}.`
    load('files')
    refreshTreeAt(filePath)
  }

  async function touchFile() {
    const name = await promptDialog({ title: 'New file', message: 'Name (e.g. index.html, .env, robots.txt):', confirmText: 'Create', placeholder: 'index.html' })
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
    const next = await promptDialog({ title: `Rename ${oldName}`, message: 'New name:', defaultValue: oldName, confirmText: 'Rename' })
    if (!next || next.trim() === oldName) return
    const sub = filePath ? `${filePath}/${oldName}` : oldName
    const r = await apiFetch(`${base}/files/rename`, {
      method: 'POST', body: JSON.stringify({ path: sub, newName: next.trim() })
    })
    const d = await r.json().catch(() => ({}))
    if (!r.ok) { uploadMsg = d.error || 'Could not rename'; return }
    uploadMsg = `Renamed to ${next}.`
    load('files')
    refreshTreeAt(filePath)
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
  async function closeEditor() {
    if (editor.content !== editor.original) {
      if (!(await confirmDialog({ title: 'Discard unsaved changes?', confirmText: 'Discard', danger: true }))) return
    }
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

  // ─── v0.2.17: multi-select, chmod, zip/unzip ───────────────────────────
  // Multi-select state. Keyed by name within the current directory so we
  // don't have to reconcile across navigation — switching directories clears
  // the selection (intentional: bulk-acting across directories invites bugs).
  let selected = $state({})
  const selectedCount = $derived(Object.values(selected).filter(Boolean).length)
  const selectedNames = $derived(Object.keys(selected).filter(n => selected[n]))
  function clearSelection() { selected = {} }
  function toggleSel(name) { selected = { ...selected, [name]: !selected[name] } }
  function selectAll() {
    const next = {}
    for (const f of files) next[f.name] = true
    selected = next
  }

  async function bulkDelete() {
    if (selectedCount === 0) return
    if (!(await confirmDialog({
      title: `Delete ${selectedCount} selected item${selectedCount > 1 ? 's' : ''}?`,
      message: 'This cannot be undone.',
      confirmText: 'Delete', danger: true,
    }))) return
    const paths = selectedNames.map(n => filePath ? `${filePath}/${n}` : n)
    const r = await apiFetch(`${base}/files/delete-many`, {
      method: 'POST', body: JSON.stringify({ paths })
    })
    const d = await r.json().catch(() => ({}))
    if (!r.ok) { uploadMsg = d.error || 'Bulk delete failed'; return }
    const errs = Array.isArray(d.errors) ? d.errors.length : 0
    uploadMsg = errs > 0
      ? `Deleted ${d.deleted}; ${errs} failed: ${d.errors.join(', ')}`
      : `Deleted ${d.deleted} item${d.deleted > 1 ? 's' : ''}.`
    clearSelection()
    load('files')
    refreshTreeAt(filePath)
  }

  async function bulkZip() {
    if (selectedCount === 0) return
    const def = selectedCount === 1 ? selectedNames[0] + '.zip' : `archive-${Date.now().toString(36)}.zip`
    const name = await promptDialog({ title: 'Create archive', message: 'Archive name:', defaultValue: def, confirmText: 'Create' })
    if (!name) return
    const paths = selectedNames.map(n => filePath ? `${filePath}/${n}` : n)
    const r = await apiFetch(`${base}/files/zip`, {
      method: 'POST', body: JSON.stringify({ paths, dest: name.trim() })
    })
    const d = await r.json().catch(() => ({}))
    if (!r.ok) { uploadMsg = d.error || 'Zip failed'; return }
    uploadMsg = `Created ${name}.`
    clearSelection()
    load('files')
  }

  async function unzipItem(name) {
    if (!(await confirmDialog({
      title: `Extract ${name}?`,
      message: 'Files extract into the current folder. Existing files with matching names are overwritten.',
      confirmText: 'Extract',
    }))) return
    const sub = filePath ? `${filePath}/${name}` : name
    const r = await apiFetch(`${base}/files/unzip`, {
      method: 'POST', body: JSON.stringify({ path: sub })
    })
    const d = await r.json().catch(() => ({}))
    if (!r.ok) { uploadMsg = d.error || 'Extract failed'; return }
    uploadMsg = `Extracted ${name}.`
    load('files')
    refreshTreeAt(filePath)
  }

  // ─── chmod modal ────────────────────────────────────────────────────────
  // Model: a 9-bit grid (rwx × user/group/other). Mode is converted to / from
  // the octal string the API expects. Mode is also previewed as the classic
  // rwxr-xr-x string so the operator recognises it instantly.
  let chmod = $state({ open: false, name: '', sub: '', bits: [false,false,false, false,false,false, false,false,false], err: '', busy: false })
  const chmodLabels = ['Owner read','Owner write','Owner exec','Group read','Group write','Group exec','Other read','Other write','Other exec']
  const chmodPreview = $derived.by(() => {
    const c = (i) => chmod.bits[i] ? 'rwx'[i % 3] : '-'
    return c(0)+c(1)+c(2) + c(3)+c(4)+c(5) + c(6)+c(7)+c(8)
  })
  const chmodOctal = $derived.by(() => {
    const oct = (a, b, c) => (chmod.bits[a]?4:0) + (chmod.bits[b]?2:0) + (chmod.bits[c]?1:0)
    return '0' + oct(0,1,2) + oct(3,4,5) + oct(6,7,8)
  })

  function openChmod(f) {
    // Parse the existing 10-char mode string "drwxr-xr-x" — drop the leading
    // type bit, then convert each of the 9 perm chars to a boolean.
    const m = (f.mode || '').slice(-9)
    const bits = []
    for (let i = 0; i < 9; i++) bits.push(m[i] === 'rwx'[i % 3])
    chmod = {
      open: true,
      name: f.name,
      sub: filePath ? `${filePath}/${f.name}` : f.name,
      bits, err: '', busy: false,
    }
  }
  function closeChmod() { chmod = { ...chmod, open: false } }
  async function saveChmod() {
    chmod.busy = true
    chmod.err = ''
    const r = await apiFetch(`${base}/files/chmod`, {
      method: 'POST', body: JSON.stringify({ path: chmod.sub, mode: chmodOctal })
    })
    const d = await r.json().catch(() => ({}))
    chmod.busy = false
    if (!r.ok) { chmod.err = d.error || 'Chmod failed'; return }
    uploadMsg = `${chmod.name} → ${chmodPreview} (${chmodOctal})`
    closeChmod()
    load('files')
  }

  // v0.2.28: recognise .zip, .tar, .tar.gz, .tgz as extractable archives.
  // The backend's files.IsArchive must stay in sync with this list.
  function isArchive(name) {
    return /\.(zip|tar|tgz|tar\.gz)$/i.test(name)
  }
</script>

<div class="wrap fade">
  <button type="button" class="back" onclick={() => go('sites')}>
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M15 18l-6-6 6-6"/></svg> Back to Sites
  </button>

  <div class="site-head">
    <div class="fav brand">{#if brandIcons[site.type]}{@html brandIcons[site.type]}{:else}{site.ic || '◆'}{/if}</div>
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
      <button type="button" role="tab" aria-selected={active === t.id} class="tab" class:active={active === t.id} onclick={() => setTab(t.id)}>
        {#if tabIcons[t.id]}<span class="tab-ic" aria-hidden="true">{@html tabIcons[t.id]}</span>{/if}
        {t.label}
      </button>
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

      <!-- v0.2.23: Danger Zone — destructive site removal, guarded by a
           type-the-domain confirm prompt. Databases aren't dropped here
           (they belong to the engine, not the site); drop them on the
           Databases tab first if you want a clean teardown. -->
      <div class="section danger-zone">
        <div class="section-h"><div>
          <h3><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true" style="width:14px;height:14px;vertical-align:-2px;margin-right:6px;color:var(--down)"><path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>Danger Zone</h3>
          <p>Permanent actions for this site</p>
        </div></div>
        <div class="section-b">
          <div class="danger-row">
            <div>
              <b>Delete this site</b>
              <p>Removes the nginx vhost, PHP-FPM pool / systemd unit, site user, document root, and SFTP access. Databases are NOT dropped — drop them on the Databases tab first if you want a complete teardown.</p>
            </div>
            <button class="btn btn-danger" onclick={deleteSite}>
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true" style="width:14px;height:14px;vertical-align:-2px;margin-right:6px"><path d="M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2m3 0v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6"/></svg>
              Delete site
            </button>
          </div>
        </div>
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
      <div class="section"><div class="section-h"><div><h3>Databases</h3><p>Choose MariaDB or PostgreSQL per database · each gets its own user</p></div></div>
        {#if dbs.length === 0}<div class="empty">No databases yet. Add one below — auracpd creates the database AND a dedicated user with a strong password.</div>
        {:else}
          <table class="db-list">
            <thead><tr><th>Database</th><th>Engine</th><th>User</th><th style="text-align:right">Actions</th></tr></thead>
            <tbody>
              {#each dbs as d}
                <tr>
                  <td>
                    <div class="db-name-cell">
                      <span class="db-ic {d.engine === 'postgres' ? 'db-ic-pg' : 'db-ic-maria'}" aria-hidden="true">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M3 5v6c0 1.66 4 3 9 3s9-1.34 9-3V5"/><path d="M3 11v6c0 1.66 4 3 9 3s9-1.34 9-3v-6"/></svg>
                      </span>
                      <span class="mono db-name">{d.name}</span>
                    </div>
                  </td>
                  <td><span class="pill-eng {d.engine === 'postgres' ? 'eng-pg' : 'eng-maria'}">{d.engine === 'postgres' ? 'PostgreSQL' : 'MariaDB'}</span></td>
                  <td><span class="mono" style="color:var(--txt-2);font-size:12.5px">{d.user}</span></td>
                  <td style="text-align:right">
                    <div style="display:inline-flex;gap:6px;align-items:center">
                      <button type="button" class="btn btn-ghost" style="padding:5px 11px;font-size:12px" onclick={() => manageDb(d.engine, d.name)} title="Open Adminer">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true" style="width:13px;height:13px;vertical-align:-2px;margin-right:5px"><polyline points="14 3 21 3 21 10"/><line x1="21" y1="3" x2="10" y2="14"/><path d="M21 14v5a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5"/></svg>
                        Manage
                      </button>
                      <button type="button" class="file-del" onclick={() => deleteDb(d.engine, d.name)} title="Drop database" aria-label="Delete {d.name}">
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2m3 0v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6"/></svg>
                      </button>
                    </div>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
        <!-- v0.2.21: condensed two-row layout. Row 1: Engine + Database name.
             Row 2: Database user + password (with regenerate). Visually
             compact and matches how operators think about a DB record. -->
        <div class="section-b" style="border-top:1px solid var(--line)">
          <h4 class="db-add-h"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg> New database</h4>
          <div class="db-grid">
            <div class="field"><label>
              <span class="label-text">Engine</span>
              <select class="select ui" bind:value={newDb.engine}>
                <option value="mariadb">MariaDB</option>
                <option value="postgres">PostgreSQL</option>
              </select>
            </label></div>
            <div class="field"><label>
              <span class="label-text">Database name</span>
              <input class="input" bind:value={newDb.name} placeholder="app_db">
            </label></div>
          </div>
          <div class="db-grid">
            <div class="field"><label>
              <span class="label-text">Database user</span>
              <input class="input" bind:value={newDb.user} placeholder="app_user">
            </label></div>
            <div class="field"><label>
              <span class="label-text">Password <span class="hint">auto-generated, editable</span></span>
              <div class="input-row">
                <input class="input" bind:value={newDb.password}>
                <button type="button" class="gen" onclick={() => newDb.password = randPw()} title="Regenerate password" aria-label="Regenerate password">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true"><path d="M23 4v6h-6M1 20v-6h6"/><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"/></svg>
                </button>
              </div>
            </label></div>
          </div>
          <button class="btn btn-primary" onclick={addDb} disabled={busy || !newDb.name || !newDb.user || !newDb.password}>
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true" style="width:14px;height:14px;vertical-align:-2px;margin-right:6px"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
            Add Database
          </button>
          {#if notice}<div class="note" style="margin-top:12px"><div>{notice}</div></div>{/if}
        </div>
      </div>
    </div>

  {:else if active === 'cache'}
    <div class="section fade"><div class="section-h"><div><h3>Cache</h3><p>nginx fastcgi_cache / proxy_cache (per-site, opt-in)</p></div></div><div class="section-b" style="padding-top:4px">
      <div class="kv"><span class="k">Full-page cache</span><span class="kv-right">{#if savedFlash['cache']}<span class="saved-flash">✓ Saved</span>{/if}<button type="button" role="switch" aria-checked={isOn('cache')} aria-label="Toggle full-page cache" class="toggle" class:on={isOn('cache')} onclick={() => toggleConfig('cache')}></button></span></div>
      <div class="kv"><span class="k">Default TTL</span><span class="v">{config.cache_ttl || '600s'}</span></div>
    </div></div>

  {:else if active === 'ssl'}
    <!-- v0.2.40: SSL/TLS tab — live TLS + stored issuance state. When lego
         left a lastError on the cert record, that's surfaced verbatim with
         a Retry button next to it. CF DNS-01 toggle gets a clearer banner
         when the host's CF token isn't configured yet. -->
    <div class="section fade"><div class="section-h"><div><h3>SSL/TLS Certificate</h3><p>Issued + renewed by auracpd via <span class="mono">go-acme/lego</span>. HTTP-01 by default; switch to DNS-01 for Cloudflare-proxied domains.</p></div>
      {#if sslStatus}<span class="status"><span class="sdot {sslStatus.status === 'active' ? 's-up' : sslStatus.status === 'pending' ? 's-warn' : 's-down'}"></span>{sslStatus.status}</span>{/if}</div>
      <div class="section-b" style="padding-top:4px">
        {#if sslStatus === null}
          <div class="kv"><span class="k">Status</span><span class="v">checking…</span></div>
        {:else if sslStatus.status === 'active'}
          <div class="kv"><span class="k">Issuer</span><span class="v">{sslStatus.issuer || '—'}</span></div>
          <div class="kv"><span class="k">Domains</span><span class="v">{(sslStatus.domains || []).join(', ') || '—'}</span></div>
          <div class="kv"><span class="k">Expires</span><span class="v">{sslStatus.expires ? new Date(sslStatus.expires).toLocaleString() : '—'}{#if sslStatus.expires}<span class="hint" style="margin-left:8px">({Math.max(0, Math.round((new Date(sslStatus.expires) - Date.now()) / 86400000))} days left)</span>{/if}</span></div>
        {:else}
          <div class="kv"><span class="k">Live status</span><span class="v">{sslStatus?.message || 'no certificate served yet'}</span></div>
          <div class="kv"><span class="k">Provider</span><span class="v">Let's Encrypt (auto, in-process via lego)</span></div>
          {#if sslStatus?.stored?.lastError}
            <div class="note ssl-fail" style="margin:14px 0 6px"><div>
              <b>Issuance failed</b> (attempt {sslStatus.stored.attempts || 1})<br>
              <span class="mono" style="font-size:12px;color:var(--down)">{sslStatus.stored.lastError}</span>
            </div></div>
            <!-- Detect the common 'orange-cloud blocks HTTP-01' pattern from
                 the error text and surface the DNS-01 remediation inline. -->
            {#if /cloudflare|http-01|connection refused|timeout|404|403/i.test(sslStatus.stored.lastError) && !sslStatus.cloudflareDNS}
              <div class="hint" style="margin:6px 0 0">
                Looks like HTTP-01 can't reach this server. If the domain is proxied through Cloudflare
                (<b>orange cloud</b>), switch to DNS-01 below — it works without exposing port 80.
              </div>
            {/if}
          {:else if sslStatus?.stored?.status === 'pending'}
            <div class="hint" style="margin-top:8px">
              Cert issuance is in progress. First request after site create can take 10–60s.
            </div>
          {:else}
            <div class="hint" style="margin-top:8px">
              No cert on file yet. Click <b>Retry issuance</b> below; watch <span class="mono">journalctl -u auracpd</span> if it fails.
            </div>
          {/if}
        {/if}

        <div style="margin-top:14px;display:flex;gap:8px;flex-wrap:wrap">
          <button class="btn btn-primary" onclick={renewCert} disabled={sslBusy}>
            {sslBusy ? 'Issuing…' : (sslStatus?.status === 'active' ? 'Renew now' : 'Retry issuance')}
          </button>
          <button class="btn btn-ghost" onclick={() => load('ssl')}>Re-check status</button>
        </div>

        <div class="kv" style="margin-top:18px;border-top:1px solid var(--line);padding-top:14px">
          <span class="k">Cloudflare DNS-01 <span class="hint">wildcard / proxied domains</span></span>
          <span class="kv-right">{#if savedFlash['cloudflare_dns']}<span class="saved-flash">✓ Saved</span>{/if}<button type="button" role="switch" aria-checked={isOn('cloudflare_dns')} aria-label="Toggle Cloudflare DNS-01 challenge" class="toggle" class:on={isOn('cloudflare_dns')} onclick={() => toggleConfig('cloudflare_dns')}></button></span>
        </div>
        {#if isOn('cloudflare_dns') && sslStatus?.cloudflareTokenSet === false}
          <div class="note ssl-fail" style="margin-top:10px"><div>
            DNS-01 needs a Cloudflare API token at the panel level.
            Configure it under <button type="button" class="linkish" onclick={() => go('instance')}>Settings → Cloudflare</button>,
            then click <b>Retry issuance</b>.
          </div></div>
        {:else}
          <div class="hint" style="margin-left:0">Token configured under <b>Settings → Cloudflare</b>. Use DNS-01 for wildcards, or when CF orange-cloud blocks HTTP-01.</div>
        {/if}
      </div></div>

  {:else if active === 'security'}
    <div class="section fade"><div class="section-h"><div><h3>Security</h3><p>Access controls</p></div></div><div class="section-b" style="padding-top:4px">
      <div class="kv"><span class="k">Basic authentication</span><span class="kv-right">{#if savedFlash['basic_auth']}<span class="saved-flash">✓ Saved</span>{/if}<button type="button" role="switch" aria-checked={isOn('basic_auth')} aria-label="Toggle basic authentication" class="toggle" class:on={isOn('basic_auth')} onclick={() => toggleConfig('basic_auth')}></button></span></div>
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
      <div class="kv"><span class="k">Block bad bots</span><span class="kv-right">{#if savedFlash['block_bots']}<span class="saved-flash">✓ Saved</span>{/if}<button type="button" role="switch" aria-checked={isOn('block_bots')} aria-label="Toggle bot blocking" class="toggle" class:on={isOn('block_bots')} onclick={() => toggleConfig('block_bots')}></button></span></div>
      <div class="hint" style="margin-left:0">
        Blocks the SEO scraper set by User-Agent: <span class="mono">AhrefsBot</span>, <span class="mono">SemrushBot</span>, <span class="mono">MJ12bot</span>, <span class="mono">DotBot</span>, <span class="mono">PetalBot</span>.
        Returns <span class="mono">403</span> at the nginx layer — no PHP / app workload spent on them.
      </div>
    </div></div>

  {:else if active === 'sshftp'}
    <div class="section fade"><div class="section-h"><div><h3>SSH / FTP Users</h3><p>Chroot-jailed to the site home — extra accounts get their own credentials</p></div></div>
      <table class="ssh-table"><thead><tr><th>User</th><th>Type</th><th style="text-align:right">Actions</th></tr></thead><tbody>
        <tr>
          <td><span class="mono">{site.user}</span> <span class="role-tag">owner</span></td>
          <td><span class="acc-pill acc-ssh">SSH + SFTP</span></td>
          <td style="text-align:right;color:var(--txt-3);font-size:12px">primary account</td>
        </tr>
        {#each sshUsers as u}
          <tr>
            <td><span class="mono">{u.username}</span></td>
            <td><span class="acc-pill {u.type === 'ssh' ? 'acc-ssh' : 'acc-sftp'}">{u.type === 'ssh' ? 'SSH + SFTP' : 'SFTP only'}</span></td>
            <td style="text-align:right">
              <button type="button" class="file-del" onclick={() => delSSH(u.username)} title="Delete user" aria-label="Delete {u.username}">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2m3 0v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6"/></svg>
              </button>
            </td>
          </tr>
        {/each}
      </tbody></table>
      <div class="section-b" style="border-top:1px solid var(--line)">
        <h4 class="db-add-h"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true"><path d="M16 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M19 8v6M22 11h-6"/></svg> New SSH/FTP user</h4>
        <div class="db-grid">
          <div class="field"><label>
            <span class="label-text">Username</span>
            <input class="input" bind:value={newSSH.username} placeholder="editor">
          </label></div>
          <div class="field"><label>
            <span class="label-text">Access</span>
            <select class="select ui" bind:value={newSSH.type}>
              <option value="sftp">SFTP only</option>
              <option value="ssh">SSH + SFTP</option>
            </select>
          </label></div>
        </div>
        <div class="field"><label>
          <span class="label-text">Password <span class="hint">auto-generated, editable</span></span>
          <div class="input-row">
            <input class="input" bind:value={newSSH.password}>
            <button type="button" class="gen" onclick={() => newSSH.password = randPw()} title="Regenerate password" aria-label="Regenerate password">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true"><path d="M23 4v6h-6M1 20v-6h6"/><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"/></svg>
            </button>
          </div>
        </label></div>
        <button class="btn btn-primary" onclick={addSSH} disabled={busy || !newSSH.username || !newSSH.password}>
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true" style="width:14px;height:14px;vertical-align:-2px;margin-right:6px"><path d="M16 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M19 8v6M22 11h-6"/></svg>
          Add User
        </button>
        {#if notice}<div class="note" style="margin-top:12px"><div>{notice}</div></div>{/if}
      </div>
    </div>

  {:else if active === 'files'}
    {#snippet treeNode(node)}
      <li>
        <div class="tree-row" class:active={node.path === filePath}>
          {#if node.children !== null && node.children.length === 0 && !node.loading}
            <span class="tree-spacer" aria-hidden="true"></span>
          {:else}
            <button type="button" class="tree-toggle" onclick={() => toggleNode(node)} aria-label={node.expanded ? 'Collapse' : 'Expand'}>
              {#if node.loading}
                <span class="tree-spin"></span>
              {:else}
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" aria-hidden="true" style="transform:rotate({node.expanded ? 90 : 0}deg);transition:transform .12s"><path d="M9 6l6 6-6 6"/></svg>
              {/if}
            </button>
          {/if}
          <button type="button" class="tree-label" onclick={() => selectTreeNode(node)}>
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true" class="tree-ic"><path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7z"/></svg>
            <span>{node.name}</span>
          </button>
        </div>
        {#if node.expanded && node.children && node.children.length > 0}
          <ul class="tree-children">
            {#each node.children as child (child.path)}
              {@render treeNode(child)}
            {/each}
          </ul>
        {/if}
      </li>
    {/snippet}

    <div class="section fade fm-section" role="region" aria-label="File manager"
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

      <!-- v0.2.18: live upload progress bar. Shows total bytes, percent, and
           a Cancel button. Sits above the bulk-action bar so an in-flight
           upload doesn't get buried under the list. -->
      {#if uploadProg.active}
        <div class="upload-prog">
          <div class="upload-prog-info">
            <span><b>Uploading {uploadProg.files} file{uploadProg.files > 1 ? 's' : ''}</b></span>
            <span class="mono">{fmtBytes(uploadProg.loaded)} / {fmtBytes(uploadProg.total)} · {Math.round(uploadProg.loaded / (uploadProg.total || 1) * 100)}%</span>
            <button type="button" class="manage" onclick={cancelUpload}>Cancel</button>
          </div>
          <div class="upload-prog-bar"><div class="upload-prog-fill" style="width:{Math.round(uploadProg.loaded / (uploadProg.total || 1) * 100)}%"></div></div>
        </div>
      {/if}

      <!-- v0.2.18: split layout — folder tree on the left, file list on the
           right. Below 760 px the tree drops above the list so the panel
           stays usable on tablets. -->
      <div class="fm-split">
        <aside class="fm-tree" aria-label="Folder tree">
          <div class="fm-tree-head"><span>Folders</span></div>
          {#if tree.children === null}
            <div class="fm-tree-empty">Loading…</div>
          {:else}
            <ul class="tree-root">
              {@render treeNode(tree)}
            </ul>
          {/if}
        </aside>
        <div class="fm-main">

      <!-- v0.2.17: sticky bulk-action bar appears when one or more rows are
           checked. Pinned to the top of the file list so it's always reachable
           without scrolling back up on long directories. -->
      {#if selectedCount > 0}
        <div class="bulk-bar">
          <span class="bulk-count">{selectedCount} selected</span>
          <button type="button" class="btn btn-ghost" style="padding:6px 12px" onclick={selectAll}>Select all ({files.length})</button>
          <button type="button" class="btn btn-ghost" style="padding:6px 12px" onclick={clearSelection}>Clear</button>
          <span class="bulk-spacer"></span>
          <button type="button" class="btn btn-ghost" style="padding:6px 12px" onclick={bulkZip}>
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" aria-hidden="true" style="width:14px;height:14px;vertical-align:-2px;margin-right:6px"><path d="M21 8v13H3V8M1 3h22v5H1z"/><path d="M10 12h4"/></svg>
            Zip
          </button>
          <button type="button" class="btn btn-danger" style="padding:6px 12px" onclick={bulkDelete}>
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true" style="width:14px;height:14px;vertical-align:-2px;margin-right:6px"><path d="M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2m3 0v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6"/></svg>
            Delete
          </button>
        </div>
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
            <div class="file-row-grid" class:sel={selected[f.name]}>
              <input type="checkbox" class="file-check" checked={!!selected[f.name]}
                     onchange={() => toggleSel(f.name)} aria-label="Select {f.name}">
              {#if f.dir}
                <button type="button" class="file-row k folder" onclick={() => openDir(f.name)} title="Open folder">
                  <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true" class="file-ic file-ic-folder"><path d="M3 6.5A1.5 1.5 0 0 1 4.5 5h4.382a1.5 1.5 0 0 1 1.06.44L11.5 6.5h8A1.5 1.5 0 0 1 21 8v9.5a1.5 1.5 0 0 1-1.5 1.5h-15A1.5 1.5 0 0 1 3 17.5v-11z"/></svg>
                  {f.name}
                </button>
              {:else if isTextish(f.name)}
                <button type="button" class="file-row k" onclick={() => openEditor(f.name)} title="Open in editor">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" class="file-ic"><path d="M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9z"/><path d="M14 3v6h6"/></svg>
                  {f.name}
                </button>
              {:else if isArchive(f.name)}
                <button type="button" class="file-row k archive" onclick={() => unzipItem(f.name)} title="Extract here">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" class="file-ic"><path d="M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9z"/><path d="M14 3v6h6M10 12v8M10 16h4"/></svg>
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
                {#if isArchive(f.name) && !f.dir}
                  <!-- v0.2.28: extract button now has a visible label so it
                       isn't mistaken for one of the icon-only actions. Distinct
                       colour (aura-strong) draws the eye on a .zip / .tar.gz row. -->
                  <button type="button" class="file-extract" onclick={() => unzipItem(f.name)} title="Extract here" aria-label="Extract {f.name}">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true"><path d="M21 8v13H3V8M1 3h22v5H1z"/><path d="M10 12h4"/></svg>
                    <span>Extract</span>
                  </button>
                {/if}
                {#if !f.dir}
                  <button type="button" class="file-act" onclick={() => downloadFile(f.name)} title="Download" aria-label="Download {f.name}">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M12 3v12m0 0l-4-4m4 4l4-4M4 21h16"/></svg>
                  </button>
                {/if}
                <button type="button" class="file-act" onclick={() => openChmod(f)} title="Permissions" aria-label="Permissions for {f.name}">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M12 1l9 4v6c0 5.5-3.8 10.7-9 12-5.2-1.3-9-6.5-9-12V5l9-4z"/></svg>
                </button>
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
        </div><!-- /.fm-main -->
      </div><!-- /.fm-split -->
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

    <!-- chmod modal: 3x3 grid of rwx checkboxes with the live preview ("rwxr-xr-x")
         and the resulting octal mode underneath. The grid layout matches how chmod
         is usually explained in tutorials, so the mental model transfers. -->
    {#if chmod.open}
      <div class="modal-back" onclick={closeChmod} role="presentation"></div>
      <div class="modal-card chmod-card" role="dialog" aria-label="Permissions for {chmod.name}">
        <div class="modal-head">
          <div>
            <h3>Permissions</h3>
            <p class="mono" style="margin:0;color:var(--txt-2);font-size:12px">{chmod.name}</p>
          </div>
          <div style="display:flex;gap:8px">
            <button type="button" class="btn btn-ghost" onclick={closeChmod}>Cancel</button>
            <button type="button" class="btn btn-primary" onclick={saveChmod} disabled={chmod.busy}>
              {chmod.busy ? 'Saving…' : 'Apply'}
            </button>
          </div>
        </div>
        {#if chmod.err}<div class="note" style="margin:0 22px 12px"><div>{chmod.err}</div></div>{/if}
        <div class="chmod-body">
          <table class="chmod-grid">
            <thead><tr><th></th><th>Read</th><th>Write</th><th>Execute</th></tr></thead>
            <tbody>
              {#each ['Owner','Group','Other'] as who, row}
                <tr>
                  <th>{who}</th>
                  {#each [0,1,2] as col}
                    {@const i = row * 3 + col}
                    <td><label class="chmod-cell">
                      <input type="checkbox" bind:checked={chmod.bits[i]} aria-label={chmodLabels[i]}>
                      <span>{'rwx'[col]}</span>
                    </label></td>
                  {/each}
                </tr>
              {/each}
            </tbody>
          </table>
          <div class="chmod-preview">
            <div><span class="chmod-label">Symbolic</span><span class="mono">{chmodPreview}</span></div>
            <div><span class="chmod-label">Octal</span><span class="mono">{chmodOctal}</span></div>
          </div>
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
          {#if notice}<div class="note" style="margin-top:14px"><div>{notice}</div></div>{/if}
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

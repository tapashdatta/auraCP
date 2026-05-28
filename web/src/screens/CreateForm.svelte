<script>
  import { go, openSite, ui } from '../lib/store.svelte.js'
  import { apiFetch } from '../lib/api.js'

  const META = {
    wordpress:    { title: 'New WordPress Site', sub: 'Managed WordPress · wp-cli · automatic database' },
    php:          { title: 'New PHP Site', sub: 'PHP-FPM pool · isolated site user' },
    nodejs:       { title: 'New Node.js Site', sub: 'systemd-managed app behind nginx' },
    python:       { title: 'New Python Site', sub: 'gunicorn/uvicorn via systemd' },
    static:       { title: 'New Static HTML Site', sub: 'Edge-cached file server · zero runtime' },
    reverseproxy: { title: 'New Reverse Proxy', sub: 'TLS termination in front of any upstream' },
  }
  const type = ui.createType
  const meta = META[type] || META.php

  // single reactive model; type-specific fields are shown conditionally.
  // `runner` is the Node-supervision choice the user picks (systemd-native vs
  // PM2 wrapper inside systemd); the API still takes pm2:bool, we map at submit.
  let m = $state({
    domain: '', user: '', password: randPw(),
    phpVersion: '', port: type === 'python' ? '8000' : '3000',
    startFile: 'app.js', module: 'main:app', upstream: 'http://127.0.0.1:8088',
    runner: 'systemd',  // 'systemd' | 'pm2'
  })
  let phpVersions = $state([])  // populated from /api/instance/php-versions on mount
  let busy = $state(false)
  let error = $state('')

  function randPw() { return Math.random().toString(36).slice(2, 10) + '-' + Math.random().toString(36).slice(2, 6) }

  // Fetch installed PHP versions so the dropdown only offers what'll actually
  // work — hard-coding 8.3/8.4/8.5 leaves the user picking a version that
  // isn't installed and getting a confusing systemctl error on submit.
  if (type === 'php' || type === 'wordpress') {
    apiFetch('/api/instance/php-versions').then(r => r.json()).then(list => {
      const installed = (list || []).filter(v => v.installed)
      phpVersions = installed
      const def = installed.find(v => v.isDefault) || installed[0]
      if (def) m.phpVersion = def.version
    }).catch(() => { phpVersions = [] })
  }

  async function submit(e) {
    e.preventDefault()
    error = ''
    if (!m.domain || !m.user) { error = 'Domain and site user are required.'; return }
    busy = true
    const r = await apiFetch('/api/sites', {
      method: 'POST',
      body: JSON.stringify({
        type, domain: m.domain, user: m.user, password: m.password,
        phpVersion: m.phpVersion, port: Number(m.port),
        startFile: m.startFile, module: m.module, upstream: m.upstream,
        pm2: m.runner === 'pm2',
      }),
    })
    const d = await r.json().catch(() => ({}))
    busy = false
    if (!r.ok) { error = d.error || 'Could not create site'; return }
    openSite(d)
  }
</script>

<div class="wrap form-wrap fade">
  <span class="back" onclick={() => go('add')}>
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M15 18l-6-6 6-6"/></svg> Back
  </span>
  <div class="ph"><div><h1>{meta.title}</h1><div class="sub">{meta.sub}</div></div></div>

  <div class="card"><div class="section-b">
    <form onsubmit={submit}>
      <div class="field"><label>Domain Name</label>
        <input class="input" bind:value={m.domain} placeholder="example.com"></div>

      {#if type === 'php' || type === 'wordpress'}
        <div class="field"><label>PHP Version <span class="hint">8.3+ only · only installed versions are listed</span></label>
          {#if phpVersions.length === 0}
            <div class="hint" style="margin-left:0">
              No PHP-FPM versions are installed on this host.
              Install one from <a href="#" onclick={(e) => { e.preventDefault(); go('instance') }}>Settings → PHP Versions</a> before creating PHP sites.
            </div>
          {:else}
            <select class="select" bind:value={m.phpVersion}>
              {#each phpVersions as v}
                <option value={v.version}>{v.version}{v.isDefault ? ' (default)' : ''}</option>
              {/each}
            </select>
          {/if}
        </div>
      {/if}
      {#if type === 'nodejs'}
        <div class="two">
          <div class="field"><label>App Port</label><input class="input" bind:value={m.port}></div>
          <div class="field"><label>Startup File</label><input class="input" bind:value={m.startFile}></div>
        </div>
        <div class="field">
          <label>Process supervision</label>
          <div class="runner-choice">
            <label class="runner-opt" class:selected={m.runner === 'systemd'}>
              <input type="radio" name="runner" value="systemd" bind:group={m.runner}>
              <div class="runner-body">
                <div class="runner-title">systemd <span class="hint" style="margin-left:6px">default</span></div>
                <div class="runner-desc">Plain <span class="mono">node {m.startFile || 'app.js'}</span> as the unit's ExecStart. Restart-on-crash, journald logs, cgroup limits — same as the rest of the panel's services.</div>
              </div>
            </label>
            <label class="runner-opt" class:selected={m.runner === 'pm2'}>
              <input type="radio" name="runner" value="pm2" bind:group={m.runner}>
              <div class="runner-body">
                <div class="runner-title">PM2 wrapper</div>
                <div class="runner-desc">Wraps the app in <span class="mono">pm2-runtime</span> (foreground — no PM2 daemon). Pick this only if your app uses <span class="mono">ecosystem.config.js</span> or you specifically want PM2's Node-level cluster mode. Process name = the domain.</div>
              </div>
            </label>
          </div>
        </div>
      {/if}
      {#if type === 'python'}
        <div class="two">
          <div class="field"><label>App Port</label><input class="input" bind:value={m.port}></div>
          <div class="field"><label>App Module <span class="hint">WSGI/ASGI</span></label><input class="input" bind:value={m.module}></div>
        </div>
      {/if}
      {#if type === 'reverseproxy'}
        <div class="field"><label>Reverse Proxy URL <span class="hint">upstream</span></label>
          <input class="input" bind:value={m.upstream}></div>
      {/if}

      <div class="field"><label>Site User <span class="hint">main SSH user</span></label>
        <input class="input" bind:value={m.user} placeholder="siteuser"></div>
      <div class="field"><label>Site User Password</label>
        <div class="input-row">
          <input class="input" bind:value={m.password}>
          <button type="button" class="gen" onclick={() => m.password = randPw()}>Generate</button>
        </div></div>

      {#if error}<div class="login-err">{error}</div>{/if}
      <div class="form-actions">
        <button class="btn btn-primary" disabled={busy}>{busy ? 'Creating…' : 'Create Site'}</button>
        <button type="button" class="btn btn-ghost" onclick={() => go('add')}>Cancel</button>
      </div>
    </form>
  </div></div>
</div>

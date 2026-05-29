<script>
  import { go, openSite, ui } from '../lib/store.svelte.js'
  import { apiFetch } from '../lib/api.js'
  import { alertDialog } from '../lib/dialog.svelte.js'
  import { toastSuccess } from '../lib/toast.svelte.js'

  const META = {
    wordpress:    { title: 'New WordPress Site', sub: 'WordPress via wp-cli with database provisioning' },
    php:          { title: 'New PHP Site',       sub: 'PHP-FPM pool, dedicated UID, Unix socket' },
    nodejs:       { title: 'New Node.js Site',   sub: 'Per-site systemd unit behind nginx' },
    python:       { title: 'New Python Site',    sub: 'gunicorn or uvicorn as a systemd unit' },
    static:       { title: 'New Static HTML Site', sub: 'nginx file server with optional response caching' },
    reverseproxy: { title: 'New Reverse Proxy',  sub: 'TLS termination in front of any upstream' },
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
    // v0.2.34: WordPress one-click auto-install. wpInstall defaults ON for
    // WordPress sites; the form expands to collect admin user/email/title.
    wpInstall: type === 'wordpress',
    wpTitle: '',
    wpAdminUser: 'admin',
    wpAdminPass: randPw(),
    wpAdminEmail: '',
  })
  let phpVersions = $state([])  // populated from /api/instance/php-versions on mount
  let busy = $state(false)
  let error = $state('')
  // Flag flips the moment the operator hand-edits the site-user field, so we
  // stop overwriting it with the domain-derived value as they keep typing.
  let userTouched = $state(false)

  function randPw() { return Math.random().toString(36).slice(2, 10) + '-' + Math.random().toString(36).slice(2, 6) }

  // Derive a sensible Linux site-user from the domain. Goal: a HUMAN-READABLE
  // handle — operator-flagged the previous output ("a-g91z" for a.garuda.sh)
  // as opaque. New shape:
  //
  //   a.garuda.sh        → "a-garuda"         (subdomain + registrable label)
  //   blog.example.com   → "blog-example"
  //   shop.example.co.uk → "shop-example"     (TLD always dropped)
  //   garuda.sh          → "garuda"           (single-label site → just the SLD)
  //   localhost          → "localhost"
  //
  // Collisions are possible (two different domains that share the same first
  // two labels — e.g. a.garuda.sh + a.garuda.com). When the operator hits
  // Create the API rejects the dup with a clear message, the field stays
  // populated, the operator edits it to disambiguate. That's a much rarer
  // case than the previous "every username is unreadable random gibberish"
  // problem, so the trade-off is worth it.
  //
  // Constraints from validate.Username: ^[a-z][a-z0-9_-]{0,31}$. We cap at
  // 30 chars to leave headroom, and prepend "s" if the cleaned label starts
  // with a digit (rare — purely numeric subdomains like "1.example.com").
  function deriveSiteUser(domain) {
    const labels = (domain || '').toLowerCase().split('.').filter(Boolean)
    if (labels.length === 0) return 'site'
    // Drop the TLD only when there's more than one label — keeps single-
    // label "localhost" working without becoming empty.
    const keep = labels.length > 1 ? labels.slice(0, -1) : labels
    const cleaned = keep
      .map(l => l.replace(/[^a-z0-9-]/g, '').replace(/^-+|-+$/g, ''))
      .filter(Boolean)
      .join('-')
      .slice(0, 30)
    if (!cleaned) return 'site'
    if (!/^[a-z]/.test(cleaned)) return ('s-' + cleaned).slice(0, 30)
    return cleaned
  }

  // Auto-sync the site user from the domain until the operator manually edits.
  $effect(() => {
    if (userTouched) return
    m.user = deriveSiteUser(m.domain)
  })

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
    if (type === 'wordpress' && m.wpInstall) {
      if (!m.wpAdminEmail) { error = 'Admin email is required for WordPress auto-install.'; return }
    }
    busy = true
    const r = await apiFetch('/api/sites', {
      method: 'POST',
      body: JSON.stringify({
        type, domain: m.domain, user: m.user, password: m.password,
        phpVersion: m.phpVersion, port: Number(m.port),
        startFile: m.startFile, module: m.module, upstream: m.upstream,
        pm2: m.runner === 'pm2',
        // WordPress-only; ignored by the backend for other types.
        wpInstall: m.wpInstall, wpTitle: m.wpTitle,
        wpAdminUser: m.wpAdminUser, wpAdminPass: m.wpAdminPass,
        wpAdminEmail: m.wpAdminEmail,
      }),
    })
    const d = await r.json().catch(() => ({}))
    busy = false
    if (!r.ok) { error = d.error || 'Could not create site'; return }

    // v0.2.34: if WordPress auto-installed, surface the admin credentials
    // in a one-shot dialog BEFORE navigating to the site detail. The
    // password is returned from the API exactly once and never stored
    // cleartext; if the operator dismisses without copying, they have to
    // wp user reset-password via SSH.
    if (d.wpInstall) {
      await alertDialog({
        title: 'WordPress is installed and ready',
        message:
          `Save these now — the password is shown only once:\n\n` +
          `Login URL:   ${d.wpInstall.loginUrl}\n` +
          `Admin user:  ${d.wpInstall.adminUser}\n` +
          `Admin pass:  ${d.wpInstall.adminPass}\n` +
          `Admin email: ${d.wpInstall.adminEmail}\n\n` +
          `Database (saved in the panel; manage via Aura DB at /dbadmin/):\n` +
          `  Name: ${d.wpInstall.dbName}\n` +
          `  User: ${d.wpInstall.dbUser}`,
        confirmText: 'I saved the password',
      })
      toastSuccess(`${m.domain} is live — log in at /wp-admin/`)
    }
    openSite(d)
  }
</script>

<div class="wrap form-wrap fade">
  <button type="button" class="back" onclick={() => go('add')}>
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M15 18l-6-6 6-6"/></svg> Back
  </button>
  <div class="ph"><div><h1>{meta.title}</h1><div class="sub">{meta.sub}</div></div></div>

  <div class="card"><div class="section-b">
    <form onsubmit={submit}>
      <div class="field"><label>
        <span class="label-text">Domain Name</span>
        <input class="input" bind:value={m.domain} placeholder="example.com">
      </label></div>

      {#if type === 'php' || type === 'wordpress'}
        <div class="field"><label>
          <span class="label-text">PHP Version <span class="hint">8.3+; only installed versions listed</span></span>
          {#if phpVersions.length === 0}
            <div class="hint" style="margin-left:0">
              No PHP-FPM versions are installed on this host.
              Install one from <button type="button" class="linkish" onclick={() => go('instance')}>Settings → PHP Versions</button> before creating PHP sites.
            </div>
          {:else}
            <select class="select" bind:value={m.phpVersion}>
              {#each phpVersions as v}
                <option value={v.version}>{v.version}{v.isDefault ? ' (default)' : ''}</option>
              {/each}
            </select>
          {/if}
        </label></div>
      {/if}

      {#if type === 'wordpress'}
        <!-- v0.2.34: WordPress one-click auto-install. Toggle defaults ON;
             unchecking creates an empty site (operator wires WP themselves). -->
        <div class="field">
          <label class="wp-toggle">
            <input type="checkbox" bind:checked={m.wpInstall}>
            <div>
              <b>Auto-install WordPress now</b>
              <span>Provisions a MariaDB database + creates wp-config.php + runs the install with the admin you set below. The site is live on first request.</span>
            </div>
          </label>
        </div>
        {#if m.wpInstall}
          <div class="field"><label>
            <span class="label-text">Site Title</span>
            <input class="input" bind:value={m.wpTitle} placeholder={m.domain || 'My WordPress Site'}>
          </label></div>
          <div class="two">
            <div class="field"><label>
              <span class="label-text">Admin Username</span>
              <input class="input" bind:value={m.wpAdminUser} placeholder="admin">
            </label></div>
            <div class="field"><label>
              <span class="label-text">Admin Email <span class="hint">required</span></span>
              <input class="input" type="email" bind:value={m.wpAdminEmail} placeholder="you@example.com">
            </label></div>
          </div>
          <div class="field"><label>
            <span class="label-text">Admin Password <span class="hint">auto-generated · shown once after create</span></span>
            <div class="input-row">
              <input class="input" bind:value={m.wpAdminPass}>
              <button type="button" class="gen" onclick={() => m.wpAdminPass = randPw()} title="Regenerate" aria-label="Regenerate admin password">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true"><path d="M23 4v6h-6M1 20v-6h6"/><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"/></svg>
              </button>
            </div>
          </label></div>
        {/if}
      {/if}
      {#if type === 'nodejs'}
        <div class="two">
          <div class="field"><label>
            <span class="label-text">App Port</span>
            <input class="input" bind:value={m.port}>
          </label></div>
          <div class="field"><label>
            <span class="label-text">Startup File</span>
            <input class="input" bind:value={m.startFile}>
          </label></div>
        </div>
        <div class="field">
          <span class="label-text" style="display:block;font-weight:600;font-size:13px;margin-bottom:7px">Process supervision</span>
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
          <div class="field"><label>
            <span class="label-text">App Port</span>
            <input class="input" bind:value={m.port}>
          </label></div>
          <div class="field"><label>
            <span class="label-text">App Module <span class="hint">WSGI/ASGI</span></span>
            <input class="input" bind:value={m.module}>
          </label></div>
        </div>
      {/if}
      {#if type === 'reverseproxy'}
        <div class="field"><label>
          <span class="label-text">Reverse Proxy URL <span class="hint">upstream</span></span>
          <input class="input" bind:value={m.upstream}>
        </label></div>
      {/if}

      <!-- v0.2.36: Site User + Site User Password paired into a 2-col row,
           and the Regenerate buttons now use the icon variant (the previous
           text "Regenerate" overflowed the .gen class's fixed 38 px box). -->
      <div class="two">
        <div class="field"><label>
          <span class="label-text">Site User <span class="hint">from domain · editable</span></span>
          <div class="input-row">
            <input class="input" bind:value={m.user} oninput={() => userTouched = true} placeholder="siteuser">
            <button type="button" class="gen" onclick={() => { userTouched = false; m.user = deriveSiteUser(m.domain) }} title="Regenerate from domain" aria-label="Regenerate site user">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true"><path d="M23 4v6h-6M1 20v-6h6"/><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"/></svg>
            </button>
          </div>
        </label></div>
        <div class="field"><label>
          <span class="label-text">Site User Password</span>
          <div class="input-row">
            <input class="input" bind:value={m.password}>
            <button type="button" class="gen" onclick={() => m.password = randPw()} title="Generate new password" aria-label="Generate site user password">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true"><path d="M23 4v6h-6M1 20v-6h6"/><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"/></svg>
            </button>
          </div>
        </label></div>
      </div>

      {#if error}<div class="login-err">{error}</div>{/if}
      <div class="form-actions">
        <button class="btn btn-primary" disabled={busy}>{busy ? 'Creating…' : 'Create Site'}</button>
        <button type="button" class="btn btn-ghost" onclick={() => go('add')}>Cancel</button>
      </div>
    </form>
  </div></div>
</div>

<script>
  import { onMount } from 'svelte'
  import { go, openSite } from '../lib/store.svelte.js'
  import { session } from '../lib/auth.svelte.js'
  import { fetchSites } from '../lib/data.js'
  import { apiFetch } from '../lib/api.js'
  import { brandIcons } from '../lib/icons.js'

  let allSites = $state([])   // populated from the API on mount
  let stats = $state(null)
  let recent = $state([])     // recent audit events for the right-rail
  let filter = $state('')
  let active = $state('All')
  const tabs = ['All', 'PHP', 'Node.js', 'WordPress']

  onMount(async () => {
    allSites = await fetchSites()
    const r = await apiFetch('/api/instance')
    if (r.ok) stats = await r.json()
    // best-effort recent activity (permission-gated; ignore failures)
    const a = await apiFetch('/api/audit?limit=4').catch(() => null)
    if (a && a.ok) {
      const rows = await a.json().catch(() => [])
      recent = Array.isArray(rows) ? rows.slice(0, 4) : []
    }
  })

  const memPct = $derived(stats && stats.memTotalMB ? Math.round(stats.memUsedMB / stats.memTotalMB * 100) : 0)
  const diskPct = $derived(stats && stats.diskTotalGB ? Math.round(stats.diskUsedGB / stats.diskTotalGB * 100) : 0)
  const loadPct = $derived(stats ? Math.min(100, Math.round(stats.load1 / Math.max(1, stats.cores) * 100)) : 0)

  // Per-type counts for the hero "site mix" mini-bar.
  const typeCounts = $derived(() => {
    const c = {}
    for (const s of allSites) c[s.type] = (c[s.type] || 0) + 1
    return c
  })

  // Greeting handle from the session — first label of the admin email, capitalised.
  const greetingName = $derived(() => {
    const e = session.user?.email || ''
    const handle = e.split('@')[0] || 'admin'
    return handle.charAt(0).toUpperCase() + handle.slice(1)
  })

  // "Health line" — overall colour + label based on the worst current metric.
  const health = $derived(() => {
    if (!stats) return { tone: 'ok', text: 'All systems operational' }
    if (loadPct > 90 || memPct > 90 || diskPct > 90) return { tone: 'danger', text: 'Resources critical' }
    if (loadPct > 70 || memPct > 75 || diskPct > 75) return { tone: 'warn',   text: 'Heads-up — usage climbing' }
    return { tone: 'ok', text: 'All systems operational' }
  })

  const filtered = $derived(
    allSites.filter(s =>
      (filter === '' || s.domain.includes(filter) || s.user.includes(filter)) &&
      (active === 'All' || s.app.includes(active))
    )
  )

  function relTime(ts) {
    if (!ts) return ''
    const d = (Date.now() - new Date(ts).getTime()) / 1000
    if (d < 60) return Math.floor(d) + 's ago'
    if (d < 3600) return Math.floor(d / 60) + 'm ago'
    if (d < 86400) return Math.floor(d / 3600) + 'h ago'
    return Math.floor(d / 86400) + 'd ago'
  }
</script>

<div class="wrap fade">
  <!-- ── Hero greeting + big system metric + activity grid ──────────── -->
  <section class="hero">
    <div class="hero-greet">
      <div class="avatar hero-avatar">{(session.user?.email || 'A').charAt(0).toUpperCase()}</div>
      <div>
        <h1>Welcome, {greetingName()}</h1>
        <span class="pill-cat {health().tone}">
          <span class="sdot s-{health().tone === 'ok' ? 'up' : health().tone === 'warn' ? 'warn' : 'down'}"></span>
          {health().text}
        </span>
      </div>
    </div>
    <button class="btn btn-dark" onclick={() => go('add')}>
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4"><path d="M12 5v14M5 12h14"/></svg>
      Add Site
    </button>
  </section>

  <section class="hero-grid">
    <!-- Big hero metric — sites count + per-type bar mix -->
    <div class="hero-card hero-card-metric">
      <span class="lbl">Sites hosted</span>
      <div class="hero-num">{allSites.length}<small>{allSites.length === 1 ? ' site' : ' sites'}</small></div>
      <div class="mix">
        {#each Object.entries(typeCounts()) as [t, n]}
          <div class="mix-row">
            <span class="mix-type">{t}</span>
            <span class="mix-bar"><i style="width:{Math.min(100, n/Math.max(1, allSites.length)*100)}%"></i></span>
            <span class="mix-n mono">{n}</span>
          </div>
        {/each}
        {#if allSites.length === 0}
          <span class="hint" style="margin-left:0">No sites yet. Use <b>Add Site</b> to provision one.</span>
        {/if}
      </div>
    </div>

    <!-- Server snapshot — load / mem / disk gauges as horizontal bars -->
    <div class="hero-card">
      <span class="lbl">Server snapshot</span>
      {#if stats}
        <div class="snap">
          <div class="snap-row">
            <span class="snap-k">Load (1m)</span>
            <span class="snap-v mono">{stats.load1.toFixed(2)}<small> · {stats.cores}c</small></span>
            <span class="bar"><i style="width:{loadPct}%"></i></span>
          </div>
          <div class="snap-row" class:hot={memPct > 75}>
            <span class="snap-k">Memory</span>
            <span class="snap-v mono">{memPct}<small>%</small></span>
            <span class="bar"><i style="width:{memPct}%"></i></span>
          </div>
          <div class="snap-row" class:hot={diskPct > 75}>
            <span class="snap-k">Disk</span>
            <span class="snap-v mono">{stats.diskUsedGB}<small> / {stats.diskTotalGB} GB</small></span>
            <span class="bar"><i style="width:{diskPct}%"></i></span>
          </div>
        </div>
      {:else}
        <div class="hint" style="margin-left:0">Loading…</div>
      {/if}
    </div>

    <!-- Server info — OS / hostname / IP. Replaces the old activity card;
         the activity feed now lives below the sites table. -->
    <div class="hero-card hero-card-server">
      <span class="lbl">Server</span>
      {#if stats}
        <dl class="srv">
          <div><dt>OS</dt><dd>{stats.os || '—'}</dd></div>
          <div><dt>Hostname</dt><dd class="mono">{stats.hostname || '—'}</dd></div>
          <div><dt>IP address</dt><dd class="mono">{stats.ip || '—'}</dd></div>
        </dl>
      {:else}
        <div class="hint" style="margin-left:0">Loading…</div>
      {/if}
    </div>
  </section>

  <!-- ── Sites table (existing behaviour; new visual tokens) ──────────── -->
  <div class="card">
    <div class="toolbar">
      <div class="filter">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="11" cy="11" r="7"/><path d="M21 21l-4-4"/></svg>
        <input placeholder="Filter by domain or user…" bind:value={filter}>
      </div>
      {#each tabs as t}
        <button type="button" class="chip" class:on={active === t} onclick={() => active = t}>{t}</button>
      {/each}
      <span class="spacer"></span>
      <span class="mono" style="color:var(--txt-3);font-size:12.5px">{filtered.length} shown</span>
    </div>
    <table>
      <thead><tr><th>Domain</th><th class="hide-on-mobile">Site User</th><th class="hide-on-mobile">App</th><th>Status</th><th class="hide-on-mobile" style="text-align:right">Manage</th></tr></thead>
      <tbody>
        {#if filtered.length === 0}
          <tr><td colspan="5"><div class="empty">No sites match the current filter.</div></td></tr>
        {/if}
        {#each filtered as s}
          <!-- v0.2.31: whole row is clickable; the Manage button stays as a
               focusable affordance for keyboard navigation. Pointer cursor +
               hover highlight signal that clicking anywhere on the row jumps
               into the site's detail view. -->
          <tr class="site-row" tabindex="0" role="link" aria-label="Open {s.domain}"
              onclick={() => openSite(s)}
              onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); openSite(s) } }}>
            <td><div class="domain"><div class="fav brand">{#if brandIcons[s.type]}{@html brandIcons[s.type]}{:else}{s.ic}{/if}</div><div class="nm">{s.domain}<small>{s.root}</small></div></div></td>
            <td class="hide-on-mobile"><span class="mono">{s.user}</span></td>
            <td class="hide-on-mobile">
              <span class="badge {s.badge}"><span class="ic brand-sm">{#if brandIcons[s.type]}{@html brandIcons[s.type]}{:else}{s.ic}{/if}</span>{s.app}{#if s.node} {s.node}{/if}</span>
            </td>
            <td><span class="status"><span class="sdot s-{s.status}"></span>{s.statusText}</span></td>
            <td class="hide-on-mobile" style="text-align:right">
              <div class="row-actions">
                <!-- v0.2.47: visit the live site in a new tab. Stops the row
                     click from also triggering openSite(). -->
                <a class="row-act-link" href="https://{s.domain}" target="_blank" rel="noopener noreferrer"
                   onclick={(e) => e.stopPropagation()}
                   title="Open https://{s.domain} in a new tab" aria-label="Open {s.domain}">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/><polyline points="15 3 21 3 21 9"/><line x1="10" y1="14" x2="21" y2="3"/></svg>
                </a>
                <button type="button" class="manage" onclick={(e) => { e.stopPropagation(); openSite(s) }}>Manage
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M9 6l6 6-6 6"/></svg>
                </button>
              </div>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>

  <!-- Recent activity — audit log preview, moved below the sites table. -->
  <div class="card activity-card">
    <div class="section-h"><div><h3>Recent activity</h3><p>Latest panel actions</p></div></div>
    {#if recent.length === 0}
      <div class="empty">No events yet.</div>
    {:else}
      <ul class="feed">
        {#each recent as e}
          <li>
            <span class="feed-dot"></span>
            <div class="feed-body">
              <div class="feed-action mono">{e.action}</div>
              {#if e.target}<div class="feed-target">{e.target}</div>{/if}
              <div class="feed-meta">{e.actor || 'system'} · {relTime(e.ts)}</div>
            </div>
          </li>
        {/each}
      </ul>
    {/if}
  </div>
</div>

<style>
  /* Hero greeting + metrics strip — dashboard landing only. */
  .hero{display:flex;align-items:flex-end;justify-content:space-between;gap:24px;margin:6px 0 22px}
  .hero-greet{display:flex;align-items:center;gap:18px;min-width:0}
  .hero-avatar{width:54px;height:54px;font-size:20px;border-radius:50%}
  .hero h1{font-family:var(--fs-display);font-weight:700;font-size:34px;letter-spacing:-.03em;line-height:1.1;margin-bottom:8px}

  /* v0.2.29: hero cards stay 3-up from iPad onward (down from a 1100 px
     breakpoint that dropped to 2-up). Achieved by trimming padding +
     reducing the metric card's hero-num so all three fit at 720 px. */
  .hero-grid{display:grid;grid-template-columns:1fr 1fr 1fr;gap:12px;margin-bottom:22px}
  .hero-card{border:1px solid var(--line);border-radius:var(--radius);background:var(--surface-0);padding:14px 16px;box-shadow:var(--shadow);min-width:0;display:flex;flex-direction:column;gap:10px}
  .hero-card .lbl{font-size:11px;text-transform:uppercase;letter-spacing:.08em;color:var(--txt-3);font-weight:600}
  .hero-card-metric{gap:4px}
  .hero-card-metric .hero-num{margin:-2px 0 6px}

  /* Per-type site mix bar inside the hero metric card. */
  .mix{display:flex;flex-direction:column;gap:7px}
  .mix-row{display:grid;grid-template-columns:78px 1fr 28px;gap:10px;align-items:center}
  .mix-type{font-size:11.5px;color:var(--txt-2);text-transform:capitalize}
  .mix-bar{height:5px;background:var(--surface-2);border-radius:var(--radius-pill);overflow:hidden}
  .mix-bar i{display:block;height:100%;background:linear-gradient(90deg,var(--aura-strong),var(--aura));border-radius:inherit}
  .mix-n{font-size:11.5px;color:var(--txt-2);text-align:right}

  /* Server snapshot rows — compact three-col grid (label / value / bar). */
  .snap{display:flex;flex-direction:column;gap:11px;margin-top:2px}
  .snap-row{display:grid;grid-template-columns:78px 1fr;column-gap:10px;row-gap:6px;align-items:center}
  .snap-row .bar{grid-column:1 / -1;margin:0}
  .snap-k{font-size:12.5px;color:var(--txt-2)}
  .snap-v{font-size:14px;font-weight:600;color:var(--txt);justify-self:end}
  .snap-v small{font-weight:500;color:var(--txt-3);font-size:11px;margin-left:3px}
  .snap-row.hot .bar i{background:linear-gradient(90deg,#a8691a,var(--warn))}

  /* Server-info card (OS / hostname / IP) — replaces the old activity card. */
  .srv{display:flex;flex-direction:column;gap:9px;margin:0}
  .srv > div{display:flex;align-items:baseline;justify-content:space-between;gap:12px}
  .srv dt{font-size:12px;color:var(--txt-3);flex:none}
  .srv dd{margin:0;font-size:12.5px;color:var(--txt);text-align:right;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}

  /* Recent activity card — now below the sites table. */
  .activity-card{margin-top:18px}
  .activity-card .section-h{padding:15px 18px;border-bottom:1px solid var(--line)}
  .activity-card .section-h h3{font-size:14px;font-weight:600;margin:0}
  .activity-card .section-h p{font-size:12px;color:var(--txt-3);margin-top:2px}
  .activity-card .feed{padding:14px 18px}
  .activity-card .empty{border-top:none}

  .feed{list-style:none;padding:0;margin:0;display:flex;flex-direction:column;gap:14px}
  .feed li{display:flex;gap:11px;align-items:center;font-size:12.5px}
  .feed-dot{width:7px;height:7px;border-radius:50%;background:var(--aura);box-shadow:0 0 0 3px var(--aura-glow);flex:none;margin-top:5px}
  /* Single-line row: action · target (flex, truncates) · meta (pushed right). */
  .feed-body{min-width:0;flex:1;display:flex;align-items:baseline;gap:8px}
  .feed-action{font-weight:600;color:var(--txt);font-size:12.5px;flex:none}
  .feed-target{color:var(--txt-2);font-size:12px;font-family:var(--fs-mono);overflow:hidden;text-overflow:ellipsis;white-space:nowrap;flex:1;min-width:0}
  .feed-meta{font-size:11px;color:var(--txt-3);margin-left:auto;flex:none;white-space:nowrap}

  .empty{text-align:center;padding:32px 16px;color:var(--txt-3)}
  .hint{color:var(--txt-3);font-size:12.5px}

  /* v0.2.47: site-row right-edge action cluster — external-link icon next
     to the Manage button. Same opacity-on-hover treatment as Manage. */
  .row-actions{display:inline-flex;align-items:center;gap:6px;justify-content:flex-end}
  .row-act-link{display:inline-flex;align-items:center;justify-content:center;width:30px;height:30px;border-radius:8px;color:var(--txt-3);border:1px solid var(--line);background:var(--surface-1);transition:.1s;opacity:.6}
  .row-act-link:hover{color:var(--aura-strong);border-color:var(--line-2);background:var(--surface-2);text-decoration:none}
  .row-act-link svg{width:14px;height:14px}
  :global(tr.site-row:hover) .row-act-link{opacity:1}

  /* v0.2.29: below 720 px (small tablet portrait + phones) hide Recent
     Activity entirely — operators on a phone are dipping in to check sites,
     not scanning the audit log. Sites Hosted + Server Snapshot stay; they're
     the only two cards that earn the screen space at small widths.
     v0.2.30: keep them side-by-side all the way down to phone widths instead
     of stacking; tightened padding + hero-num at < 480 px keeps the pair
     readable at 320 px viewports. */
  @media (max-width:720px){
    .hero-grid{grid-template-columns:1fr 1fr}
    /* Server card takes the full second row rather than a lone half cell. */
    .hero-card-server{grid-column:1 / -1}
  }
  @media (max-width:480px){
    .hero{flex-direction:column;align-items:flex-start;gap:14px}
    .hero h1{font-size:26px}
    .hero-grid{gap:10px}
    .hero-card{padding:12px 14px;gap:8px}
    :global(.hero-num){font-size:32px !important}
    :global(.hero-num small){display:none}
    .mix-row{grid-template-columns:60px 1fr 22px;gap:7px}
    .mix-type{font-size:10.5px}
    .snap-row{grid-template-columns:60px 1fr;column-gap:6px}
    .snap-k{font-size:11.5px}
    .snap-v{font-size:12.5px}
  }
</style>

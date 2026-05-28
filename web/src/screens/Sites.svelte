<script>
  import { onMount } from 'svelte'
  import { go, openSite } from '../lib/store.svelte.js'
  import { session } from '../lib/auth.svelte.js'
  import { fetchSites } from '../lib/data.js'
  import { apiFetch } from '../lib/api.js'

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

    <!-- Recent activity — audit log preview -->
    <div class="hero-card">
      <span class="lbl">Recent activity</span>
      {#if recent.length === 0}
        <div class="hint" style="margin-left:0">No events yet.</div>
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
      <thead><tr><th>Domain</th><th>Site User</th><th>App</th><th>Status</th><th style="text-align:right">Manage</th></tr></thead>
      <tbody>
        {#if filtered.length === 0}
          <tr><td colspan="5"><div class="empty">No sites match the current filter.</div></td></tr>
        {/if}
        {#each filtered as s}
          <tr>
            <td><div class="domain"><div class="fav">{s.ic}</div><div class="nm">{s.domain}<small>{s.root}</small></div></div></td>
            <td><span class="mono">{s.user}</span></td>
            <td>
              <span class="badge {s.badge}"><span class="ic">{s.ic}</span>{s.app}</span>
              {#if s.node}<span class="node-tag">node {s.node}</span>{/if}
            </td>
            <td><span class="status"><span class="sdot s-{s.status}"></span>{s.statusText}</span></td>
            <td style="text-align:right">
              <button type="button" class="manage" onclick={() => openSite(s)}>Manage
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M9 6l6 6-6 6"/></svg>
              </button>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
</div>

<style>
  /* Hero greeting + metrics strip — dashboard landing only. */
  .hero{display:flex;align-items:flex-end;justify-content:space-between;gap:24px;margin:6px 0 22px}
  .hero-greet{display:flex;align-items:center;gap:18px;min-width:0}
  .hero-avatar{width:54px;height:54px;font-size:20px;border-radius:50%}
  .hero h1{font-family:var(--fs-display);font-weight:700;font-size:34px;letter-spacing:-.03em;line-height:1.1;margin-bottom:8px}

  .hero-grid{display:grid;grid-template-columns:1.3fr 1fr 1fr;gap:14px;margin-bottom:26px}
  .hero-card{border:1px solid var(--line);border-radius:var(--radius);background:var(--surface-0);padding:18px 20px;box-shadow:var(--shadow);min-width:0;display:flex;flex-direction:column;gap:12px}
  .hero-card .lbl{font-size:11.5px;text-transform:uppercase;letter-spacing:.09em;color:var(--txt-3);font-weight:600}
  .hero-card-metric{gap:6px}
  .hero-card-metric .hero-num{margin:-4px 0 8px}

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

  /* Recent activity feed inside the third hero card. */
  .feed{list-style:none;padding:0;margin:0;display:flex;flex-direction:column;gap:14px}
  .feed li{display:flex;gap:11px;align-items:flex-start;font-size:12.5px}
  .feed-dot{width:7px;height:7px;border-radius:50%;background:var(--aura);box-shadow:0 0 0 3px var(--aura-glow);flex:none;margin-top:5px}
  .feed-body{min-width:0;flex:1}
  .feed-action{font-weight:600;color:var(--txt);font-size:12.5px}
  .feed-target{color:var(--txt-2);font-size:12px;font-family:var(--fs-mono);overflow:hidden;text-overflow:ellipsis;white-space:nowrap;margin-top:1px}
  .feed-meta{font-size:11px;color:var(--txt-3);margin-top:3px}

  .empty{text-align:center;padding:32px 16px;color:var(--txt-3)}
  .hint{color:var(--txt-3);font-size:12.5px}

  @media (max-width:1100px){
    .hero-grid{grid-template-columns:1fr 1fr}
    .hero-card-metric{grid-column:1 / -1}
  }
  @media (max-width:640px){
    .hero{flex-direction:column;align-items:flex-start;gap:14px}
    .hero h1{font-size:26px}
    :global(.hero-num){font-size:64px !important}
    .hero-grid{grid-template-columns:1fr}
    .hero-card-metric{grid-column:auto}
  }
</style>

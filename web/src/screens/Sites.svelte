<script>
  import { onMount } from 'svelte'
  import { go, openSite } from '../lib/store.svelte.js'
  import { fetchSites } from '../lib/data.js'
  import { apiFetch } from '../lib/api.js'

  let allSites = $state([])   // populated from the API on mount
  let stats = $state(null)
  let filter = $state('')
  let active = $state('All')
  const tabs = ['All', 'PHP', 'Node.js', 'WordPress']

  onMount(async () => {
    allSites = await fetchSites()
    const r = await apiFetch('/api/instance')
    if (r.ok) stats = await r.json()
  })

  const memPct = $derived(stats && stats.memTotalMB ? Math.round(stats.memUsedMB / stats.memTotalMB * 100) : 0)
  const diskPct = $derived(stats && stats.diskTotalGB ? Math.round(stats.diskUsedGB / stats.diskTotalGB * 100) : 0)

  const filtered = $derived(
    allSites.filter(s =>
      (filter === '' || s.domain.includes(filter) || s.user.includes(filter)) &&
      (active === 'All' || s.app.includes(active))
    )
  )
</script>

<div class="wrap fade">
  <div class="ph">
    <div>
      <h1>Sites</h1>
      <div class="sub">{allSites.length} site{allSites.length === 1 ? '' : 's'}</div>
    </div>
    <button class="btn btn-primary" onclick={() => go('add')}>
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4"><path d="M12 5v14M5 12h14"/></svg>
      Add Site
    </button>
  </div>

  {#if stats}
    <div class="stats">
      <div class="stat"><div class="lbl">Load (1m)</div><div class="val">{stats.load1.toFixed(2)}</div><div class="bar"><i style="width:{Math.min(100, stats.load1/(stats.cores||1)*100)}%"></i></div></div>
      <div class="stat" class:warn={memPct > 75}><div class="lbl">Memory</div><div class="val">{memPct}<small>%</small></div><div class="bar"><i style="width:{memPct}%"></i></div></div>
      <div class="stat" class:warn={diskPct > 75}><div class="lbl">Disk</div><div class="val">{stats.diskUsedGB}<small> / {stats.diskTotalGB} GB</small></div><div class="bar"><i style="width:{diskPct}%"></i></div></div>
      <div class="stat"><div class="lbl">CPU cores</div><div class="val">{stats.cores}</div></div>
    </div>
  {/if}

  <div class="card">
    <div class="toolbar">
      <div class="filter">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="11" cy="11" r="7"/><path d="M21 21l-4-4"/></svg>
        <input placeholder="Filter by domain or user…" bind:value={filter}>
      </div>
      {#each tabs as t}
        <span class="chip" class:on={active === t} onclick={() => active = t}>{t}</span>
      {/each}
      <span class="spacer"></span>
      <span class="mono" style="color:var(--txt-3);font-size:12.5px">{filtered.length} shown</span>
    </div>
    <table>
      <thead><tr><th>Domain</th><th>Site User</th><th>App</th><th>Status</th><th style="text-align:right">Manage</th></tr></thead>
      <tbody>
        {#if filtered.length === 0}
          <tr><td colspan="5"><div class="empty">No sites yet — click <b>Add Site</b> to create one.</div></td></tr>
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
              <span class="manage" onclick={() => openSite(s)}>Manage
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M9 6l6 6-6 6"/></svg>
              </span>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
</div>

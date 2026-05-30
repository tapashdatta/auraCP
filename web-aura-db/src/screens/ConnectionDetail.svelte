<script>
  import { routeState, navigate } from '../lib/router.svelte.js'
  import { api, AuraDBError } from '../lib/api.js'
  import { connections, loadConnections } from '../lib/connections.svelte.js'
  import { loadSchemas, loadObjects } from '../lib/sqlEditor/schemaCache.svelte.js'
  import { t } from '../lib/strings.js'
  import Btn from '../lib/components/Btn.svelte'
  import StatusDot from '../lib/components/StatusDot.svelte'
  import Pill from '../lib/components/Pill.svelte'
  import ConfirmDialog from '../lib/components/ConfirmDialog.svelte'

  const id = $derived(routeState.params.id)
  const conn = $derived(connections.list.find((c) => c.id === id) || null)
  const engineLabel = $derived(conn ? (t(`tree.engine.${conn.engine}`) || conn.engine) : '')

  let testing = $state(false)
  /** @type {string|null} */
  let testResult = $state(null)
  let confirmOpen = $state(false)

  // Overview metrics. Strictly real, lazily fetched — no synthetic
  // trend/sparkline data (this project is live-data-only). Tables/views
  // are tallied from the schema object lists (reusing the shared schema
  // cache); skipped past a soft schema cap to stay lightweight.
  const SCHEMA_TALLY_CAP = 20
  let stats = $state({ schemas: null, tables: null, views: null, latencyMs: null, capped: false })
  let loadedFor = null

  $effect(() => {
    const cid = id
    if (cid && conn && loadedFor !== cid) {
      loadedFor = cid
      void loadStats(cid)
    }
  })

  async function loadStats(cid) {
    stats = { schemas: null, tables: null, views: null, latencyMs: null, capped: false }
    // Latency ping runs independently of the schema tally.
    api.testConnection(cid)
      .then((r) => { stats.latencyMs = r?.latencyMs ?? null })
      .catch(() => { /* latency stays — */ })
    try {
      const names = await loadSchemas(cid)
      if (cid !== id) return
      stats.schemas = names.length
      if (names.length === 0) { stats.tables = 0; stats.views = 0; return }
      if (names.length > SCHEMA_TALLY_CAP) { stats.capped = true; return }
      const objs = await Promise.all(
        names.map((n) => loadObjects(cid, n).catch(() => ({ tables: [], views: [] }))),
      )
      if (cid !== id) return
      stats.tables = objs.reduce((a, o) => a + ((o?.tables || []).length), 0)
      stats.views = objs.reduce((a, o) => a + ((o?.views || []).length), 0)
    } catch {
      /* leave nulls; tiles show — */
    }
  }

  const fmt = (n) => (n == null ? '—' : n)

  async function testConn() {
    if (!id) return
    testing = true; testResult = null
    try {
      const r = await api.testConnection(id)
      testResult = `OK ${r?.latencyMs ?? ''}ms`
      if (r?.latencyMs != null) stats.latencyMs = r.latencyMs
    } catch (err) {
      if (err instanceof AuraDBError) testResult = `ERR ${err.code}: ${err.message}`
      else testResult = 'ERR'
    } finally {
      testing = false
    }
  }

  async function doDelete() {
    if (!id) return
    await api.deleteConnection(id)
    await loadConnections()
    navigate('/connections')
  }
</script>

<div class="pane">
  <header class="pane__head">
    <h1 class="pane__title">{conn?.name || id}</h1>
    {#if conn}
      <StatusDot state={conn.status || 'idle'} />
      <Pill tone="info">{engineLabel}</Pill>
      {#if conn.readOnly}<Pill tone="warning">{t('tree.readonly')}</Pill>{/if}
    {/if}
  </header>
  {#if conn}
    <p class="pane__sub">{conn.host}{conn.port ? ':' + conn.port : ''}{conn.database ? ' · ' + conn.database : ''} • {engineLabel}</p>
  {/if}

  {#if conn}
    <div class="stats">
      <div class="stat">
        <span class="stat__label">Schemas</span>
        <span class="stat__value">{fmt(stats.schemas)}</span>
      </div>
      <div class="stat">
        <span class="stat__label">Tables</span>
        <span class="stat__value" title={stats.capped ? 'Too many schemas to tally on load' : undefined}>{stats.capped ? '—' : fmt(stats.tables)}</span>
      </div>
      <div class="stat">
        <span class="stat__label">Views</span>
        <span class="stat__value">{stats.capped ? '—' : fmt(stats.views)}</span>
      </div>
      <div class="stat">
        <span class="stat__label">Latency</span>
        <span class="stat__value">{fmt(stats.latencyMs)}{#if stats.latencyMs != null}<span class="stat__unit">ms</span>{/if}</span>
      </div>
    </div>
  {/if}

  <section class="section">
    <h2 class="section__title">Connection</h2>
    {#if conn}
      <table class="data u-max-w-520">
        <tbody>
          <tr><td class="u-detail-row-label">{t('conn.form.host')}</td><td class="num">{conn.host}{conn.port ? ':' + conn.port : ''}</td></tr>
          <tr><td class="u-color-dim">{t('conn.form.database')}</td><td class="num">{conn.database || '—'}</td></tr>
          <tr><td class="u-color-dim">{t('conn.form.username')}</td><td class="num">{conn.username || '—'}</td></tr>
        </tbody>
      </table>
    {/if}
    <div class="u-mt-4 u-row u-row--wrap">
      <Btn variant="primary" loading={testing} onclick={testConn}>{t('conn.detail.test')}</Btn>
      <Btn variant="ghost" onclick={() => navigate(`/connections/${id}`)}>{t('conn.detail.reveal')}</Btn>
      <Btn variant="ghost" onclick={() => navigate('/audit')}>{t('conn.detail.audit')}</Btn>
      <Btn variant="danger" onclick={() => confirmOpen = true}>{t('conn.detail.delete')}</Btn>
    </div>
    {#if testResult}
      <div class="u-test-result" role="status" aria-live="polite">{testResult}</div>
    {/if}
  </section>
</div>

<ConfirmDialog
  bind:open={confirmOpen}
  title={t('conn.detail.delete')}
  message="Delete this connection? Saved queries are kept."
  confirmLabel={t('action.delete')}
  tone="danger"
  onConfirm={doDelete}
/>

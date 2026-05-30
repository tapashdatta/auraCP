<script>
  import { routeState, navigate } from '../router.svelte.js'
  import { connections } from '../connections.svelte.js'
  import { toggleTree } from '../ui.svelte.js'
  import { t } from '../strings.js'
  import Icon from './Icon.svelte'

  // The connection the current route is scoped to: prefer the :id route
  // param, fall back to the tree's selection.
  const selectedConn = $derived(
    connections.list.find((c) => c.id === routeState.params.id) ||
    connections.list.find((c) => c.id === connections.selectedId) ||
    null,
  )

  // Breadcrumb trail derived from the route. Connection-scoped routes read
  // Connections › <conn> › <schema> › <table>; the standalone sections
  // (history/audit/account) render a single crumb.
  const crumbs = $derived(buildCrumbs(routeState, selectedConn))

  function buildCrumbs(rs, conn) {
    if (rs.name === 'history') return [{ label: t('nav.history') }]
    if (rs.name === 'audit') return [{ label: t('nav.audit') }]
    if (rs.name === 'account') return [{ label: t('nav.account') }]

    const out = [{ label: t('nav.connections'), path: '/connections' }]
    if (rs.name === 'query') out.push({ label: t('nav.queries') })
    if (conn) out.push({ label: conn.name, path: `/connections/${conn.id}` })
    const schema = rs.params.schema
    const table = rs.params.table
    if (schema) {
      out.push({
        label: schema,
        path: conn ? `/connections/${conn.id}/schemas/${schema}` : undefined,
      })
    }
    if (table) out.push({ label: table })
    return out
  }
</script>

<header class="topbar">
  <button
    type="button"
    class="topbar__burger"
    onclick={toggleTree}
    aria-label="Toggle connections"
  >
    <Icon name="menu" size={18} />
  </button>

  <button type="button" class="topbar__navbtn" onclick={() => history.back()} aria-label="Back">
    <Icon name="chevL" size={16} />
  </button>
  <button type="button" class="topbar__navbtn" onclick={() => history.forward()} aria-label="Forward">
    <Icon name="chevR" size={16} />
  </button>

  <div class="topbar__sep"></div>

  <nav class="topbar__crumbs" aria-label="Breadcrumb">
    {#each crumbs as c, i (i)}
      {#if i > 0}<span class="crumb__sep" aria-hidden="true">›</span>{/if}
      {#if c.path && i < crumbs.length - 1}
        <button type="button" class="crumb crumb--link" onclick={() => navigate(c.path)}>{c.label}</button>
      {:else}
        <span class="crumb crumb--current" aria-current="page">{c.label}</span>
      {/if}
    {/each}
  </nav>

  <div class="topbar__spacer"></div>

  {#if selectedConn}
    <span class="topbar__badge" title={selectedConn.name}>
      <span class="topbar__dot" style="background: var(--engine-{selectedConn.engine}, var(--accent))"></span>
      {selectedConn.engine}
    </span>
  {/if}
</header>

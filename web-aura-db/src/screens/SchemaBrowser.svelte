<script>
  import { routeState, navigate } from '../lib/router.svelte.js'
  import { api } from '../lib/api.js'
  import { t } from '../lib/strings.js'
  import { openTab } from '../lib/workspaces.svelte.js'
  import LoadingPane from '../lib/components/LoadingPane.svelte'

  const id = $derived(routeState.params.id)
  const schema = $derived(routeState.params.schema)

  let loading = $state(true)
  /** @type {{tables:any[], views:any[], functions:any[]}} */
  let data = $state({ tables: [], views: [], functions: [] })

  $effect(() => {
    if (!id || !schema) return
    loading = true
    api.listObjects(id, schema).then((r) => {
      data = {
        tables: r?.tables || [],
        views: r?.views || [],
        functions: r?.functions || [],
      }
    }).catch(() => { /* shell-stage */ }).finally(() => { loading = false })
  })

  function openTable(tbl) {
    // Single-click → table detail page. Double-click would jump straight
    // to the row grid; SchemaBrowser uses single-click for now.
    openTab({
      title: `${schema}.${tbl.name}`,
      path: `/connections/${id}/schemas/${schema}/tables/${tbl.name}`,
      icon: 'table',
    })
    navigate(`/connections/${id}/schemas/${schema}/tables/${tbl.name}`)
  }
  function openTableRows(tbl) {
    openTab({
      title: `${schema}.${tbl.name}`,
      path: `/connections/${id}/schemas/${schema}/tables/${tbl.name}/rows`,
      icon: 'table',
    })
    navigate(`/connections/${id}/schemas/${schema}/tables/${tbl.name}/rows`)
  }
</script>

<div class="pane">
  <header class="pane__head">
    <h1 class="pane__title">{t('schema.title', { schema })}</h1>
  </header>

  {#if loading}
    <LoadingPane />
  {:else}
    <section class="section">
      <h2 class="section__title">{t('schema.objects.tables')}</h2>
      {#if data.tables.length === 0}
        <div class="schema__note">No tables.</div>
      {:else}
        <table class="data">
          <tbody>
            {#each data.tables as tbl (tbl.name)}
              <tr
                class="u-cursor-pointer"
                tabindex="0"
                role="link"
                aria-label={`Open ${tbl.name}`}
                onclick={() => openTable(tbl)}
                ondblclick={() => openTableRows(tbl)}
                onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); openTable(tbl) } }}
                title="Double-click to open rows"
              >
                <td class="num">{tbl.name}</td>
                <td class="num u-color-mute">{tbl.rowEstimate ?? ''}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    </section>
    <section class="section">
      <h2 class="section__title">{t('schema.objects.views')}</h2>
      <div class="schema__note">{data.views.length} view(s)</div>
    </section>
    <section class="section">
      <h2 class="section__title">{t('schema.objects.functions')}</h2>
      <div class="schema__note">{data.functions.length} function(s)</div>
    </section>
  {/if}
</div>

<style>
  .schema__note { color: var(--text-dim); font-size: var(--fs-body); }
</style>

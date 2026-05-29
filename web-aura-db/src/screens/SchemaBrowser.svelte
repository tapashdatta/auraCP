<script>
  import { routeState, navigate } from '../lib/router.svelte.js'
  import { api } from '../lib/api.js'
  import { t } from '../lib/strings.js'
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
    navigate(`/connections/${id}/schemas/${schema}/tables/${tbl.name}`)
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
        <div style="color:var(--text-dim);font-size:var(--fs-body)">No tables.</div>
      {:else}
        <table class="data">
          <tbody>
            {#each data.tables as tbl (tbl.name)}
              <tr onclick={() => openTable(tbl)} style="cursor:pointer">
                <td class="num">{tbl.name}</td>
                <td class="num" style="color:var(--text-mute)">{tbl.rowEstimate ?? ''}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    </section>
    <section class="section">
      <h2 class="section__title">{t('schema.objects.views')}</h2>
      <div style="color:var(--text-dim);font-size:var(--fs-body)">{data.views.length} view(s)</div>
    </section>
    <section class="section">
      <h2 class="section__title">{t('schema.objects.functions')}</h2>
      <div style="color:var(--text-dim);font-size:var(--fs-body)">{data.functions.length} function(s)</div>
    </section>
  {/if}
</div>

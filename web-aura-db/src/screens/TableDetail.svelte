<script>
  import { routeState, navigate } from '../lib/router.svelte.js'
  import { api } from '../lib/api.js'
  import { t } from '../lib/strings.js'
  import { openTab } from '../lib/workspaces.svelte.js'
  import Btn from '../lib/components/Btn.svelte'
  import LoadingPane from '../lib/components/LoadingPane.svelte'

  function openRows() {
    const path = `/connections/${id}/schemas/${schema}/tables/${table}/rows`
    openTab({ title: `${schema}.${table}`, path, icon: 'table' })
    navigate(path)
  }

  const id = $derived(routeState.params.id)
  const schema = $derived(routeState.params.schema)
  const table = $derived(routeState.params.table)

  let loading = $state(true)
  /** @type {{columns:any[], indices:any[], ddl:string}} */
  let meta = $state({ columns: [], indices: [], ddl: '' })
  let activeTab = $state('columns') // 'columns' | 'indices' | 'ddl'

  $effect(() => {
    if (!id || !schema || !table) return
    loading = true
    api.getTable(id, schema, table).then((r) => {
      meta = {
        columns: r?.columns || [],
        indices: r?.indices || [],
        ddl: r?.ddl || '',
      }
    }).catch(() => { /* shell-stage */ }).finally(() => { loading = false })
  })
</script>

<div class="pane">
  <header class="pane__head">
    <h1 class="pane__title">{t('table.title', { table })}</h1>
    <span class="u-spacer"></span>
    <Btn variant="primary" onclick={openRows}>{t('rows.title')}</Btn>
  </header>

  <div class="tabledetail__tabs">
    {#each [['columns', t('table.tab.columns')], ['indices', t('table.tab.indices')], ['ddl', t('table.tab.ddl')]] as [k, label] (k)}
      <button
        type="button"
        class="topnav__btn {activeTab === k ? 'topnav__btn--active' : ''}"
        aria-pressed={activeTab === k}
        onclick={() => activeTab = k}
      >{label}</button>
    {/each}
  </div>

  {#if loading}
    <LoadingPane />
  {:else if activeTab === 'columns'}
    <table class="data">
      <thead><tr><th>Name</th><th>Type</th><th>Nullable</th><th>Default</th></tr></thead>
      <tbody>
        {#each meta.columns as col (col.name)}
          <tr>
            <td class="num">{col.name}</td>
            <td class="num u-color-dim">{col.type}</td>
            <td class="num">{col.nullable ? 'yes' : 'no'}</td>
            <td class="num u-color-mute">{col.default ?? '—'}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else if activeTab === 'indices'}
    <table class="data">
      <thead><tr><th>Name</th><th>Columns</th><th>Unique</th></tr></thead>
      <tbody>
        {#each meta.indices as idx (idx.name)}
          <tr>
            <td class="num">{idx.name}</td>
            <td class="num">{(idx.columns || []).join(', ')}</td>
            <td class="num">{idx.unique ? 'yes' : 'no'}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <pre class="code">{meta.ddl}</pre>
  {/if}
</div>

<style>
  .tabledetail__tabs {
    display: flex;
    gap: 0;
    border-bottom: 1px solid var(--border);
    margin-bottom: 16px;
  }
</style>

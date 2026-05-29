<script>
  import { routeState } from '../lib/router.svelte.js'
  import EmptyState from '../lib/components/EmptyState.svelte'
  import { t } from '../lib/strings.js'

  const id = $derived(routeState.params.id)
  const schema = $derived(routeState.params.schema)
  const table = $derived(routeState.params.table)
</script>

<div class="pane">
  <header class="pane__head">
    <h1 class="pane__title">{t('rows.title')}</h1>
    <span class="pane__subtitle">{schema}.{table} · conn {id}</span>
  </header>
  <EmptyState
    title={t('rows.placeholder.title')}
    body={t('rows.placeholder.body')}
  />
</div>

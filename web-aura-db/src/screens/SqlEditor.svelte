<script>
  import { routeState } from '../lib/router.svelte.js'
  import EmptyState from '../lib/components/EmptyState.svelte'
  import { t } from '../lib/strings.js'

  const id = $derived(routeState.params.id)
</script>

<div class="pane">
  <header class="pane__head">
    <h1 class="pane__title">{t('sql.title')}</h1>
    <span class="pane__subtitle">conn {id}</span>
  </header>
  <EmptyState
    title={t('sql.placeholder.title')}
    body={t('sql.placeholder.body')}
  />
</div>

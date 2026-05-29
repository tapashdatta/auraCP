<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { t } from '../lib/strings.js'
  import TextField from '../lib/components/TextField.svelte'
  import EmptyState from '../lib/components/EmptyState.svelte'
  import LoadingPane from '../lib/components/LoadingPane.svelte'
  import CodePreview from '../lib/components/CodePreview.svelte'

  let query = $state('')
  let loading = $state(false)
  /** @type {any[]} */
  let results = $state([])

  async function run() {
    loading = true
    try {
      const r = await api.searchHistory({ q: query })
      results = Array.isArray(r) ? r : (r?.items || [])
    } catch { results = [] }
    finally { loading = false }
  }

  onMount(run)
</script>

<div class="pane">
  <header class="pane__head">
    <h1 class="pane__title">{t('history.title')}</h1>
  </header>

  <div style="max-width:480px;margin-bottom:16px">
    <TextField bind:value={query} placeholder={t('history.search.placeholder')} mono />
  </div>

  {#if loading}
    <LoadingPane />
  {:else if results.length === 0}
    <EmptyState title={t('history.empty.title')} body={t('history.empty.body')} />
  {:else}
    {#each results as r (r.id)}
      <div class="section">
        <div style="display:flex;gap:8px;align-items:baseline;margin-bottom:6px">
          <span class="num" style="color:var(--text-mute);font-size:var(--fs-meta)">{r.ranAt || ''}</span>
          <span class="num" style="color:var(--text-dim);font-size:var(--fs-meta)">{r.connection || ''}</span>
          <span class="num" style="color:var(--text-mute);font-size:var(--fs-meta)">{r.durationMs ?? ''}ms</span>
        </div>
        <CodePreview code={r.sql || ''} />
      </div>
    {/each}
  {/if}
</div>

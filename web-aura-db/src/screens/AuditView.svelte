<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { connections } from '../lib/connections.svelte.js'
  import { t } from '../lib/strings.js'
  import SelectField from '../lib/components/SelectField.svelte'
  import EmptyState from '../lib/components/EmptyState.svelte'
  import LoadingPane from '../lib/components/LoadingPane.svelte'

  let connId = $state('')
  /** @type {any[]} */
  let events = $state([])
  let loading = $state(false)

  const options = $derived(connections.list.map((c) => ({ value: c.id, label: c.name })))

  async function load() {
    if (!connId) { events = []; return }
    loading = true
    try {
      const r = await api.audit(connId)
      events = Array.isArray(r) ? r : (r?.items || [])
    } catch { events = [] }
    finally { loading = false }
  }

  onMount(() => {
    if (connections.list.length > 0) {
      connId = connections.list[0].id
      load()
    }
  })

  $effect(() => { void connId; load() })
</script>

<div class="pane">
  <header class="pane__head">
    <h1 class="pane__title">{t('audit.title')}</h1>
    <span class="u-spacer"></span>
    <div class="u-min-w-240">
      <SelectField bind:value={connId} options={options} placeholder="Choose a connection" />
    </div>
  </header>

  {#if loading}
    <LoadingPane />
  {:else if events.length === 0}
    <EmptyState title={t('audit.empty.title')} body={t('audit.empty.body')} />
  {:else}
    <table class="data">
      <thead><tr><th>Time</th><th>Actor</th><th>Action</th><th>Detail</th></tr></thead>
      <tbody>
        {#each events as e (e.id)}
          <tr>
            <td class="num u-color-mute">{e.at || ''}</td>
            <td class="num">{e.actor || ''}</td>
            <td class="num u-color-dim">{e.action || ''}</td>
            <td class="num u-color-mute">{e.detail || ''}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</div>

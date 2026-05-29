<script>
  import { connections } from '../lib/connections.svelte.js'
  import { navigate } from '../lib/router.svelte.js'
  import { t } from '../lib/strings.js'
  import Btn from '../lib/components/Btn.svelte'
  import StatusDot from '../lib/components/StatusDot.svelte'
  import EmptyState from '../lib/components/EmptyState.svelte'
  import LoadingPane from '../lib/components/LoadingPane.svelte'
</script>

<div class="pane">
  <header class="pane__head">
    <h1 class="pane__title">{t('conn.list.title')}</h1>
    <span class="pane__spacer" style="flex:1"></span>
    <Btn variant="primary" onclick={() => navigate('/connections/new')}>{t('conn.list.action.new')}</Btn>
  </header>

  {#if connections.loading}
    <LoadingPane />
  {:else if connections.list.length === 0}
    <EmptyState
      title={t('conn.list.empty.title')}
      body={t('conn.list.empty.body')}
      action={{ label: t('conn.list.action.new'), onClick: () => navigate('/connections/new') }}
    />
  {:else}
    <table class="data">
      <thead>
        <tr>
          <th style="width:24px"></th>
          <th>{t('conn.list.col.name')}</th>
          <th>{t('conn.list.col.engine')}</th>
          <th>{t('conn.list.col.host')}</th>
          <th>{t('conn.list.col.lastUsed')}</th>
        </tr>
      </thead>
      <tbody>
        {#each connections.list as c (c.id)}
          <tr onclick={() => navigate(`/connections/${c.id}`)} style="cursor:pointer">
            <td><StatusDot state={c.status || 'idle'} /></td>
            <td>{c.name}</td>
            <td class="num" style="color:var(--text-dim)">{c.engine}</td>
            <td class="num" style="color:var(--text-mute)">{c.host || ''}{c.port ? ':' + c.port : ''}</td>
            <td class="num" style="color:var(--text-mute)">{c.lastUsed || '—'}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</div>

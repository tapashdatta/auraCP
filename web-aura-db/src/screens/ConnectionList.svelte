<script>
  import { connections } from '../lib/connections.svelte.js'
  import { navigate } from '../lib/router.svelte.js'
  import { t } from '../lib/strings.js'
  import Btn from '../lib/components/Btn.svelte'
  import StatusDot from '../lib/components/StatusDot.svelte'
  import EmptyState from '../lib/components/EmptyState.svelte'
  import LoadingPane from '../lib/components/LoadingPane.svelte'

  function activateRow(c, e) {
    if (e?.type === 'keydown' && e.key !== 'Enter' && e.key !== ' ') return
    if (e?.type === 'keydown') e.preventDefault()
    navigate(`/connections/${c.id}`)
  }
</script>

<div class="pane">
  <header class="pane__head">
    <h1 class="pane__title">{t('conn.list.title')}</h1>
    <span class="pane__spacer u-spacer"></span>
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
          <th class="connlist__col-dot"></th>
          <th>{t('conn.list.col.name')}</th>
          <th>{t('conn.list.col.engine')}</th>
          <th>{t('conn.list.col.host')}</th>
          <th>{t('conn.list.col.lastUsed')}</th>
        </tr>
      </thead>
      <tbody>
        {#each connections.list as c (c.id)}
          <!-- FIX (PR #11 a11y-17): rows are clickable; expose keyboard
               activation by giving each row tabindex=0 + role=link and
               handling Enter/Space. Also expose c.name as the
               accessible name and the engine/host as supplementary
               text via aria-label so SR doesn't have to walk cells. -->
          <tr
            class="u-cursor-pointer"
            tabindex="0"
            role="link"
            aria-label={`${c.name}, ${c.engine}${c.host ? ', ' + c.host : ''}`}
            onclick={() => activateRow(c)}
            onkeydown={(e) => activateRow(c, e)}
          >
            <td><StatusDot state={c.status || 'idle'} title={c.status || 'idle'} /></td>
            <!-- FIX (PR #11 a11y-12): wrap long names in a title= so a
                 truncated cell still surfaces the full text on hover. -->
            <td title={c.name}>{c.name}</td>
            <td class="num u-color-dim">{c.engine}</td>
            <td class="num u-color-mute">{c.host || ''}{c.port ? ':' + c.port : ''}</td>
            <td class="num u-color-mute">{c.lastUsed || '—'}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</div>

<style>
  .connlist__col-dot { width: 24px; }
</style>

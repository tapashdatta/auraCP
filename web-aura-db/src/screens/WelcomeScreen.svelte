<script>
  import { connections } from '../lib/connections.svelte.js'
  import { navigate } from '../lib/router.svelte.js'
  import { t } from '../lib/strings.js'
  import Btn from '../lib/components/Btn.svelte'
  import KbdHint from '../lib/components/KbdHint.svelte'
  import StatusDot from '../lib/components/StatusDot.svelte'

  const recent = $derived(connections.list.slice(0, 5))
</script>

<div class="pane">
  <header class="pane__head">
    <h1 class="pane__title">{t('welcome.title')}</h1>
    <span class="pane__subtitle">{t('welcome.subtitle')}</span>
  </header>

  <section class="section">
    <h2 class="section__title">{t('welcome.recent')}</h2>
    {#if recent.length === 0}
      <p style="color:var(--text-dim);font-size:var(--fs-body)">{t('tree.empty.body')}</p>
    {:else}
      <table class="data">
        <tbody>
          {#each recent as c (c.id)}
            <tr onclick={() => navigate(`/connections/${c.id}`)} style="cursor:pointer">
              <td style="width:10px"><StatusDot state={c.status || 'idle'} /></td>
              <td>{c.name}</td>
              <td class="num" style="color:var(--text-dim)">{c.engine}</td>
              <td class="num" style="color:var(--text-mute)">{c.host || ''}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
    <div style="margin-top:16px">
      <Btn variant="primary" onclick={() => navigate('/connections/new')}>{t('welcome.cta')}</Btn>
    </div>
  </section>

  <section class="section">
    <h2 class="section__title">{t('welcome.cheatsheet.title')}</h2>
    <table class="data" style="max-width:380px">
      <tbody>
        <tr><td>{t('welcome.cheatsheet.search')}</td><td style="text-align:right"><KbdHint keys={['⌘', 'K']} /></td></tr>
        <tr><td>{t('welcome.cheatsheet.closeTab')}</td><td style="text-align:right"><KbdHint keys={['⌘', 'W']} /></td></tr>
      </tbody>
    </table>
  </section>
</div>

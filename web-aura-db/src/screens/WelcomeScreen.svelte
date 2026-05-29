<script>
  import { connections } from '../lib/connections.svelte.js'
  import { navigate } from '../lib/router.svelte.js'
  import { t } from '../lib/strings.js'
  import Btn from '../lib/components/Btn.svelte'
  import KbdHint from '../lib/components/KbdHint.svelte'
  import StatusDot from '../lib/components/StatusDot.svelte'

  const recent = $derived(connections.list.slice(0, 5))

  function activateRow(c, e) {
    if (e?.type === 'keydown' && e.key !== 'Enter' && e.key !== ' ') return
    if (e?.type === 'keydown') e.preventDefault()
    navigate(`/connections/${c.id}`)
  }
</script>

<div class="pane">
  <header class="pane__head">
    <h1 class="pane__title">{t('welcome.title')}</h1>
    <span class="pane__subtitle">{t('welcome.subtitle')}</span>
  </header>

  <section class="section">
    <h2 class="section__title">{t('welcome.recent')}</h2>
    {#if recent.length === 0}
      <p class="welcome__hint">{t('tree.empty.body')}</p>
    {:else}
      <table class="data">
        <tbody>
          {#each recent as c (c.id)}
            <tr
              class="u-cursor-pointer"
              tabindex="0"
              role="link"
              aria-label={`${c.name}, ${c.engine}`}
              onclick={() => activateRow(c)}
              onkeydown={(e) => activateRow(c, e)}
            >
              <td class="welcome__col-dot"><StatusDot state={c.status || 'idle'} /></td>
              <td title={c.name}>{c.name}</td>
              <td class="num u-color-dim">{c.engine}</td>
              <td class="num u-color-mute">{c.host || ''}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
    <div class="u-mt-4">
      <Btn variant="primary" onclick={() => navigate('/connections/new')}>{t('welcome.cta')}</Btn>
    </div>
  </section>

  <section class="section">
    <h2 class="section__title">{t('welcome.cheatsheet.title')}</h2>
    <table class="data welcome__cheats">
      <tbody>
        <tr><td>{t('welcome.cheatsheet.search')}</td><td class="u-text-right"><KbdHint keys={['⌘', 'K']} /></td></tr>
        <tr><td>{t('welcome.cheatsheet.closeTab')}</td><td class="u-text-right"><KbdHint keys={['⌘', 'W']} /></td></tr>
      </tbody>
    </table>
  </section>
</div>

<style>
  .welcome__hint { color: var(--text-dim); font-size: var(--fs-body); }
  .welcome__col-dot { width: 10px; }
  .welcome__cheats { max-width: 380px; }
</style>

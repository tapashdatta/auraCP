<script>
  import { theme, setTheme } from '../lib/theme.svelte.js'
  import { t } from '../lib/strings.js'
  import Btn from '../lib/components/Btn.svelte'
  import Toggle from '../lib/components/Toggle.svelte'

  // theme value mapped to a bindable boolean toggle (true = light).
  let isLight = $state(theme.value === 'light')
  $effect(() => { setTheme(isLight ? 'light' : 'dark') })
</script>

<div class="pane">
  <header class="pane__head">
    <h1 class="pane__title">{t('account.title')}</h1>
  </header>

  <section class="section">
    <h2 class="section__title">{t('account.session')}</h2>
    <Btn variant="danger" onclick={() => { location.href = '/logout' }}>{t('account.signout')}</Btn>
  </section>

  <section class="section">
    <h2 class="section__title">{t('account.theme')}</h2>
    <Toggle bind:value={isLight} label={isLight ? t('nav.theme.light') : t('nav.theme.dark')} />
  </section>

  <section class="section">
    <h2 class="section__title">{t('account.stepup')}</h2>
    <div style="color:var(--text-dim);font-size:var(--fs-body)">{t('account.stepup.notEnrolled')}</div>
  </section>
</div>

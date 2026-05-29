<script>
  import { t } from '../lib/strings.js'
  import Btn from '../lib/components/Btn.svelte'

  function signIn() {
    const ret = '/dbadmin/' + (typeof location !== 'undefined' ? (location.hash || '') : '')
    location.href = '/login?next=' + encodeURIComponent(ret)
  }
</script>

<div class="auth-gate">
  <div class="auth-gate__card">
    <h2 class="auth-gate__title">{t('auth.title')}</h2>
    <p class="auth-gate__body">{t('auth.body')}</p>
    <Btn variant="primary" onclick={signIn}>{t('auth.action')}</Btn>
  </div>
</div>

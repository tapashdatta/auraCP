<script>
  import { t } from '../lib/strings.js'
  import Btn from '../lib/components/Btn.svelte'

  // FIX (PR #11 OPEN-REDIRECT-NEXT-PARAM): we used to build the `next`
  // query param from location.hash without validating the resulting path.
  // The panel `/login` endpoint already enforces an internal-only
  // allowlist (PR #11 INT-2), but defense-in-depth: only forward the
  // hash when it begins with `#/` so callers can never coax a host or
  // scheme through. Same effect as old code for normal use; rejects
  // anything that doesn't start with `#/`.
  function safeNext() {
    if (typeof location === 'undefined') return '/dbadmin/'
    const h = location.hash || ''
    if (h && !h.startsWith('#/')) return '/dbadmin/'
    return '/dbadmin/' + h
  }

  function signIn() {
    const ret = safeNext()
    location.href = '/login?next=' + encodeURIComponent(ret)
  }
</script>

<div class="auth-gate">
  <div class="auth-gate__card">
    <!-- FIX (PR #11 dc-5): brand presence on the auth gate. -->
    <div class="auth-gate__brand" aria-hidden="false">
      <span class="auth-gate__monogram" aria-hidden="true">A</span>
      <span>{t('brand')}</span>
    </div>
    <h2 class="auth-gate__title">{t('auth.title')}</h2>
    <p class="auth-gate__body">{t('auth.body')}</p>
    <Btn variant="primary" onclick={signIn}>{t('auth.action')}</Btn>
  </div>
</div>

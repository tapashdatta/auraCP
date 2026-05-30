<script>
  import { routeState, navigate } from '../router.svelte.js'
  import { theme, toggleTheme } from '../theme.svelte.js'
  import { t } from '../strings.js'
  import { signOut } from '../signout.js'
  import DropdownMenu from './DropdownMenu.svelte'
  import Icon from './Icon.svelte'

  // Same nav buckets as the former top nav, now presented as the
  // prototype's vertical icon rail. `match` lists the route names that
  // light the bucket up. Audit may later be gated by RBAC.
  const navItems = [
    { id: 'connections', label: t('nav.connections'), path: '/connections', icon: 'database', match: ['conn.list', 'conn.detail', 'conn.new', 'schema', 'table', 'rows', 'welcome'] },
    { id: 'queries',     label: t('nav.queries'),     path: '/',             icon: 'terminal', match: ['query'] },
    { id: 'history',     label: t('nav.history'),     path: '/history',      icon: 'clock',    match: ['history'] },
    { id: 'audit',       label: t('nav.audit'),       path: '/audit',        icon: 'shield',   match: ['audit'] },
  ]

  let userOpen = $state(false)
  /** @type {HTMLElement|undefined} */
  let userBtn = $state(undefined)

  const menuItems = $derived([
    { label: t('nav.account'), onSelect: () => navigate('/account') },
    { label: t('nav.signout'), onSelect: signOut, tone: /** @type {const} */ ('danger') },
  ])

  const themeLabel = $derived(t('nav.theme', { value: theme.value === 'dark' ? t('nav.theme.dark') : t('nav.theme.light') }))

  function isActive(item) {
    return item.match.includes(routeState.name)
  }
</script>

<!-- FIX (PR #11 a11y-19): each rail button exposes aria-current="page"
     for the active section, plus title + aria-label so the icon-only
     buttons are legible to pointer and SR users alike. -->
<nav class="rail" aria-label="Primary">
  <button
    type="button"
    class="rail__logo"
    onclick={() => navigate('/connections')}
    title={t('brand')}
    aria-label={t('brand')}
  >A</button>

  <div class="rail__divider"></div>

  <div class="rail__nav">
    {#each navItems as item (item.id)}
      <button
        type="button"
        class="rail__btn {isActive(item) ? 'rail__btn--active' : ''}"
        aria-current={isActive(item) ? 'page' : undefined}
        title={item.label}
        aria-label={item.label}
        onclick={() => navigate(item.path)}
      >
        <Icon name={item.icon} size={20} />
      </button>
    {/each}
  </div>

  <div class="rail__spacer"></div>

  <button
    type="button"
    class="rail__btn"
    title={themeLabel}
    aria-label={themeLabel}
    onclick={toggleTheme}
  >
    <Icon name={theme.value === 'dark' ? 'sun' : 'moon'} size={20} />
  </button>

  <button
    type="button"
    class="rail__avatar"
    bind:this={userBtn}
    onclick={() => (userOpen = !userOpen)}
    aria-haspopup="menu"
    aria-expanded={userOpen}
    aria-label="User menu"
  >A</button>
  <DropdownMenu bind:open={userOpen} items={menuItems} anchor={userBtn} />
</nav>

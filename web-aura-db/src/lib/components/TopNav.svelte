<script>
  import { routeState, navigate } from '../router.svelte.js'
  import { theme, toggleTheme } from '../theme.svelte.js'
  import { t } from '../strings.js'
  import { signOut } from '../signout.js'
  import DropdownMenu from './DropdownMenu.svelte'

  /** Top-level nav buckets. Audit may be hidden by RBAC later. */
  const navItems = [
    { id: 'connections', label: t('nav.connections'), path: '/connections', match: ['conn.list', 'conn.detail', 'conn.new', 'schema', 'table', 'rows', 'welcome'] },
    { id: 'queries',     label: t('nav.queries'),     path: '/',             match: ['query'] },
    { id: 'history',     label: t('nav.history'),     path: '/history',      match: ['history'] },
    { id: 'audit',       label: t('nav.audit'),       path: '/audit',        match: ['audit'] },
  ]

  let userOpen = $state(false)
  /** @type {HTMLElement|undefined} */
  let userBtn = $state(undefined)

  const menuItems = $derived([
    { label: t('nav.account'),         onSelect: () => navigate('/account') },
    { label: t('nav.theme', { value: theme.value === 'dark' ? t('nav.theme.dark') : t('nav.theme.light') }), onSelect: toggleTheme },
    { label: t('nav.signout'),         onSelect: signOut, tone: /** @type {const} */ ('danger') },
  ])

  function isActive(item) {
    return item.match.includes(routeState.name)
  }
</script>

<!-- FIX (PR #11 a11y-19): each nav button now exposes aria-current="page"
     when it represents the active route, so SR users can hear which
     section they are in. -->
<header class="topnav">
  <span class="topnav__brand">{t('brand')}</span>
  <nav class="topnav__nav" aria-label="Primary">
    {#each navItems as item (item.id)}
      <button
        type="button"
        class="topnav__btn {isActive(item) ? 'topnav__btn--active' : ''}"
        aria-current={isActive(item) ? 'page' : undefined}
        onclick={() => navigate(item.path)}
      >{item.label}</button>
    {/each}
  </nav>
  <span class="topnav__spacer"></span>
  <button
    type="button"
    class="topnav__user"
    bind:this={userBtn}
    onclick={() => userOpen = !userOpen}
    aria-haspopup="menu"
    aria-expanded={userOpen}
    aria-label="User menu"
  >A</button>
  <DropdownMenu bind:open={userOpen} items={menuItems} anchor={userBtn} />
</header>

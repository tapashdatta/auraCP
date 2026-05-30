// Central UI state (Svelte 5 runes). Components import { ui } and mutate it.
// Hash-based routing keeps the URL in sync with navigation so that
// page refresh, copy-paste, and browser back/forward all work correctly.
//
// Hash format:
//   #/                   → sites list
//   #/detail/<dom>       → site detail (settings tab)
//   #/detail/<dom>/<tab> → site detail, specific tab
//   #/add                → add site
//   #/create/<type>      → create form
//   #/instance           → instance settings
//   #/account            → account
//   #/users              → admin users
export const ui = $state({
  view: 'sites',      // 'sites' | 'add' | 'create' | 'detail' | 'instance' | 'account' | 'users'
  createType: 'php',
  site: null,         // selected site for detail view
})

// Write a history entry. Used for view-level transitions (back/forward supported).
function push(hash) {
  history.pushState(null, '', hash)
}

// Replace the current history entry. Used for tab changes within a view
// (doesn't pollute the back-button stack).
function replace(hash) {
  history.replaceState(null, '', hash)
}

export function go(view) {
  ui.view = view
  window.scrollTo({ top: 0, behavior: 'instant' })
  const map = { sites: '#/', add: '#/add', instance: '#/instance', account: '#/account', users: '#/users' }
  push(map[view] ?? '#/')
}

export function openCreate(type) {
  ui.createType = type
  ui.view = 'create'
  window.scrollTo({ top: 0, behavior: 'instant' })
  push(`#/create/${encodeURIComponent(type)}`)
}

export function openSite(site) {
  ui.site = site
  ui.view = 'detail'
  window.scrollTo({ top: 0, behavior: 'instant' })
  push(`#/detail/${encodeURIComponent(site.domain)}`)
}

// Called by SiteDetail when the active tab changes — replaces the hash
// without creating a new history entry (tab switches aren't navigation).
export function setDetailTab(domain, tab) {
  const base = `#/detail/${encodeURIComponent(domain)}`
  replace(tab === 'settings' ? base : `${base}/${tab}`)
}

// Parse the current window.location.hash and return a route descriptor.
// Called on initial mount and on browser back/forward (popstate).
export function parseHash() {
  const raw = window.location.hash.replace(/^#\/?/, '')
  const parts = raw.split('/').filter(Boolean)
  const view = parts[0] || ''
  if (view === 'detail' && parts[1]) {
    return { view: 'detail', domain: decodeURIComponent(parts[1]), tab: parts[2] || 'settings' }
  }
  if (view === 'add')      return { view: 'add' }
  if (view === 'create')   return { view: 'create', createType: decodeURIComponent(parts[1] || 'php') }
  if (view === 'instance') return { view: 'instance' }
  if (view === 'account')  return { view: 'account' }
  if (view === 'users')    return { view: 'users' }
  return { view: 'sites' }
}

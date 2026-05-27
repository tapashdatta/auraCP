// Central UI state (Svelte 5 runes). Components import { ui } and mutate it.
// A real router comes later; for the prototype this drives screen navigation.
export const ui = $state({
  view: 'sites',      // 'sites' | 'add' | 'create' | 'detail'
  createType: 'php',  // which type the create form renders
  site: null,         // selected site for detail view
})

export function go(view) {
  ui.view = view
  window.scrollTo({ top: 0, behavior: 'instant' })
}

export function openCreate(type) {
  ui.createType = type
  go('create')
}

export function openSite(site) {
  ui.site = site
  go('detail')
}

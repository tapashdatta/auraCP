// Small cross-component UI state. Currently just the mobile sidebar
// drawer: the icon rail stays pinned at all widths (56px), but below
// 720px the connection/schema tree collapses into a slide-out drawer
// toggled by the burger in the breadcrumb topbar. AppShell mirrors
// `treeOpen` onto `.shell--tree-open` and clears it on every route
// change so navigating closes the drawer.
export const ui = $state({ treeOpen: false })

export function toggleTree() {
  ui.treeOpen = !ui.treeOpen
}

export function closeTree() {
  ui.treeOpen = false
}

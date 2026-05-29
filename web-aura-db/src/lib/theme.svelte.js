// Theme store. Dark is default; light is opt-in (the inverse of the panel).
// Persisted to localStorage under auracp_db_theme; the chosen theme is mirrored
// onto <html data-theme="..."> so the CSS variables in app.css switch atomically.

const KEY = 'auracp_db_theme'

/** @returns {'dark'|'light'} */
function readInitial() {
  if (typeof localStorage === 'undefined') return 'dark'
  const v = localStorage.getItem(KEY)
  return v === 'light' ? 'light' : 'dark'
}

export const theme = $state({ value: readInitial() })

function apply(v) {
  if (typeof document !== 'undefined') {
    if (v === 'light') document.documentElement.setAttribute('data-theme', 'light')
    else document.documentElement.removeAttribute('data-theme')
  }
}

apply(theme.value)

/** @param {'dark'|'light'} v */
export function setTheme(v) {
  theme.value = v
  if (typeof localStorage !== 'undefined') localStorage.setItem(KEY, v)
  apply(v)
}

export function toggleTheme() {
  setTheme(theme.value === 'dark' ? 'light' : 'dark')
}

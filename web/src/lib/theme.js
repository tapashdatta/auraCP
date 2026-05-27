// Theme: light default + dark, persisted to localStorage, applied to <html data-theme>.
const KEY = 'auracp-theme'

export function initTheme() {
  const saved = localStorage.getItem(KEY)
  const theme = saved || 'light'
  document.documentElement.setAttribute('data-theme', theme)
}

export function getTheme() {
  return document.documentElement.getAttribute('data-theme') || 'light'
}

export function toggleTheme() {
  const next = getTheme() === 'dark' ? 'light' : 'dark'
  document.documentElement.setAttribute('data-theme', next)
  localStorage.setItem(KEY, next)
  return next
}

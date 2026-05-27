// CSRF-aware fetch wrapper. Reads the readable auracp_csrf cookie and echoes it
// in the X-CSRF-Token header on state-changing requests (the server enforces it).
function csrfToken() {
  const m = document.cookie.match(/(?:^|;\s*)auracp_csrf=([^;]+)/)
  return m ? decodeURIComponent(m[1]) : ''
}

export async function apiFetch(path, opts = {}) {
  const method = (opts.method || 'GET').toUpperCase()
  const headers = { ...(opts.headers || {}) }
  if (opts.body && !headers['Content-Type']) headers['Content-Type'] = 'application/json'
  if (method !== 'GET' && method !== 'HEAD') headers['X-CSRF-Token'] = csrfToken()
  return fetch(path, { ...opts, headers })
}

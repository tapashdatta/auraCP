// Session presence heuristic. The panel owns the actual session — Aura DB only
// observes whether the auracp_csrf cookie is present at boot. The first 401
// from /api/dbadmin redirects to /login (handled inside api.js).

export const session = $state({
  /** True only if the auracp_csrf cookie was readable at boot. */
  hasCookie: hasCsrfCookie(),
})

function hasCsrfCookie() {
  if (typeof document === 'undefined') return false
  return /(?:^|;\s*)auracp_csrf=[^;]+/.test(document.cookie)
}

/** Re-read the cookie (e.g. after the user returns from /login). */
export function refreshSession() {
  session.hasCookie = hasCsrfCookie()
}

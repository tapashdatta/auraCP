// Session presence heuristic. The panel owns the actual session — Aura DB
// only observes whether the auracp_csrf cookie is present at boot. The
// first 401 from /api/dbadmin redirects to /login (handled inside api.js).
//
// FIX (PR #11 INT-5): the old comment claimed `hasCookie` was meaningful
// session state; it isn't — a cookie can be present and invalid, or the
// browser may strip it on cross-origin proxy. The flag is intentionally
// a low-confidence shortcut to skip the first round-trip when we know
// the user has *some* csrf token. The canonical session state is the
// 401 response from /api/dbadmin/* (api.js calls navigate('/401') on
// CSRF rejection). Do not rely on this for security decisions.

export const session = $state({
  /** Best-effort: true only if the auracp_csrf cookie was readable at
   * boot. Not a real session probe — see file header. */
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

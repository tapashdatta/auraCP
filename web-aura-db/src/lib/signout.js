// FIX-7 (PR #11): Sign Out handler. Lives in its own module so vitest can
// import + assert on the fetch call without rendering TopNav.svelte.
//
// The previous implementation did `location.href = '/logout'` — a GET to
// the wrong path. The panel's actual logout route is POST /api/auth/logout
// and demands the X-CSRF-Token header. Without this fix, clicking Sign
// Out either 404'd (panel returns the SPA shell on GET /logout) or, if
// the SPA shell silently consumed the URL, simply did nothing while
// leaving the session cookie intact.

/**
 * Sign the operator out of the panel and redirect them to /login.
 *
 * Errors are swallowed deliberately: even if the network call fails we
 * still want to land on /login (the server will eventually expire the
 * session, and the user wants out NOW).
 *
 * @returns {Promise<void>}
 */
export async function signOut() {
  try {
    const m = document.cookie.match(/(?:^|;\s*)auracp_csrf=([^;]+)/)
    const csrf = m ? decodeURIComponent(m[1]) : ''
    await fetch('/api/auth/logout', {
      method: 'POST',
      credentials: 'same-origin',
      headers: csrf ? { 'X-CSRF-Token': csrf } : {},
    })
  } catch {
    /* swallow — still navigate to /login below */
  } finally {
    if (typeof location !== 'undefined') {
      location.href = '/login'
    }
  }
}

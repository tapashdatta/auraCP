/*!
 * auraCP — Adminer bootstrap (companion to adminer.css)
 *
 * v0.2.53: trimmed to just theme persistence. The new Adminer 5.x
 * adminer.css is self-contained — no chrome injection, no login-shell
 * wrap, no theme-toggle UI. All we need is to apply the persisted
 * theme attribute on <html> BEFORE first paint so there's no flash.
 *
 * Operators flip theme via DevTools:
 *   localStorage.setItem('acp.adminer.theme', 'light')   // or 'dark'
 *   location.reload()
 *
 * A real toggle UI can land in v0.2.54+ once we know the Adminer 5.x
 * layout has a stable spot for it.
 */
(function () {
  var KEY = 'acp.adminer.theme';
  try {
    var t = localStorage.getItem(KEY);
    if (t !== 'light' && t !== 'dark') t = 'dark';
    document.documentElement.setAttribute('data-acp-theme', t);
  } catch (e) {
    document.documentElement.setAttribute('data-acp-theme', 'dark');
  }
})();

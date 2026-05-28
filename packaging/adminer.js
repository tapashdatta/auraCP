/*!
 * auraCP — Adminer bootstrap (companion to adminer.css)
 * --------------------------------------------------------
 *  - Applies [data-acp-theme="dark|light"] on <html> from localStorage
 *    BEFORE first paint so there's no flash.
 *  - Adds brand chrome (mark + label) into Adminer's #menu h1.
 *  - Appends a theme-toggle button + footer note to #menu.
 *  - Wraps the bare login form in .acp-login-shell + .acp-login-card.
 *
 * Loaded by the SSO wrapper (index.php) via a head() override on the
 * Adminer subclass — Adminer 4.x doesn't auto-include adminer.js the
 * way it does adminer.css.
 */
(function () {
  var KEY = 'acp.adminer.theme';

  function getTheme() {
    try { return localStorage.getItem(KEY) || 'dark'; }
    catch (e) { return 'dark'; }
  }
  function setTheme(t) {
    document.documentElement.setAttribute('data-acp-theme', t);
    try { localStorage.setItem(KEY, t); } catch (e) {}
  }

  // Apply early — script is loaded with `defer` so this still runs before
  // DOMContentLoaded but after <html> is parsed. No paint has happened yet.
  setTheme(getTheme());

  function el(tag, cls, html) {
    var n = document.createElement(tag);
    if (cls) n.className = cls;
    if (html != null) n.innerHTML = html;
    return n;
  }

  function decorateMenu() {
    var menu = document.getElementById('menu');
    if (!menu) return;

    // Brand chrome — replace the plain "Adminer" anchor in #h1
    var h1 = menu.querySelector('h1');
    if (h1 && !h1.querySelector('.acp-brand')) {
      var existing = h1.innerHTML;
      h1.innerHTML = '';
      var brand = el('span', 'acp-brand');
      brand.appendChild(el('span', 'acp-mark', 'a'));
      brand.appendChild(el('span', null,
        'auraCP <span class="version">Adminer</span>'));
      h1.appendChild(brand);
      // Preserve Adminer's logout form / version link that lived in h1
      if (/<\/?(form|a)\b/i.test(existing)) {
        var hold = document.createElement('span');
        hold.style.cssText = 'margin-left:auto;font-weight:400;font-size:11px';
        hold.innerHTML = existing;
        h1.appendChild(hold);
      }
    }

    // Theme toggle — only once
    if (!menu.querySelector('.acp-theme-toggle')) {
      var btn = el('button', 'acp-theme-toggle');
      btn.type = 'button';
      btn.appendChild(el('span', 'acp-theme-toggle-icon'));
      var label = el('span', 'acp-theme-label');
      btn.appendChild(label);
      function refresh() {
        var t = getTheme();
        label.textContent = (t === 'dark') ? 'Switch to light' : 'Switch to dark';
      }
      btn.addEventListener('click', function () {
        setTheme(getTheme() === 'dark' ? 'light' : 'dark');
        refresh();
      });
      refresh();
      menu.appendChild(btn);

      var note = el('div', 'acp-footer-note',
        'Adminer · vendored by auraCP');
      menu.appendChild(note);
    }
  }

  function decorateLogin() {
    if (document.getElementById('menu')) return;          // not a login screen
    if (document.querySelector('.acp-login-shell')) return;
    var form = document.querySelector('form');
    if (!form || !form.querySelector('input[name="auth[username]"]')) return;

    var shell = el('div', 'acp-login-shell');
    var card = el('div', 'acp-login-card');
    var header = el('div', 'acp-login-header');
    header.appendChild(el('span', 'acp-mark', 'a'));
    var hdrText = document.createElement('div');
    hdrText.appendChild(el('h1', 'acp-login-title', 'auraCP Adminer'));
    hdrText.appendChild(el('p',  'acp-login-sub',  'Sign in to a managed database.'));
    header.appendChild(hdrText);
    card.appendChild(header);

    form.parentNode.insertBefore(shell, form);
    shell.appendChild(card);
    card.appendChild(form);
  }

  function run() {
    decorateMenu();
    decorateLogin();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', run);
  } else {
    run();
  }
})();

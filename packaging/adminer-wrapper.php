<?php
/*
 * auraCP — Adminer SSO wrapper (v0.2.31 redesign)
 *
 * Previous design tried to set $_SESSION['creds'] then include adminer.php and
 * have a subclass read them. Adminer calls session_name("Adminer") and starts
 * its own session, which clobbers ours — credentials() in our subclass saw
 * an empty session, so the wrapper always landed on "No active panel session".
 *
 * New flow uses Adminer's OWN authentication. On ?sso=<token>:
 *   1. Read + delete the SSO token (single-use).
 *   2. Render a tiny auto-submitting HTML form that POSTs to /_adminer/ with
 *      auth[driver/server/username/password/db] populated from the token.
 *   3. Adminer's own index.php receives the POST, validates the creds against
 *      the engine, sets its session, redirects to the database browse view.
 *
 * Net effect: operator clicks Manage in the panel, opens a tab, sees Adminer
 * already logged into the right database. No login form, no manual typing.
 * Password is in the form body for one POST over HTTPS; never in the URL.
 *
 * If a request hits /_adminer/ without an SSO token AND without an active
 * Adminer session, we render a friendly "click Manage" page rather than
 * Adminer's bare login form (which would invite operators to type a server
 * password). Adminer's auth is bypassed only via the SSO POST flow.
 */

if (!empty($_GET['sso'])) {
    $tok = preg_replace('/[^A-Za-z0-9_-]/', '', $_GET['sso']);
    if (strlen($tok) < 16 || strlen($tok) > 96) {
        http_response_code(400);
        exit("invalid sso token");
    }
    $path = '/run/auracp/adminer-sso/' . $tok;
    if (!is_file($path)) {
        http_response_code(403);
        exit("SSO token not found. Click Manage again from the panel.");
    }
    $raw = file_get_contents($path);
    @unlink($path); // single-use, regardless of validity below
    $data = json_decode($raw, true);
    if (!$data || empty($data['user']) || !isset($data['password'])
            || empty($data['name']) || empty($data['engine'])
            || empty($data['expires']) || $data['expires'] < time()) {
        http_response_code(403);
        exit("SSO token expired. Click Manage again from the panel.");
    }
    $driver = ($data['engine'] === 'postgres') ? 'pgsql' : 'server';
    // Auto-submit the credentials to Adminer's own auth handler. Adminer
    // accepts auth[driver|server|username|password|db|permanent] on its
    // root path; on success it sets its own session + redirects.
    $h = function ($s) { return htmlspecialchars((string)$s, ENT_QUOTES, 'UTF-8'); };
    ?><!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <title>auraCP — opening Adminer…</title>
  <style>
    body{font-family:system-ui,-apple-system,sans-serif;background:#0f1014;color:#9aa4b6;
         margin:0;display:flex;align-items:center;justify-content:center;height:100vh}
    .box{text-align:center;font-size:14px}
    .spin{width:22px;height:22px;border:2px solid #2d343f;border-top-color:#38e3a3;
          border-radius:50%;animation:s 600ms linear infinite;margin:0 auto 12px}
    @keyframes s{to{transform:rotate(360deg)}}
  </style>
</head>
<body>
  <div class="box">
    <div class="spin"></div>
    Opening Adminer…
  </div>
  <form id="auracp-auth" method="POST" action="/_adminer/" autocomplete="off">
    <input type="hidden" name="auth[driver]"    value="<?= $h($driver) ?>">
    <input type="hidden" name="auth[server]"    value="localhost">
    <input type="hidden" name="auth[username]"  value="<?= $h($data['user']) ?>">
    <input type="hidden" name="auth[password]"  value="<?= $h($data['password']) ?>">
    <input type="hidden" name="auth[db]"        value="<?= $h($data['name']) ?>">
    <input type="hidden" name="auth[permanent]" value="0">
    <noscript>
      <p>JavaScript disabled. Click below to continue:</p>
      <button type="submit">Open Adminer</button>
    </noscript>
  </form>
  <script>document.getElementById('auracp-auth').submit();</script>
</body>
</html><?php
    exit;
}

// Refuse direct access to /_adminer/login or anywhere without an active
// Adminer session AND without an SSO token — the only legitimate entry
// point is the panel's Manage button. (Adminer would otherwise render its
// login form, which we don't want operators to ever see.)
//
// Detection: a POST to /_adminer/ with auth[*] fields is Adminer's own
// auth handler (we just dispatched it above) — let it through. A GET with
// no session cookie and no token is what we want to block.
$isAuthPost = ($_SERVER['REQUEST_METHOD'] === 'POST') && isset($_POST['auth']);
$hasSession = isset($_COOKIE['adminer_sid']) || isset($_COOKIE['adminer_key']);
if (!$isAuthPost && !$hasSession) {
    http_response_code(403);
    ?><!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <title>auraCP — Adminer</title>
  <style>
    body{font-family:system-ui,-apple-system,sans-serif;color:#333;
         max-width:520px;margin:80px auto;padding:0 20px;line-height:1.55}
    h1{font-size:18px;margin:0 0 12px}
    p{color:#555}
  </style>
</head>
<body>
  <h1>No active session</h1>
  <p>Return to the auraCP panel and click <b>Manage</b> next to the database you want to open.</p>
</body>
</html><?php
    exit;
}

// Active Adminer session — hand off to Adminer normally.
//
// v0.2.48: register an Adminer subclass that emits <script src="adminer.js">
// in the <head>. Adminer 4.x auto-loads adminer.css from its own directory
// but NOT adminer.js; the only stable injection point is the Adminer
// subclass's head() override. The subclass is defined inside the function
// body because the parent `Adminer` class only exists after adminer.php is
// included — `extends Adminer` at the top level would fail to parse.
function adminer_object() {
    return new class extends Adminer {
        function head(...$args) {
            parent::head(...$args);
            echo '<script src="adminer.js" defer></script>' . "\n";
        }
    };
}
include __DIR__ . '/adminer.php';

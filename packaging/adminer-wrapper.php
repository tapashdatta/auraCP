<?php
/*
 * auraCP — Adminer SSO wrapper
 *
 * Reads a one-time SSO token written by auracpd at
 * /run/auracp/adminer-sso/<token>. The token file holds the database
 * credentials in JSON. We unlink the file on first read (single-use),
 * store the creds in the PHP session, then hand off to Adminer with
 * an `adminer_object()` that returns those creds from `credentials()`.
 *
 * Adminer ships as a single file (adminer.php) next to this wrapper.
 * Operators never see a login form — they were already authenticated
 * by the panel before getting here.
 *
 * Security notes:
 * - Token files are written 0640 root:www-data so PHP-FPM (www-data) can
 *   read them; nothing else on the box can.
 * - PHP session cookie is HttpOnly + SameSite=Lax. Session lifetime is
 *   short (4h) — re-clicking Manage refreshes it.
 * - This file should be the ONLY entry point in /opt/auracp/adminer/.
 *   adminer.php is loaded via include(); a stray request to it directly
 *   bypasses our auth wrapper, so the installer keeps it 0600 and only
 *   readable by the FPM user via include().
 */

session_set_cookie_params([
    'lifetime' => 0,
    'path'     => '/_adminer/',
    'secure'   => !empty($_SERVER['HTTPS']),
    'httponly' => true,
    'samesite' => 'Lax',
]);
session_name('auracp_adminer');
session_start();

if (!empty($_GET['sso'])) {
    // Strict token shape: hex/alphanumeric only, length-bounded.
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
    $_SESSION['creds'] = [
        'driver' => ($data['engine'] === 'postgres') ? 'pgsql' : 'server',
        'server' => 'localhost',
        'user'   => $data['user'],
        'pass'   => $data['password'],
        'db'     => $data['name'],
    ];
    // Drop the sso query param so refresh doesn't re-read a deleted file.
    header('Location: /_adminer/');
    exit;
}

if (empty($_SESSION['creds'])) {
    http_response_code(403);
    echo "<!doctype html><html><head><meta charset=utf-8><title>auraCP — Adminer</title>";
    echo "<style>body{font-family:system-ui,sans-serif;color:#333;max-width:520px;margin:80px auto;padding:0 20px}h1{font-size:18px;margin:0 0 12px}p{line-height:1.55;color:#555}</style></head><body>";
    echo "<h1>No active session</h1>";
    echo "<p>Return to the auraCP panel and click <b>Manage</b> next to the database you want to open.</p>";
    echo "</body></html>";
    exit;
}

// Hand off to Adminer with our credentials pre-loaded. adminer_object() is
// Adminer's plugin hook — it must return an Adminer (or subclass) instance.
// adminer-plugins.php ends with `return new AuracpAdminer;` so require returns
// that instance directly.
function adminer_object() {
    return require __DIR__ . '/adminer-plugins.php';
}

include __DIR__ . '/adminer.php';

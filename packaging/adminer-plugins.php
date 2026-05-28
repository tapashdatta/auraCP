<?php
/*
 * auraCP — Adminer customisations
 *
 * Returns an Adminer subclass with our pre-auth wired into credentials()
 * and database(). Adminer's plugin system runs adminer_object() once per
 * request; we keep the override surface tiny.
 */

class AuracpAdminer extends Adminer {
    function name() {
        return 'auraCP — database manager';
    }

    function credentials() {
        $c = $_SESSION['creds'] ?? null;
        if (!$c) return ['', '', ''];
        return [$c['server'], $c['user'], $c['pass']];
    }

    function database() {
        $c = $_SESSION['creds'] ?? null;
        return $c ? $c['db'] : null;
    }

    function databases($flush = true) {
        // Limit the database picker to JUST the one this SSO token authorised
        // — operators shouldn't see / browse other tenants' databases even if
        // the engine user happened to have rights.
        $c = $_SESSION['creds'] ?? null;
        return $c ? [$c['db']] : [];
    }

    function login($login, $password) {
        // We already validated via the panel session; let Adminer use the
        // credentials we set in credentials() without showing the login form.
        return true;
    }

    function loginForm() {
        echo '<p style="padding:14px;color:#666">No active panel session. Return to the panel and click Manage.</p>';
    }

    function permanentLogin($create = false) {
        return false;   // sessions only; no 'remember me' cookies in Adminer
    }

    function homepage() {
        // Skip Adminer's homepage; jump straight to the database browse view.
        $c = $_SESSION['creds'] ?? null;
        if ($c) {
            header('Location: ?' . $c['driver'] . '=&username=' . urlencode($c['user'])
                . '&db=' . urlencode($c['db']));
            exit;
        }
        return true;
    }
}

return new AuracpAdminer;

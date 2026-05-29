# Migrating from Adminer to Aura DB

auraCP v0.2.x shipped Adminer as the bundled database manager UI. Adminer
ran under a dedicated PHP-FPM pool (`auracp-adminer`), was reverse-proxied
by nginx at `/_adminer/` on the panel domain, and was opened via a
one-time SSO token minted by `POST /api/sites/{domain}/databases/{engine}/{name}/manage`.

PR #17 (v0.3.0) removes Adminer entirely. **Aura DB** — auraCP's native
database administration tool, written in Go + Svelte and embedded inside
`auracpd` — is now the sole DB admin surface.

This document covers what changes for operators upgrading from v0.2.x.

---

## TL;DR

- The **Manage** button next to each database on the Site Detail page
  now opens **Aura DB**, not Adminer. It deep-links to the connection
  picker with the engine + database name pre-filled.
- Aura DB lives at `/dbadmin/` on the same panel domain you already use.
  No separate login: the panel session cookie carries through.
- `/_adminer/` returns 404. The bundled Adminer copy at
  `/opt/auracp/adminer/` is removed on upgrade.
- The `POST .../manage` endpoint still exists but returns a Aura DB
  URL (`/dbadmin/#/connections?engine=...&name=...`) instead of an
  SSO token. Existing API callers that just open the returned `url`
  continue to work; callers that parsed `?sso=` out of the URL will
  break.
- The PHP-FPM `auracp-adminer` pool and the `/etc/tmpfiles.d/auracp-adminer.conf`
  drop-in are removed automatically by the v0.3.0 installer and the
  Debian postinst.

---

## What's the same

- The list of databases per site, the create/delete actions, and the
  per-database credentials persisted in the panel store are unchanged.
- The encrypted password blob lives in the panel's existing SQLite
  store (`databases.password_enc`); the encryption key
  (`/etc/auracp/secret.key`) is unchanged.
- Permissions remain governed by the panel RBAC system. The
  `databases.read` permission gates the Manage button, just as it
  gated Adminer SSO before.

## What's different

| Topic                | Adminer (v0.2.x)                                    | Aura DB (v0.3.0)                                       |
| -------------------- | --------------------------------------------------- | ------------------------------------------------------ |
| Frontend             | PHP, served by FPM, behind `/_adminer/`             | Svelte SPA, embedded in `auracpd`, behind `/dbadmin/`  |
| Backend              | Adminer's PHP-internal MySQL/PG clients             | Go drivers in `pkg/dbadmin/driver`                     |
| Auth                 | SSO-token bridge (60s TTL) + Adminer PHP session    | Panel session cookie + `ResolveIdentity` directly      |
| SQL safety           | Adminer's permissive UI                             | AST-based classifier in `pkg/dbadmin/classifier`       |
| Audit                | None (Adminer logs only what the engine logs)       | SHA-256 hash-chained NDJSON + panel `audit_log` table  |
| Multi-user grants    | Adminer always runs as the site DB user             | Per-connection grants in `aura_db_grants`              |
| Open-basedir         | `/opt/auracp/adminer:/run/auracp/adminer-*:/tmp`    | n/a (no PHP)                                           |
| PHP-FPM pool         | `auracp-adminer` (`/run/php-fpm/auracp-adminer.sock`)| n/a                                                    |
| Encrypted password   | Decrypted server-side per click, written to token   | Stored in `aura_db_connections.creds_enc`              |
| Export               | Adminer's "Export" → SQL/CSV                        | `/api/dbadmin/export/...` → streaming CSV/NDJSON/SQL   |

---

## Upgrade path

The v0.3.0 installer and the .deb postinst both run a purge step
that removes:

- `/opt/auracp/adminer/` (the Adminer PHP wrapper + theme).
- `/etc/php/*/fpm/pool.d/auracp-adminer.conf` (the dedicated FPM pool).
- `/etc/tmpfiles.d/auracp-adminer.conf` (the SSO runtime-dir drop-in).
- `/run/auracp/adminer-sso/` and `/run/auracp/adminer-sessions/`
  (SSO tokens and Adminer PHP sessions, both tmpfs).

If you previously customised any of these files (uncommon — the
installer always re-wrote them), back them up before upgrading.

After upgrade:

1. The first time you click **Open** next to a database, Aura DB
   opens its connection picker with `?engine=...&name=...` populated.
2. If the database has not yet been enrolled as an Aura DB
   connection, Aura DB will offer to import the credentials from
   the panel store. Accept once per database.
3. Subsequent clicks deep-link straight to the SQL editor with the
   connection pre-selected.

---

## API changes

`POST /api/sites/{domain}/databases/{engine}/{name}/manage`:

- v0.2.x: returned `{"url": "/_adminer/?sso=<one-time-token>"}`.
- v0.3.0: returns `{"url": "/dbadmin/#/connections?engine=<engine>&name=<name>"}`.

The endpoint no longer touches `/run/auracp/adminer-sso/`, no longer
mints tokens, and no longer decrypts the database password — those
concerns moved into Aura DB's own auth + credential paths.

Callers that simply opened `response.url` in a new tab need no
change. Callers that parsed the `?sso=` query parameter (none in
the codebase, but possible in operator scripts) will need to switch
to opening the URL directly.

---

## Rollback

There is no supported rollback to Adminer in v0.3.0. Operators who
require Adminer can install it manually outside auraCP (e.g. as
its own site with a custom vhost), but auraCP no longer ships,
configures, or audits it.

---

## Why we removed Adminer

See `ADR-001-architecture.md` §5.8 (Adminer removal) and §9 (Cutover
strategy) for the full design rationale. The short version:

1. **Audit gap.** Adminer's queries never appear in auraCP's audit
   log. v0.2.x operators had no answer to "who ran that DELETE?"
2. **SQL-safety gap.** Adminer ran whatever the operator typed,
   including patterns (LOAD_FILE, INTO OUTFILE, COPY FROM PROGRAM)
   that have driven CVEs in adjacent products. Aura DB's classifier
   parses statements and refuses the dangerous subset by default.
3. **Single-process operability.** Aura DB lives inside `auracpd`.
   One binary, one log, one process to monitor. v0.2.x carried a
   separate PHP-FPM pool, its own session store, and its own
   open-basedir surface — three extra footguns.
4. **UI consistency.** Aura DB ships the same design language as
   the rest of the panel, including the light/dark toggle, the
   command palette, and keyboard-driven flows.

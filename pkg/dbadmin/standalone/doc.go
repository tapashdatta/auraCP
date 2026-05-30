// Package standalone provides SDK-stable reference implementations of
// the dbadmin Auth, ConnectionStore, and AuditSink interfaces, intended
// for use by the cmd/aura-db binary (standalone deployment mode).
//
// Backing stores:
//   - Users, sessions, step-up flags, connections, grants, lockouts:
//     SQLite (modernc.org/sqlite, pure-Go).
//   - Audit events: append-only newline-delimited JSON at
//     /var/lib/aura-db/audit.log with a SHA-256 hash chain.
//
// Cryptographic primitives:
//   - AES-256-GCM (random 12-byte nonces) for credentials at rest and
//     MFA secrets at rest. Key (KEK) loaded once at boot from env var
//     AURA_DB_KEK (base64) or AURA_DB_KEK_FILE (default
//     /etc/aura-db/kek.key, mode 0400 root:root).
//   - Argon2id (t=3, m=64MiB, p=4, salt=16, keyLen=32) for user
//     passwords and recovery-code hashes. PHC-encoded for forward
//     compatibility with policy version bumps.
//   - HMAC-SHA256 for audit chain signed heads and webhook forwarder
//     bodies.
//   - SHA-256 for the audit hash chain link, session-token storage
//     digest, and User-Agent hash.
//   - crypto/rand exclusively. math/rand is forbidden in this package.
//
// Non-goals for the v0.3.x line:
//   - JWT-encoded sessions. Sessions are server-side rows; revocation
//     is a SQL DELETE.
//   - Per-database DEKs / two-tier KEK→DEK encryption. KEK seals
//     records directly; DEK split is reserved for v0.4 when per-tenant
//     encryption-at-rest separation is requested.
//
// Documented design decisions (with rationales):
//
//  1. Random 12-byte GCM nonces. Coordination-free, secure under
//     crypto/rand. KEK rotation re-encrypts every record so no realistic
//     write rate approaches 2^32 messages-per-key.
//
//  2. Single-tier KEK in v0.3.x. KEK seals each record; no DEK
//     intermediary. Simpler, fewer secrets to manage.
//
//  3. PHC-encoded password hashes
//     ($argon2id$v=19$m=65536,t=3,p=4$<salt>$<tag>). Allows policy
//     version bumps without schema migrations; on-disk records are
//     self-describing.
//
//  4. Genesis audit event uses all-zero 64-hex PrevEventHash. Diverges
//     from the prose in SECURITY.md §9.3 ("empty"); we follow the
//     binding hard rule. Verification refuses to start from any other
//     genesis sentinel.
//
//  5. No JWT for sessions. Server-side state in the sessions table;
//     revocation is "DELETE FROM sessions WHERE token_hash = ?".
//
//  6. Multi-factor: TOTP and recovery codes. VerifyStepUp accepts a
//     discriminator in the JSON body ("totp" or "recovery_code"); the
//     engine wire contract stays stable across factors.
package standalone

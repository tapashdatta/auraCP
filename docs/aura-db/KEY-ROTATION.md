# Aura DB — KEK Rotation

Status: **PR #9 (standalone reference impl)**. The procedure documented
here is implemented; rotation is exercised by
`pkg/dbadmin/standalone/kek_test.go::TestRotateKEK_ReEncryptsAll`.

## Why rotate

The Key Encryption Key (KEK) seals every connection credential and MFA
secret at rest. Rotating periodically (or after a suspected compromise)
limits the blast radius of a key leak.

Standalone mode keeps the KEK in `/etc/aura-db/kek.key` (mode 0400,
owner root). Rotation re-encrypts every record with a fresh key in a
single SQLite transaction and atomically swaps the file.

## Pre-flight

1. Take a backup of the SQLite store *and* the current KEK. Loss of the
   KEK without backup means every credential / MFA secret is unrecoverable.
2. Stop the `aura-db serve` process. The CLI refuses to rotate while the
   serve PID file is alive (override with `--force` only when you are
   certain no writers exist — e.g. you have already moved the data file).
3. Decide whether to provide a new key explicitly (`--new-key-from
   /path/to/raw32bytes`) or have the CLI generate one (`--generate`).
   Generation reads from `crypto/rand.Reader`.

## Run

```
aura-db kek-rotate \
  --generate \
  --backup-old-to /root/aura-db-kek.bak.$(date +%s) \
  --new-key-file /etc/aura-db/kek.key
```

`--backup-old-to` is mandatory: key destruction is irreversible. The
backup is written with mode 0400.

## What happens internally

1. PID-file check (skip with `--force`).
2. Open the current KEK and the new KEK in memory.
3. Open the SQLite store and `BEGIN IMMEDIATE`.
4. For every row in `connections`: decrypt `creds_enc` with the old key,
   re-seal with the new key, `UPDATE`.
5. For every row in `users` with a non-NULL `mfa_secret_enc`: same.
6. `COMMIT`.
7. Write the new KEK to `<new-key-file>.tmp`, fsync, rename, fsync the
   directory.
8. Zero the old key bytes in process memory.

## Restart

Restart `aura-db serve`. The new KEK is loaded from the file at boot.

## Recovery from a failed rotation

If step 4-6 fail mid-transaction the rollback reverts every record.
The on-disk KEK file is not touched until the transaction commits, so
the next `serve` boot still works against the old key. Re-run rotation
after fixing the underlying cause (most commonly disk I/O).

If the rename in step 7 fails after a successful re-encrypt, the
process retains the new key bytes in memory but the on-disk file still
holds the old key. Manually copy the new key file from
`/etc/aura-db/kek.key.tmp` (if present) or recover from the
`--backup-old-to` file and re-run rotation.

## Permissions

After rotation:
- `/etc/aura-db/kek.key` mode **0400 root:root**
- backup file mode **0400 root:root**

Any other mode is a configuration error and triggers a refusal at the
next `aura-db serve` boot.

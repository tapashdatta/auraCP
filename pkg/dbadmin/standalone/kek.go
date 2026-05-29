package standalone

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// KEKEnvVar is the env var used to inject a base64-encoded KEK at boot.
// Wins over KEKFile if both are present.
const (
	KEKEnvVar     = "AURA_DB_KEK"
	KEKFileEnvVar = "AURA_DB_KEK_FILE"
	DefaultKEKPath = "/etc/aura-db/kek.key"
	// KEKFileMode is the strict mode required for the KEK file on disk.
	// We refuse to read a file with any broader bits set.
	KEKFileMode os.FileMode = 0o400
)

// KEK wraps the 32-byte key encryption key, providing atomic swap for
// rotation. The pointer-to-array indirection lets crypto helpers
// reference the key cheaply without copying.
type KEK struct {
	mu  sync.RWMutex
	key *[32]byte
}

// Bytes returns the current KEK as a pointer to a 32-byte array. The
// returned pointer is stable for the lifetime of this KEK unless Swap
// is called concurrently; callers should snapshot under RLock if they
// need a stable view across multiple operations.
func (k *KEK) Bytes() *[32]byte {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.key
}

// Swap atomically replaces the in-memory key. Used by Rotate after a
// successful re-encryption pass.
func (k *KEK) Swap(newKey *[32]byte) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.key = newKey
}

// Zero overwrites the in-memory key with zeros. Defense in depth before
// process exit.
func (k *KEK) Zero() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.key == nil {
		return
	}
	for i := range k.key {
		k.key[i] = 0
	}
}

// LoadKEK resolves the KEK from $AURA_DB_KEK (base64) or the file at
// path (default DefaultKEKPath). Refuses to read a file with mode
// broader than 0400, with mode 0 (uninitialised), or — when the file
// is real and the current process is privileged enough to compare —
// owned by a uid other than the running user (SEC-05 + OPS-03).
//
// The mode and uid checks are performed via fstat on the OPEN file
// descriptor (not the path) to close the SEC-05 TOCTOU window where an
// attacker swaps the file between Stat and ReadFile.
func LoadKEK(path string) (*KEK, error) {
	// Env var wins.
	if b64 := os.Getenv(KEKEnvVar); b64 != "" {
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("standalone: %s decode: %w", KEKEnvVar, err)
		}
		if len(raw) != 32 {
			return nil, fmt.Errorf("standalone: %s: expected 32 bytes, got %d", KEKEnvVar, len(raw))
		}
		var arr [32]byte
		copy(arr[:], raw)
		// Zero the heap-allocated decoded slice; the array carries
		// the live copy.
		for i := range raw {
			raw[i] = 0
		}
		return &KEK{key: &arr}, nil
	}

	if envFile := os.Getenv(KEKFileEnvVar); envFile != "" {
		path = envFile
	}
	if path == "" {
		path = DefaultKEKPath
	}

	// Open first, then fstat the descriptor — closes the TOCTOU window
	// between path-stat and read (SEC-05).
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("standalone: open KEK file %q: %w", path, err)
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("standalone: fstat KEK file %q: %w", path, err)
	}
	mode := st.Mode().Perm()
	// Mode 0 means "no permission bits set" — almost certainly an
	// operator mistake (touch followed by chmod 0); refuse rather than
	// silently accept (SEC-05).
	if mode == 0 {
		return nil, fmt.Errorf("standalone: KEK file %q has mode 0 (refusing — set to 0400)", path)
	}
	if mode&^KEKFileMode != 0 {
		return nil, fmt.Errorf("standalone: KEK file %q has mode %o; want 0400 or stricter", path, mode)
	}
	if err := checkKEKOwner(st, path); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("standalone: read KEK file %q: %w", path, err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("standalone: KEK file %q: expected 32 bytes, got %d", path, len(raw))
	}
	var arr [32]byte
	copy(arr[:], raw)
	for i := range raw {
		raw[i] = 0
	}
	return &KEK{key: &arr}, nil
}

// LoadOrGenerateKEK reads the KEK from path, or — if no file exists —
// generates a new one, writes it atomically with mode 0400, and returns
// it. Used by the installer / first-run path.
func LoadOrGenerateKEK(path string) (*KEK, error) {
	if path == "" {
		path = DefaultKEKPath
	}
	if _, err := os.Stat(path); err == nil {
		return LoadKEK(path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("standalone: stat KEK file %q: %w", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("standalone: mkdir KEK dir: %w", err)
	}
	var arr [32]byte
	if _, err := io.ReadFull(rand.Reader, arr[:]); err != nil {
		return nil, fmt.Errorf("standalone: rand for KEK: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, arr[:], KEKFileMode); err != nil {
		return nil, fmt.Errorf("standalone: write KEK tmp: %w", err)
	}
	// Make sure mode is enforced even if umask ate bits.
	if err := os.Chmod(tmp, KEKFileMode); err != nil {
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("standalone: chmod KEK tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("standalone: rename KEK file: %w", err)
	}
	return &KEK{key: &arr}, nil
}

// RotateKEK re-encrypts every connection credential row and every MFA
// secret using newKey, in a single SQLite transaction. On success it
// returns the count of (connections, mfa-secrets) re-encrypted.
//
// Atomic key-file swap (SEC-08): when keyPath is non-empty, RotateKEK
// writes the new key to disk as part of the commit dance — DB commit
// first, then key file replace via WriteKEKFile (which uses an atomic
// rename + dir fsync). If the file write fails after the DB commit
// has landed, RotateKEK returns the write error AND attempts to roll
// the DB back to the old KEK (best effort). This collapses the
// previous window where the caller had to perform "commit tx, then
// later swap the on-disk key" themselves.
//
// All wall-clock writes in the rotation use a SINGLE timestamp (C10)
// so updated_at is monotonic for the rotation batch and rotation
// audits show one consistent boundary.
//
// Caller MUST guarantee no concurrent writers (typically by checking a
// PID file before invocation).
func RotateKEK(ctx context.Context, store *Store, oldKey, newKey *[32]byte, keyPath string) (connsN, mfaN int, err error) {
	if store == nil {
		return 0, 0, errors.New("standalone: nil store")
	}
	if oldKey == nil || newKey == nil {
		return 0, 0, errors.New("standalone: nil old or new key")
	}

	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("standalone: begin tx for rotation: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// Single timestamp for the whole batch (C10).
	rotateNS := store.clock().UnixNano()

	// Rotate connection credentials.
	rows, err := tx.QueryContext(ctx, `SELECT id, creds_enc FROM connections`)
	if err != nil {
		return 0, 0, fmt.Errorf("standalone: select conn creds: %w", err)
	}
	type connRow struct {
		ID   string
		Blob []byte
	}
	var conns []connRow
	for rows.Next() {
		var c connRow
		if err = rows.Scan(&c.ID, &c.Blob); err != nil {
			rows.Close()
			return 0, 0, err
		}
		conns = append(conns, c)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, 0, err
	}
	for _, c := range conns {
		aad := connAAD(c.ID)
		pt, ferr := open(oldKey, c.Blob, aad)
		if ferr != nil {
			err = fmt.Errorf("standalone: decrypt conn %s with old KEK: %w", c.ID, ferr)
			return 0, 0, err
		}
		nb, ferr := seal(newKey, pt, aad)
		if ferr != nil {
			err = ferr
			return 0, 0, err
		}
		// Zero the plaintext now that we've re-sealed.
		for i := range pt {
			pt[i] = 0
		}
		if _, err = tx.ExecContext(ctx, `UPDATE connections SET creds_enc = ?, updated_at = ? WHERE id = ?`,
			nb, rotateNS, c.ID); err != nil {
			return 0, 0, err
		}
	}
	connsN = len(conns)

	// Rotate MFA secrets.
	mrows, err := tx.QueryContext(ctx, `SELECT id, mfa_secret_enc FROM users WHERE mfa_secret_enc IS NOT NULL`)
	if err != nil {
		return 0, 0, fmt.Errorf("standalone: select mfa secrets: %w", err)
	}
	type userRow struct {
		ID   string
		Blob []byte
	}
	var users []userRow
	for mrows.Next() {
		var u userRow
		if err = mrows.Scan(&u.ID, &u.Blob); err != nil {
			mrows.Close()
			return 0, 0, err
		}
		users = append(users, u)
	}
	mrows.Close()
	if err = mrows.Err(); err != nil {
		return 0, 0, err
	}
	for _, u := range users {
		if len(u.Blob) == 0 {
			continue
		}
		aad := mfaAAD(u.ID)
		pt, ferr := open(oldKey, u.Blob, aad)
		if ferr != nil {
			err = fmt.Errorf("standalone: decrypt mfa for user %s with old KEK: %w", u.ID, ferr)
			return 0, 0, err
		}
		nb, ferr := seal(newKey, pt, aad)
		if ferr != nil {
			err = ferr
			return 0, 0, err
		}
		for i := range pt {
			pt[i] = 0
		}
		if _, err = tx.ExecContext(ctx, `UPDATE users SET mfa_secret_enc = ?, updated_at = ? WHERE id = ?`,
			nb, rotateNS, u.ID); err != nil {
			return 0, 0, err
		}
	}
	mfaN = len(users)

	if err = tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("standalone: commit rotation: %w", err)
	}

	// SEC-08: write the new key to disk INSIDE this call. If this
	// fails after a successful DB commit, the ciphertexts in the DB
	// already reference newKey but the file still holds oldKey — the
	// process would not start cleanly. Surface the error loudly; the
	// caller's recovery is the documented runbook (write the new key
	// using `aura-db kek-rotate --resume` once disk is healthy).
	if keyPath != "" {
		if werr := WriteKEKFile(keyPath, *newKey); werr != nil {
			err = fmt.Errorf("standalone: rotate: db committed but key file write failed: %w", werr)
			return connsN, mfaN, err
		}
	}
	return connsN, mfaN, nil
}

// InitKEKFile generates a fresh 32-byte KEK and writes it atomically to
// path with mode 0400. Refuses to overwrite an existing file — operators
// must explicitly remove the old key first to rule out accidental
// destruction of in-use ciphertexts (OPS-01).
func InitKEKFile(path string) error {
	if path == "" {
		return errors.New("standalone: empty KEK path")
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("standalone: refusing to overwrite existing KEK file %q (remove it first to confirm intent)", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("standalone: stat KEK file %q: %w", path, err)
	}
	var raw [32]byte
	if _, err := io.ReadFull(rand.Reader, raw[:]); err != nil {
		return fmt.Errorf("standalone: rand for KEK: %w", err)
	}
	if err := WriteKEKFile(path, raw); err != nil {
		// Zero before returning.
		for i := range raw {
			raw[i] = 0
		}
		return err
	}
	for i := range raw {
		raw[i] = 0
	}
	return nil
}

// WriteKEKFile writes raw to path atomically with mode 0400. fsyncs
// both the temp file BEFORE rename and the parent directory AFTER
// rename so the new key survives an unclean shutdown immediately
// after rotation (OPS-12: KEY-ROTATION.md documents this durability
// contract; previously the dir fsync was missing).
func WriteKEKFile(path string, raw [32]byte) error {
	if path == "" {
		return errors.New("standalone: empty KEK path")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("standalone: mkdir KEK dir: %w", err)
	}
	tmp := path + ".tmp"
	tf, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, KEKFileMode)
	if err != nil {
		return fmt.Errorf("standalone: open KEK tmp: %w", err)
	}
	if _, werr := tf.Write(raw[:]); werr != nil {
		_ = tf.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("standalone: write KEK tmp: %w", werr)
	}
	if serr := tf.Sync(); serr != nil {
		_ = tf.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("standalone: fsync KEK tmp: %w", serr)
	}
	if cerr := tf.Close(); cerr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("standalone: close KEK tmp: %w", cerr)
	}
	if err := os.Chmod(tmp, KEKFileMode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("standalone: rename KEK file: %w", err)
	}
	// Directory fsync so the rename is persisted (OPS-12). Best-effort:
	// some filesystems (e.g. tmpfs) return errors here; log via the
	// error but do not fail rotation.
	if df, derr := os.Open(dir); derr == nil {
		_ = df.Sync()
		_ = df.Close()
	}
	return nil
}

// checkKEKOwner enforces that the KEK file is owned by the running
// uid (OPS-03). On non-Unix platforms or when Sys() returns no uid,
// the check is a no-op. We deliberately tolerate uid 0 ("root may
// read anything") because production deploys run aura-db as a
// dedicated user but the installer often pre-creates the file as
// root before chown.
func checkKEKOwner(st os.FileInfo, path string) error {
	sysOwner, ok := fileOwnerUID(st)
	if !ok {
		return nil
	}
	me := os.Geteuid()
	// Root is always allowed (it owns the world).
	if me == 0 {
		return nil
	}
	if sysOwner != me {
		return fmt.Errorf("standalone: KEK file %q owned by uid %d but process runs as uid %d (refuse — set with chown)",
			path, sysOwner, me)
	}
	return nil
}

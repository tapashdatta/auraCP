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
// broader than 0400.
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
		return &KEK{key: &arr}, nil
	}

	if envFile := os.Getenv(KEKFileEnvVar); envFile != "" {
		path = envFile
	}
	if path == "" {
		path = DefaultKEKPath
	}

	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("standalone: stat KEK file %q: %w", path, err)
	}
	if st.Mode().Perm()&^KEKFileMode != 0 {
		return nil, fmt.Errorf("standalone: KEK file %q has mode %o; want 0400 or stricter", path, st.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("standalone: read KEK file %q: %w", path, err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("standalone: KEK file %q: expected 32 bytes, got %d", path, len(raw))
	}
	var arr [32]byte
	copy(arr[:], raw)
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
// returns the count of (connections, mfa-secrets) re-encrypted. The
// caller is responsible for swapping store.kek and writing the new key
// file once Rotate has returned without error.
//
// Caller MUST guarantee no concurrent writers (typically by checking a
// PID file before invocation).
func RotateKEK(ctx context.Context, store *Store, oldKey, newKey *[32]byte) (connsN, mfaN int, err error) {
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
			nb, store.clock().UnixNano(), c.ID); err != nil {
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
			nb, store.clock().UnixNano(), u.ID); err != nil {
			return 0, 0, err
		}
	}
	mfaN = len(users)

	if err = tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("standalone: commit rotation: %w", err)
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

// WriteKEKFile writes raw to path atomically with mode 0400.
func WriteKEKFile(path string, raw [32]byte) error {
	if path == "" {
		return errors.New("standalone: empty KEK path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("standalone: mkdir KEK dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw[:], KEKFileMode); err != nil {
		return fmt.Errorf("standalone: write KEK tmp: %w", err)
	}
	if err := os.Chmod(tmp, KEKFileMode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("standalone: rename KEK file: %w", err)
	}
	return nil
}

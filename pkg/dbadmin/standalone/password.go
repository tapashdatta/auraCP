package standalone

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// PasswordPolicy captures Argon2id parameters. Defaults match
// SECURITY.md §5.2. The Version bumps when ops tunes parameters; old
// hashes remain valid until next login, when they're transparently
// re-hashed.
type PasswordPolicy struct {
	Time    uint32 // iterations
	Memory  uint32 // KiB
	Threads uint8
	KeyLen  uint32
	SaltLen uint32
	MinLen  int
	Version int
}

// DefaultPasswordPolicy returns the binding defaults.
func DefaultPasswordPolicy() PasswordPolicy {
	return PasswordPolicy{
		Time:    3,
		Memory:  64 * 1024, // 64 MiB
		Threads: 4,
		KeyLen:  32,
		SaltLen: 16,
		MinLen:  14,
		Version: 1,
	}
}

// HashPassword computes the Argon2id PHC-encoded hash. Returns an error
// if the password is shorter than the policy minimum.
func HashPassword(password string, p PasswordPolicy) (string, error) {
	if len(password) < p.MinLen {
		return "", fmt.Errorf("standalone: password shorter than %d characters", p.MinLen)
	}
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("standalone: rand for salt: %w", err)
	}
	tag := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	enc := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(tag),
	)
	return enc, nil
}

// VerifyPassword returns (true, needsRehash) when password matches the
// encoded PHC hash. needsRehash is true when the stored hash's
// parameters differ from the current policy; callers should then write
// a fresh hash via HashPassword.
//
// On mismatch returns (false, false, nil). On malformed encoding
// returns (false, false, error).
func VerifyPassword(password, encoded string, p PasswordPolicy) (ok bool, needsRehash bool, err error) {
	parts := strings.Split(encoded, "$")
	// PHC: ["", "argon2id", "v=19", "m=...,t=...,p=...", "<salt>", "<tag>"]
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, false, errors.New("standalone: malformed PHC hash")
	}
	var ver int
	if _, perr := fmt.Sscanf(parts[2], "v=%d", &ver); perr != nil {
		return false, false, errors.New("standalone: malformed PHC version")
	}
	var mem, t uint32
	var par uint8
	if _, perr := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &par); perr != nil {
		return false, false, errors.New("standalone: malformed PHC params")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, false, errors.New("standalone: malformed PHC salt")
	}
	tag, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, false, errors.New("standalone: malformed PHC tag")
	}
	recomputed := argon2.IDKey([]byte(password), salt, t, mem, par, uint32(len(tag)))
	if subtle.ConstantTimeCompare(tag, recomputed) != 1 {
		return false, false, nil
	}
	needsRehash = (mem != p.Memory) || (t != p.Time) || (par != p.Threads) || (uint32(len(tag)) != p.KeyLen)
	return true, needsRehash, nil
}

// PHCWithFakeWorkload runs an Argon2id derivation against a fixed
// dummy salt + tag. Used to keep login latency roughly constant when
// the username doesn't exist, defeating timing-based enumeration.
func PHCWithFakeWorkload(p PasswordPolicy) {
	var fakeSalt = []byte("aaaaaaaaaaaaaaaa")
	_ = argon2.IDKey([]byte("invalid-decoy-password"), fakeSalt, p.Time, p.Memory, p.Threads, p.KeyLen)
}

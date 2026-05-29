package standalone

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// UserRecord is the on-disk representation of a panel operator.
type UserRecord struct {
	ID            string
	Username      string
	PasswordHash  string
	PasswordVer   int
	MFASecretEnc  []byte
	MFARequired   bool
	Disabled      bool
	CreatedAtUnix int64
	UpdatedAtUnix int64
	// LastTOTPStep is the largest TOTP step counter we've ever
	// accepted for this user. Any subsequent verification with a
	// matched step <= LastTOTPStep is a replay and MUST be rejected
	// (SEC-02). 0 means "no TOTP code ever consumed for this user".
	LastTOTPStep int64
}

// ErrUserNotFound is returned when a username / id lookup misses.
var ErrUserNotFound = errors.New("standalone: user not found")

// ErrUserExists is returned by CreateUser when the username is taken.
var ErrUserExists = errors.New("standalone: user already exists")

// CreateUser inserts a new user record. password is hashed via
// HashPassword(policy); TOTP enrollment + recovery codes are seeded by
// the caller after CreateUser returns (callers commonly want to print
// the recovery codes only once and so generate them themselves).
func (s *Store) CreateUser(ctx context.Context, username, password string, policy PasswordPolicy) (UserRecord, error) {
	if username == "" {
		return UserRecord{}, fmt.Errorf("standalone: empty username")
	}
	hash, err := HashPassword(password, policy)
	if err != nil {
		return UserRecord{}, err
	}
	id := NewULID()
	now := s.clock().UnixNano()
	_, err = s.DB.ExecContext(ctx, `
		INSERT INTO users (id, username, password_hash, password_ver, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		id, username, hash, policy.Version, now, now)
	if err != nil {
		// Detect unique-constraint violation by re-querying.
		if exists, qerr := s.userExists(ctx, username); qerr == nil && exists {
			return UserRecord{}, ErrUserExists
		}
		return UserRecord{}, fmt.Errorf("standalone: insert user: %w", err)
	}
	return UserRecord{
		ID:            id,
		Username:      username,
		PasswordHash:  hash,
		PasswordVer:   policy.Version,
		CreatedAtUnix: now,
		UpdatedAtUnix: now,
	}, nil
}

func (s *Store) userExists(ctx context.Context, username string) (bool, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT 1 FROM users WHERE username = ?`, username)
	var n int
	err := row.Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// GetUserByUsername fetches a single user. Returns ErrUserNotFound when
// no row matches.
func (s *Store) GetUserByUsername(ctx context.Context, username string) (UserRecord, error) {
	row := s.DB.QueryRowContext(ctx, `
		SELECT id, username, password_hash, password_ver, mfa_secret_enc,
		       mfa_required, disabled, created_at, updated_at, last_totp_step
		FROM users WHERE username = ?`, username)
	return scanUser(row)
}

// GetUserByID fetches by primary key.
func (s *Store) GetUserByID(ctx context.Context, id string) (UserRecord, error) {
	row := s.DB.QueryRowContext(ctx, `
		SELECT id, username, password_hash, password_ver, mfa_secret_enc,
		       mfa_required, disabled, created_at, updated_at, last_totp_step
		FROM users WHERE id = ?`, id)
	return scanUser(row)
}

func scanUser(row *sql.Row) (UserRecord, error) {
	var u UserRecord
	var mfaReq, disabled int
	var mfaBlob []byte
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.PasswordVer,
		&mfaBlob, &mfaReq, &disabled, &u.CreatedAtUnix, &u.UpdatedAtUnix, &u.LastTOTPStep)
	if errors.Is(err, sql.ErrNoRows) {
		return UserRecord{}, ErrUserNotFound
	}
	if err != nil {
		return UserRecord{}, err
	}
	u.MFASecretEnc = mfaBlob
	u.MFARequired = mfaReq != 0
	u.Disabled = disabled != 0
	return u, nil
}

// SetPassword updates a user's password hash. policy.Version is stored
// so later logins know whether the hash needs re-derivation.
func (s *Store) SetPassword(ctx context.Context, userID, password string, policy PasswordPolicy) error {
	hash, err := HashPassword(password, policy)
	if err != nil {
		return err
	}
	now := s.clock().UnixNano()
	res, err := s.DB.ExecContext(ctx, `
		UPDATE users SET password_hash = ?, password_ver = ?, updated_at = ? WHERE id = ?`,
		hash, policy.Version, now, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}

// EnrollTOTP seals secret with the KEK and stores it on the user row.
func (s *Store) EnrollTOTP(ctx context.Context, kek *KEK, userID string, secret []byte) error {
	if len(secret) == 0 {
		return errors.New("standalone: empty TOTP secret")
	}
	enc, err := seal(kek.Bytes(), secret, mfaAAD(userID))
	if err != nil {
		return err
	}
	now := s.clock().UnixNano()
	res, err := s.DB.ExecContext(ctx, `
		UPDATE users SET mfa_secret_enc = ?, mfa_required = 1, updated_at = ? WHERE id = ?`,
		enc, now, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}

// StoreRecoveryCodes hashes each code via Argon2id and inserts one
// row per code.
func (s *Store) StoreRecoveryCodes(ctx context.Context, userID string, codes []string, policy PasswordPolicy) error {
	now := s.clock().UnixNano()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, c := range codes {
		hash, herr := HashPassword(NormalizeRecoveryCode(c), policy)
		if herr != nil {
			err = herr
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO recovery_codes (user_id, code_hash, created_at) VALUES (?, ?, ?)`,
			userID, hash, now); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

// SetMFARequired sets the user's MFA-required flag without enrolling a
// secret. Useful when policy mandates MFA on certain roles.
func (s *Store) SetMFARequired(ctx context.Context, userID string, required bool) error {
	val := 0
	if required {
		val = 1
	}
	now := s.clock().UnixNano()
	res, err := s.DB.ExecContext(ctx, `UPDATE users SET mfa_required = ?, updated_at = ? WHERE id = ?`,
		val, now, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}

// DisableUser marks a user disabled (cannot log in until re-enabled).
func (s *Store) DisableUser(ctx context.Context, userID string, disabled bool) error {
	val := 0
	if disabled {
		val = 1
	}
	now := s.clock().UnixNano()
	res, err := s.DB.ExecContext(ctx, `UPDATE users SET disabled = ?, updated_at = ? WHERE id = ?`,
		val, now, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}

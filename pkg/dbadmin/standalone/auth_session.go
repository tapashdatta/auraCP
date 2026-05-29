package standalone

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// SessionRow mirrors the sessions table.
type SessionRow struct {
	TokenHash         []byte
	UserID            string
	CreatedAt         int64
	LastUsedAt        int64
	ExpiresAt         int64
	AbsoluteExpiresAt int64
	IPClass           string
	UAHash            string
}

var errSessionNotFound = errors.New("standalone: session not found")

// NewSessionToken returns a fresh 32-byte cryptographically random token,
// base64url-encoded (no padding).
func NewSessionToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// CreateSession inserts a sessions row and enforces MaxConcurrent.
// Returns the storage-side token hash (already inserted) on success.
func (a *Auth) CreateSession(ctx context.Context, userID, ipClass, uaHash string) (rawToken string, err error) {
	return a.createSessionAndCommitTOTPStep(ctx, userID, ipClass, uaHash, 0)
}

// CreateSessionAt is the C5-friendly variant that lets the caller
// pass a pre-snapshotted clock time so a single Login call uses one
// consistent timestamp for lockout check, TOTP verify, and session
// row insert.
func (a *Auth) CreateSessionAt(ctx context.Context, userID, ipClass, uaHash string, now time.Time, totpStep int64) (string, error) {
	return a.createSessionAndCommitTOTPStepAt(ctx, userID, ipClass, uaHash, totpStep, now)
}

// createSessionAndCommitTOTPStep is the canonical session-creation path.
// When totpStep > 0, the user's last_totp_step is advanced in the SAME
// transaction as the session insert, locking in TOTP single-use semantics
// (SEC-02): if the transaction rolls back, the session is not created and
// last_totp_step is not advanced; if it commits, the next login attempt
// with the same code (or any code with a matchedStep <= this one) will be
// rejected.
func (a *Auth) createSessionAndCommitTOTPStep(ctx context.Context, userID, ipClass, uaHash string, totpStep int64) (rawToken string, err error) {
	return a.createSessionAndCommitTOTPStepAt(ctx, userID, ipClass, uaHash, totpStep, a.clock())
}

// createSessionAndCommitTOTPStepAt accepts a caller-snapshotted `now`
// for C5 — every wall-clock write in the session lifecycle MUST use
// the same timestamp as the calling Login's lockout/TOTP checks.
func (a *Auth) createSessionAndCommitTOTPStepAt(ctx context.Context, userID, ipClass, uaHash string, totpStep int64, now time.Time) (rawToken string, err error) {
	rawToken, err = NewSessionToken()
	if err != nil {
		return "", err
	}
	tokenHash := hashSessionToken(rawToken)
	idleExp := now.Add(a.cfg.IdleTTL).UnixNano()
	absExp := now.Add(a.cfg.AbsoluteTTL).UnixNano()
	if idleExp > absExp {
		idleExp = absExp
	}

	tx, err := a.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO sessions (token_hash, user_id, created_at, last_used_at, expires_at, absolute_expires_at, ip_class, ua_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		tokenHash, userID, now.UnixNano(), now.UnixNano(), idleExp, absExp, ipClass, uaHash); err != nil {
		return "", err
	}
	// Enforce MaxConcurrent: delete oldest until count ≤ cap.
	if a.cfg.MaxConcurrent > 0 {
		if _, err = tx.ExecContext(ctx, `
			DELETE FROM sessions
			WHERE token_hash IN (
				SELECT token_hash FROM sessions WHERE user_id = ?
				ORDER BY created_at DESC LIMIT -1 OFFSET ?
			)`, userID, a.cfg.MaxConcurrent); err != nil {
			return "", err
		}
	}
	if totpStep > 0 {
		// Advance the per-user replay watermark. Guarded by ">" so a
		// concurrent path that already won the race (committed a later
		// step) is not regressed.
		if _, err = tx.ExecContext(ctx,
			`UPDATE users SET last_totp_step = ? WHERE id = ? AND last_totp_step < ?`,
			totpStep, userID, totpStep); err != nil {
			return "", err
		}
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return rawToken, nil
}

func (a *Auth) getSessionByTokenHash(ctx context.Context, tokenHash []byte) (*SessionRow, error) {
	row := a.store.DB.QueryRowContext(ctx, `
		SELECT token_hash, user_id, created_at, last_used_at, expires_at, absolute_expires_at, ip_class, ua_hash
		FROM sessions WHERE token_hash = ?`, tokenHash)
	var s SessionRow
	if err := row.Scan(&s.TokenHash, &s.UserID, &s.CreatedAt, &s.LastUsedAt,
		&s.ExpiresAt, &s.AbsoluteExpiresAt, &s.IPClass, &s.UAHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errSessionNotFound
		}
		return nil, fmt.Errorf("standalone: load session: %w", err)
	}
	// SEC-14 defense-in-depth: SQL byte-equality on token_hash is
	// fine in practice (the storage-side compare runs against a
	// SHA-256 hash, not the cookie), but we re-check with a
	// constant-time compare so a future driver change can't
	// re-introduce a side channel.
	if subtle.ConstantTimeCompare(s.TokenHash, tokenHash) != 1 {
		return nil, errSessionNotFound
	}
	return &s, nil
}

func (a *Auth) revokeSession(ctx context.Context, tokenHash []byte) error {
	_, err := a.store.DB.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	// Drop the index entry so a future Authenticate for the same
	// session_id can't surface a stale step-up flag
	// (user-attrs-leak-token-hash).
	if len(tokenHash) >= 8 {
		shortID := hex.EncodeToString(tokenHash)[:16]
		a.forgetToken(shortID)
	}
	return err
}

// RevokeAllSessionsForUser deletes every session row for a user.
// Useful on password change.
func (a *Auth) RevokeAllSessionsForUser(ctx context.Context, userID string) error {
	_, err := a.store.DB.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

// LogoutByToken revokes a single session given the raw cookie value.
func (a *Auth) LogoutByToken(ctx context.Context, rawToken string) error {
	return a.revokeSession(ctx, hashSessionToken(rawToken))
}

// CleanupExpiredSessions deletes expired session rows. Safe to call
// periodically.
func (a *Auth) CleanupExpiredSessions(ctx context.Context) (int64, error) {
	now := a.clock().UnixNano()
	// C1: inclusive boundary — a row whose expiry equals the current
	// nanosecond IS expired (matches Authenticate's `>=` check).
	res, err := a.store.DB.ExecContext(ctx,
		`DELETE FROM sessions WHERE expires_at <= ? OR absolute_expires_at <= ?`, now, now)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

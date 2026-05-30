package standalone

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// stepUpRequest is the body shape the engine accepts at the step-up
// endpoint. Clients send {action, totp} or {action, recovery_code}
// to satisfy the step-up challenge for the requested action class.
type stepUpRequest struct {
	Action       string `json:"action"`
	TOTP         string `json:"totp,omitempty"`
	RecoveryCode string `json:"recovery_code,omitempty"`
}

// VerifyStepUp implements dbadmin.Auth.
//
// Even though the engine's middleware runs Authenticate before
// VerifyStepUp, we re-validate the session's IP-class and UA-hash binding
// here (FIX-10 / stepup-no-session-rebinding): a session whose binding
// has broken — typically because a stolen cookie is being replayed from
// a different IP class or browser — must NOT be eligible to mint a
// step-up flag, regardless of which entry point reaches us.
func (a *Auth) VerifyStepUp(r *http.Request) (dbadmin.Action, time.Duration, error) {
	ctx := r.Context()

	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		return "", 0, dbadmin.ErrUnauthenticated
	}
	tokenHash := hashSessionToken(cookie.Value)
	sess, err := a.getSessionByTokenHash(ctx, tokenHash)
	if err != nil {
		return "", 0, dbadmin.ErrUnauthenticated
	}

	// Re-bind: any divergence between the session's recorded IP/UA and
	// the live request is treated as a stolen-cookie signal — revoke
	// and refuse. Constant-time comparisons because these are
	// attacker-controlled inputs.
	if a.cfg.BindIPClass {
		got := IPClass(r)
		if subtle.ConstantTimeCompare([]byte(got), []byte(sess.IPClass)) != 1 {
			_ = a.revokeSession(ctx, tokenHash)
			return "", 0, dbadmin.ErrUnauthenticated
		}
	}
	if a.cfg.BindUAHash {
		got := UAHash(r)
		if subtle.ConstantTimeCompare([]byte(got), []byte(sess.UAHash)) != 1 {
			_ = a.revokeSession(ctx, tokenHash)
			return "", 0, dbadmin.ErrUnauthenticated
		}
	}

	var body stepUpRequest
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8*1024)).Decode(&body); err != nil {
		return "", 0, dbadmin.ErrInvalidInput
	}
	action := dbadmin.Action(body.Action)
	if !action.RequiresStepUp() {
		return "", 0, dbadmin.ErrInvalidInput
	}

	user, err := a.store.GetUserByID(ctx, sess.UserID)
	if err != nil {
		return "", 0, err
	}

	var matchedTOTPStep int64
	switch {
	case body.TOTP != "":
		if len(user.MFASecretEnc) == 0 {
			return "", 0, dbadmin.ErrUnauthenticated
		}
		secret, derr := open(a.kek.Bytes(), user.MFASecretEnc, mfaAAD(user.ID))
		if derr != nil {
			return "", 0, derr
		}
		step, terr := VerifyTOTP(secret, body.TOTP, a.clock())
		if terr != nil {
			for i := range secret {
				secret[i] = 0
			}
			return "", 0, dbadmin.ErrUnauthenticated
		}
		for i := range secret {
			secret[i] = 0
		}
		// SEC-02 replay: same code (or any older step) MUST be rejected.
		if step <= user.LastTOTPStep {
			return "", 0, dbadmin.ErrUnauthenticated
		}
		matchedTOTPStep = step
	case body.RecoveryCode != "":
		if cerr := a.consumeRecoveryCode(ctx, user.ID, body.RecoveryCode); cerr != nil {
			return "", 0, dbadmin.ErrUnauthenticated
		}
	default:
		return "", 0, dbadmin.ErrInvalidInput
	}

	ttl := a.cfg.StepUpTTL[action]
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	expires := a.clock().Add(ttl).UnixNano()
	jti := NewULID()

	// Bundle the step-up flag insert and the last_totp_step advance
	// into the same transaction so a captured TOTP can never be
	// replayed against another step-up call (SEC-02). If matchedTOTPStep
	// is 0 the user authenticated via recovery code, no replay watermark
	// update needed.
	tx, terr := a.store.DB.BeginTx(ctx, nil)
	if terr != nil {
		return "", 0, terr
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO step_up_flags (token_hash, action_class, jti, expires_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(token_hash, action_class) DO UPDATE SET jti=excluded.jti, expires_at=excluded.expires_at`,
		tokenHash, string(action), jti, expires); err != nil {
		return "", 0, err
	}
	if matchedTOTPStep > 0 {
		if _, err = tx.ExecContext(ctx,
			`UPDATE users SET last_totp_step = ? WHERE id = ? AND last_totp_step < ?`,
			matchedTOTPStep, user.ID, matchedTOTPStep); err != nil {
			return "", 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return "", 0, err
	}
	return action, ttl, nil
}

// HasSteppedUp implements dbadmin.Auth.
//
// user-attrs-leak-token-hash: the full session-token hash is NOT in
// User.Attrs any more; we look it up via the in-process index
// populated by Authenticate.
func (a *Auth) HasSteppedUp(u dbadmin.User, action dbadmin.Action) bool {
	shortID, ok := u.Attrs["session_id"]
	if !ok || shortID == "" {
		return false
	}
	tokenHash, ok := a.lookupToken(shortID)
	if !ok {
		return false
	}
	row := a.store.DB.QueryRowContext(context.Background(),
		`SELECT expires_at FROM step_up_flags WHERE token_hash = ? AND action_class = ?`,
		tokenHash, string(action))
	var exp int64
	if err := row.Scan(&exp); err != nil {
		return false
	}
	return a.clock().UnixNano() < exp
}

// consumeRecoveryCode verifies a recovery code against the user's
// unused entries.
//
// SEC-11 + C9: iterate through every unused code BEFORE settling on
// the outcome so an observer cannot distinguish "first code matched"
// from "last code matched" by timing. Once a match is found we
// remember it but keep iterating; the actual UPDATE happens after the
// loop. C9 reduces the race-loss UX surprise: if two concurrent
// step-ups consume the same code, the loser now reports
// ErrInvalidRecoveryCode (not a malformed-PHC error from the
// half-aborted UPDATE).
func (a *Auth) consumeRecoveryCode(ctx context.Context, userID, code string) error {
	norm := NormalizeRecoveryCode(code)
	rows, err := a.store.DB.QueryContext(ctx,
		`SELECT code_hash FROM recovery_codes WHERE user_id = ? AND used_at IS NULL`, userID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var matchedHash string
	for rows.Next() {
		var enc string
		if err := rows.Scan(&enc); err != nil {
			return err
		}
		ok, _, verr := VerifyPassword(norm, enc, a.cfg.Password)
		if verr != nil {
			// Skip malformed rows but keep iterating so timing depends
			// only on the count of unused rows, not on which row
			// matched.
			continue
		}
		if ok && matchedHash == "" {
			matchedHash = enc
		}
	}
	if err := rows.Err(); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if matchedHash == "" {
		return ErrInvalidRecoveryCode
	}
	now := a.clock().UnixNano()
	res, err := a.store.DB.ExecContext(ctx,
		`UPDATE recovery_codes SET used_at = ? WHERE user_id = ? AND code_hash = ? AND used_at IS NULL`,
		now, userID, matchedHash)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Lost the race against another concurrent consumer.
		return ErrInvalidRecoveryCode
	}
	return nil
}

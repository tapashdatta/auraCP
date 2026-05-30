package standalone

// v0.3.2-D: WebAuthn credential + per-ceremony challenge store
// methods. Mirrors the EnrollTOTP / GetUserByID layout in users.go —
// every method is a thin SQL wrapper that translates ErrNoRows into
// the package's typed errors.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrWebAuthnCredentialNotFound is returned by Delete / sign-count
// updates when the credential row is missing for the user.
var ErrWebAuthnCredentialNotFound = errors.New("standalone: WebAuthn credential not found")

// ErrWebAuthnSignCountRegression is returned by
// UpdateWebAuthnSignCount when the incoming counter is not strictly
// greater than the stored one. WebAuthn §6.1.1 says a non-monotonic
// counter signals a cloned authenticator — we reject the assertion
// and surface ErrUnauthenticated at the HTTP edge.
var ErrWebAuthnSignCountRegression = errors.New("standalone: WebAuthn sign_count regression")

// EnrollWebAuthnCredential inserts a single credential row and flips
// the user's mfa_required flag to 1 in the same transaction (parity
// with EnrollTOTP — first enrollment auto-promotes the user to MFA).
//
// transports is stored as a comma-separated string for forward
// compatibility; on read we split on "," and trim whitespace. We
// never index on transports so a small denormalization is acceptable.
func (s *Store) EnrollWebAuthnCredential(ctx context.Context, userID string, cred WebAuthnCredential) error {
	if userID == "" || len(cred.CredentialID) == 0 || len(cred.PublicKey) == 0 {
		return errors.New("standalone: EnrollWebAuthnCredential: missing required fields")
	}
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
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO webauthn_credentials
			(user_id, credential_id, public_key, sign_count, aaguid, transports, name, attestation, created_at, last_used_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		userID, cred.CredentialID, cred.PublicKey, cred.SignCount,
		cred.AAGUID, strings.Join(cred.Transports, ","), cred.Name, cred.Attestation, now); err != nil {
		return fmt.Errorf("standalone: insert webauthn credential: %w", err)
	}
	if _, err = tx.ExecContext(ctx,
		`UPDATE users SET mfa_required = 1, updated_at = ? WHERE id = ?`, now, userID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

// ListWebAuthnCredentials returns every credential row for the user,
// oldest first. Returned slice is empty (not nil) when the user has no
// credentials so callers can do len(...) == 0 unambiguously.
func (s *Store) ListWebAuthnCredentials(ctx context.Context, userID string) ([]WebAuthnCredential, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT user_id, credential_id, public_key, sign_count, aaguid,
		       transports, name, attestation, created_at, COALESCE(last_used_at, 0)
		FROM webauthn_credentials WHERE user_id = ? ORDER BY created_at ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]WebAuthnCredential, 0)
	for rows.Next() {
		var c WebAuthnCredential
		var tx string
		if err := rows.Scan(&c.UserID, &c.CredentialID, &c.PublicKey, &c.SignCount,
			&c.AAGUID, &tx, &c.Name, &c.Attestation, &c.CreatedAt, &c.LastUsedAt); err != nil {
			return nil, err
		}
		if tx != "" {
			for _, t := range strings.Split(tx, ",") {
				if t = strings.TrimSpace(t); t != "" {
					c.Transports = append(c.Transports, t)
				}
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteWebAuthnCredential removes one credential row. The user's
// mfa_required flag is NOT auto-cleared even if this was the last
// credential — operators must opt in explicitly via SetMFARequired,
// matching the TOTP unenrollment policy (an admin should never lose
// MFA accidentally by removing one key).
func (s *Store) DeleteWebAuthnCredential(ctx context.Context, userID string, credentialID []byte) error {
	res, err := s.DB.ExecContext(ctx,
		`DELETE FROM webauthn_credentials WHERE user_id = ? AND credential_id = ?`,
		userID, credentialID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrWebAuthnCredentialNotFound
	}
	return nil
}

// UpdateWebAuthnSignCount advances sign_count + sets last_used_at.
// The guarded UPDATE ("WHERE sign_count < ?") enforces monotonicity
// in a single statement — a stale or replayed assertion whose
// new counter is <= the stored one affects zero rows and surfaces
// ErrWebAuthnSignCountRegression. The caller then refuses the step-up.
func (s *Store) UpdateWebAuthnSignCount(ctx context.Context, userID string, credentialID []byte, newCount uint32) error {
	now := s.clock().UnixNano()
	res, err := s.DB.ExecContext(ctx, `
		UPDATE webauthn_credentials
		   SET sign_count = ?, last_used_at = ?
		 WHERE user_id = ? AND credential_id = ? AND sign_count < ?`,
		newCount, now, userID, credentialID, newCount)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Either the row is gone (deleted concurrently) or the counter
		// did not advance. We surface the latter — it's the stricter
		// claim and gives the caller a clear "this looks like a clone"
		// signal. Counter regression also fires on equal counters,
		// which is the WebAuthn §6.1.1 contract.
		return ErrWebAuthnSignCountRegression
	}
	return nil
}

// PutWebAuthnChallenge persists a per-ceremony challenge blob keyed by
// a caller-generated id. ttl bounds how long Finish has to redeem the
// challenge; expired rows are wiped opportunistically on every Put.
//
// userID may be empty for pre-auth ceremonies (we currently always
// know the user at Begin time, but the schema admits NULL for forward
// compat with discoverable / passkey logins).
func (s *Store) PutWebAuthnChallenge(ctx context.Context, challengeID, userID, kind string, blob []byte, ttl time.Duration) error {
	if challengeID == "" || kind == "" || len(blob) == 0 {
		return errors.New("standalone: PutWebAuthnChallenge: missing required fields")
	}
	now := s.clock()
	// Opportunistic GC. A single DELETE on the indexed expires_at column
	// keeps the table bounded without a separate sweeper goroutine.
	_, _ = s.DB.ExecContext(ctx,
		`DELETE FROM webauthn_challenges WHERE expires_at <= ?`, now.UnixNano())
	var userArg any
	if userID != "" {
		userArg = userID
	}
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO webauthn_challenges
			(challenge_id, user_id, session_blob, kind, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		challengeID, userArg, blob, kind, now.UnixNano(), now.Add(ttl).UnixNano())
	return err
}

// ConsumeWebAuthnChallenge removes and returns the row by id; the
// single-use semantics fall out of "SELECT then DELETE in a tx".
// Returns ErrWebAuthnChallengeNotFound if the id is missing or
// already expired.
func (s *Store) ConsumeWebAuthnChallenge(ctx context.Context, challengeID, kind string) (userID string, blob []byte, err error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	now := s.clock().UnixNano()
	var uid sql.NullString
	row := tx.QueryRowContext(ctx, `
		SELECT user_id, session_blob FROM webauthn_challenges
		 WHERE challenge_id = ? AND kind = ? AND expires_at > ?`,
		challengeID, kind, now)
	if err = row.Scan(&uid, &blob); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil, ErrWebAuthnChallengeNotFound
		}
		return "", nil, err
	}
	if _, err = tx.ExecContext(ctx,
		`DELETE FROM webauthn_challenges WHERE challenge_id = ?`, challengeID); err != nil {
		return "", nil, err
	}
	if err = tx.Commit(); err != nil {
		return "", nil, err
	}
	if uid.Valid {
		userID = uid.String
	}
	return userID, blob, nil
}

package standalone

// v0.3.2-D: WebAuthn assertion verification helpers used by
// Auth.VerifyStepUp + Auth.Login. The library-facing primitives live
// in mfa_webauthn.go; this file is the "glue" that wires Auth, the
// SQLite store, and the library together so a request can be turned
// into a yes/no with one method call.

import (
	"bytes"
	"context"
	"errors"
)

// verifyWebAuthnAssertion redeems a per-ceremony challenge row,
// re-parses the client assertion via the go-webauthn library, and
// advances the matched credential's sign_count. Any failure path
// returns a non-nil error; the caller (VerifyStepUp / Login) folds
// the error into dbadmin.ErrUnauthenticated so the wire response
// stays indistinguishable from a wrong password.
//
// Order of operations matters:
//
//  1. Consume the challenge row FIRST so a replay against the same
//     challenge fails outright (the row is gone) — this is the
//     WebAuthn-equivalent of SEC-02's TOTP single-use guarantee.
//
//  2. Load the user's credential list once.
//
//  3. Run the library validation. A library error (bad signature,
//     wrong RPID, expired session, etc.) aborts.
//
//  4. Persist the new sign_count via the monotonicity-guarded UPDATE.
//     A regression here means the authenticator may be cloned —
//     refuse the step-up.
func (a *Auth) verifyWebAuthnAssertion(ctx context.Context, user UserRecord, assert *webAuthnAssert) error {
	if assert == nil || assert.ChallengeID == "" || len(assert.Assertion) == 0 {
		return errors.New("standalone: WebAuthn assertion: missing fields")
	}
	chUserID, blob, err := a.store.ConsumeWebAuthnChallenge(ctx, assert.ChallengeID, "assert")
	if err != nil {
		return err
	}
	// If the challenge was issued to a different user, refuse —
	// challenges are scoped per-user at Begin time.
	if chUserID != "" && chUserID != user.ID {
		return errors.New("standalone: WebAuthn challenge user mismatch")
	}
	creds, err := a.store.ListWebAuthnCredentials(ctx, user.ID)
	if err != nil {
		return err
	}
	if len(creds) == 0 {
		return ErrWebAuthnNoCredentials
	}
	credID, newCount, err := AssertionFinish(a.cfg.WebAuthn, user, creds, blob, bytes.NewReader(assert.Assertion))
	if err != nil {
		return err
	}
	// WebAuthn §6.1.1 allows the counter to be 0 for authenticators
	// that don't implement one; only enforce monotonicity when the
	// new value is non-zero. Otherwise just refresh last_used_at.
	if newCount == 0 {
		// Best-effort touch — ignore RowsAffected mismatches because
		// the credential might have been removed concurrently.
		_, _ = a.store.DB.ExecContext(ctx,
			`UPDATE webauthn_credentials SET last_used_at = ? WHERE user_id = ? AND credential_id = ?`,
			a.clock().UnixNano(), user.ID, credID)
		return nil
	}
	if err := a.store.UpdateWebAuthnSignCount(ctx, user.ID, credID, newCount); err != nil {
		return err
	}
	return nil
}

// hasWebAuthnCredentials reports whether the user has at least one
// registered credential. The task spec calls for "WebAuthn first if
// any credentials enrolled" in step-up — this is the cheap check the
// HTTP layer uses to decide whether to surface the WebAuthn prompt
// vs. the TOTP modal.
func (a *Auth) hasWebAuthnCredentials(ctx context.Context, userID string) (bool, error) {
	row := a.store.DB.QueryRowContext(ctx,
		`SELECT 1 FROM webauthn_credentials WHERE user_id = ? LIMIT 1`, userID)
	var n int
	err := row.Scan(&n)
	if err != nil {
		return false, nil //nolint:nilerr // sql.ErrNoRows folds to "false, nil"
	}
	return true, nil
}

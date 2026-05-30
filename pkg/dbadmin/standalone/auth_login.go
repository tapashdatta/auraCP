package standalone

import (
	"context"
	"errors"
)

// ErrMFARequired is returned by Login when the user has MFA enrolled or
// required and the supplied LoginRequest lacks a TOTP code.
var ErrMFARequired = errors.New("standalone: MFA required")

// ErrInvalidCredentials is returned for any user-facing auth failure
// (bad username, bad password, bad TOTP). Indistinguishable on purpose.
var ErrInvalidCredentials = errors.New("standalone: invalid credentials")

// ErrLockedOut is returned when the IP or user is in an active lockout.
var ErrLockedOut = errors.New("standalone: locked out")

// LoginRequest carries the parameters Login needs. We model it as a
// struct to keep the signature stable as fields are added (recovery
// code, etc.).
type LoginRequest struct {
	Username string
	Password string
	TOTPCode string // optional; required when user.MFARequired

	IPClass string
	UAHash  string
}

// LoginResult is returned on success.
type LoginResult struct {
	UserID     string
	Username   string
	RawToken   string // set the cookie to this value
	IdleTTL    int64  // seconds
	AbsoluteTTL int64 // seconds
}

// Login is the canonical username+password (+ optional TOTP) entry
// point. Used by aura-db's /login handler (delivered in a later PR)
// and by the CLI's user-passwd --self.
//
// C5: a single clock snapshot is taken at the top of the request and
// reused for every time-sensitive step (lockout check, TOTP step
// computation, session creation). Without this, an attacker who can
// stall the request between password verify and TOTP verify could
// straddle a 30-second step boundary and accept a stale code.
func (a *Auth) Login(ctx context.Context, req LoginRequest) (*LoginResult, error) {
	now := a.clock()

	if locked, _ := a.IsLocked(ctx, "ip:"+req.IPClass); locked {
		return nil, ErrLockedOut
	}
	if locked, _ := a.IsLocked(ctx, userScopeKey(req.Username)); locked {
		return nil, ErrLockedOut
	}

	user, err := a.store.GetUserByUsername(ctx, req.Username)
	if errors.Is(err, ErrUserNotFound) {
		// Constant-time decoy to avoid enumeration via timing.
		PHCWithFakeWorkload(a.cfg.Password)
		a.recordLoginFailure(ctx, req)
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	if user.Disabled {
		return nil, ErrInvalidCredentials
	}

	ok, needsRehash, verr := VerifyPassword(req.Password, user.PasswordHash, a.cfg.Password)
	if verr != nil {
		return nil, verr
	}
	if !ok {
		a.recordLoginFailure(ctx, req)
		return nil, ErrInvalidCredentials
	}

	// MFA gate.
	var matchedTOTPStep int64
	if user.MFARequired {
		switch {
		case req.TOTPCode != "":
			if len(user.MFASecretEnc) == 0 {
				// User flagged required but no secret enrolled — admin error.
				a.recordUserScopeFailure(ctx, req)
				return nil, ErrMFARequired
			}
			secret, derr := open(a.kek.Bytes(), user.MFASecretEnc, mfaAAD(user.ID))
			if derr != nil {
				return nil, derr
			}
			step, terr := VerifyTOTP(secret, req.TOTPCode, now)
			if terr != nil {
				for i := range secret {
					secret[i] = 0
				}
				a.recordLoginFailure(ctx, req)
				return nil, ErrInvalidCredentials
			}
			for i := range secret {
				secret[i] = 0
			}
			// SEC-02 replay window: a code that matches at step N is only
			// valid if N is strictly greater than the last accepted step.
			if step <= user.LastTOTPStep {
				a.recordLoginFailure(ctx, req)
				return nil, ErrInvalidCredentials
			}
			matchedTOTPStep = step
		default:
			// SEC-03: password was correct but no second factor
			// supplied. Don't hand attackers an unbounded
			// password-correctness oracle — count the attempt against
			// the per-user lockout. We skip the IP-scope bump so a
			// real user mid-flow isn't punished.
			a.recordUserScopeFailure(ctx, req)
			return nil, ErrMFARequired
		}
	}

	if needsRehash {
		// Best-effort transparent rehash.
		_ = a.store.SetPassword(ctx, user.ID, req.Password, a.cfg.Password)
	}

	rawToken, err := a.createSessionAndCommitTOTPStepAt(ctx, user.ID, req.IPClass, req.UAHash, matchedTOTPStep, now)
	if err != nil {
		return nil, err
	}

	// Successful login clears any per-user failure window. (IP window
	// is left alone — a victim's success doesn't unlock the attacker.)
	_ = a.clearLoginAttempts(ctx, userScopeKey(req.Username))

	return &LoginResult{
		UserID:      user.ID,
		Username:    user.Username,
		RawToken:    rawToken,
		IdleTTL:     int64(a.cfg.IdleTTL.Seconds()),
		AbsoluteTTL: int64(a.cfg.AbsoluteTTL.Seconds()),
	}, nil
}

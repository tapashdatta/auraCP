package standalone

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// userScopeKey returns the per-user lockout scope key with the username
// normalized to lowercase. Without normalization, an attacker could rotate
// the case of the username ("alice" → "Alice" → "ALICE") to bypass the
// per-user lockout counter — the `users.username` column uses COLLATE
// NOCASE so all variants resolve to the same row, but the scope key was
// case-sensitive.
func userScopeKey(username string) string {
	return "user:" + strings.ToLower(username)
}

// lockoutWindow is the sliding window over which failed attempts count.
const lockoutWindow = 15 * time.Minute

// IsLocked reports whether scope (e.g. "ip:1.2.3.0/24" or
// "user:<name>") has an active lockout row.
func (a *Auth) IsLocked(ctx context.Context, scope string) (bool, error) {
	if scope == "" {
		return false, nil
	}
	row := a.store.DB.QueryRowContext(ctx, `SELECT expires_at FROM lockouts WHERE scope = ?`, scope)
	var exp int64
	if err := row.Scan(&exp); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return a.clock().UnixNano() < exp, nil
}

// recordLoginFailure inserts attempt rows and, when the count exceeds
// the configured limit, escalates a lockout entry.
func (a *Auth) recordLoginFailure(ctx context.Context, req LoginRequest) {
	now := a.clock()
	since := now.Add(-lockoutWindow).UnixNano()

	scopes := []struct {
		key   string
		limit int
	}{
		{"ip:" + req.IPClass, a.cfg.LoginPerIP15m},
		{userScopeKey(req.Username), a.cfg.LoginPerUser15m},
	}
	for _, s := range scopes {
		if s.key == "ip:" || s.key == "user:" {
			continue
		}
		_, _ = a.store.DB.ExecContext(ctx,
			`INSERT INTO login_attempts (scope, attempted_at) VALUES (?, ?)`, s.key, now.UnixNano())
		var count int
		row := a.store.DB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM login_attempts WHERE scope = ? AND attempted_at >= ?`, s.key, since)
		if err := row.Scan(&count); err != nil {
			continue
		}
		if s.limit > 0 && count >= s.limit {
			a.escalateLockout(ctx, s.key, now)
		}
	}
}

func (a *Auth) escalateLockout(ctx context.Context, scope string, now time.Time) {
	// Read current lockout (if any) to determine the escalation index.
	var idx int
	row := a.store.DB.QueryRowContext(ctx, `SELECT count FROM lockouts WHERE scope = ?`, scope)
	if err := row.Scan(&idx); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return
	}
	if idx >= len(a.cfg.Escalation) {
		idx = len(a.cfg.Escalation) - 1
	}
	ttl := a.cfg.Escalation[idx]
	expires := now.Add(ttl).UnixNano()
	_, _ = a.store.DB.ExecContext(ctx, `
		INSERT INTO lockouts (scope, count, expires_at) VALUES (?, ?, ?)
		ON CONFLICT(scope) DO UPDATE SET count = lockouts.count + 1, expires_at = excluded.expires_at`,
		scope, idx+1, expires)
}

// recordUserScopeFailure bumps the per-user lockout counter ONLY (no IP
// scope). Used for the MFA-required-but-missing path (SEC-03): the
// password was correct, so we must count the attempt against the lockout
// to deny the attacker an unbounded password-correctness oracle, but we
// skip the per-IP counter to avoid locking out the real user mid-flow
// from their own browser.
func (a *Auth) recordUserScopeFailure(ctx context.Context, req LoginRequest) {
	now := a.clock()
	since := now.Add(-lockoutWindow).UnixNano()
	scope := userScopeKey(req.Username)
	if scope == "user:" {
		return
	}
	_, _ = a.store.DB.ExecContext(ctx,
		`INSERT INTO login_attempts (scope, attempted_at) VALUES (?, ?)`, scope, now.UnixNano())
	var count int
	row := a.store.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM login_attempts WHERE scope = ? AND attempted_at >= ?`, scope, since)
	if err := row.Scan(&count); err != nil {
		return
	}
	if a.cfg.LoginPerUser15m > 0 && count >= a.cfg.LoginPerUser15m {
		a.escalateLockout(ctx, scope, now)
	}
}

// clearLoginAttempts removes attempt rows for scope. Called on successful login.
func (a *Auth) clearLoginAttempts(ctx context.Context, scope string) error {
	_, err := a.store.DB.ExecContext(ctx, `DELETE FROM login_attempts WHERE scope = ?`, scope)
	return err
}

// CleanupLockouts removes expired lockout + attempt rows.
func (a *Auth) CleanupLockouts(ctx context.Context) error {
	now := a.clock().UnixNano()
	cutoff := a.clock().Add(-lockoutWindow).UnixNano()
	if _, err := a.store.DB.ExecContext(ctx, `DELETE FROM lockouts WHERE expires_at < ?`, now); err != nil {
		return err
	}
	if _, err := a.store.DB.ExecContext(ctx, `DELETE FROM login_attempts WHERE attempted_at < ?`, cutoff); err != nil {
		return err
	}
	return nil
}

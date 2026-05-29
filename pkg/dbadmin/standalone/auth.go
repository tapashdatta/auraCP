package standalone

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// SessionCookieName is the cookie used to carry the standalone session
// token. Matches SECURITY.md §5.4.
const SessionCookieName = "aura_session"

// SessionAttrTokenHashKey is the historical attrs key for the
// hex-encoded session-token hash.
//
// user-attrs-leak-token-hash: as of PR #9.5, Auth no longer surfaces
// the full token hash through User.Attrs (only the truncated 16-char
// session_id is exposed). Step-up lookups now go through an internal
// session_id -> tokenHash index inside Auth. The constant is kept for
// external SDK consumers that previously read this key from
// dbadmin.User but is never written by Auth itself; reading it will
// return "" on PR #9.5+ deployments.
const SessionAttrTokenHashKey = "session_token_hash"

// Auth implements dbadmin.Auth backed by the standalone SQLite store.
type Auth struct {
	store *Store
	kek   *KEK
	cfg   AuthRuntimeConfig
	clock Clock

	// sessionTokenIndex maps the truncated session_id (first 16 chars
	// of hex token hash, surfaced via User.Attrs["session_id"]) to the
	// full 32-byte token hash. Lets HasSteppedUp / VerifyStepUp look
	// up the hash without leaking it into User.Attrs
	// (user-attrs-leak-token-hash). Bounded by the active sessions
	// table: entries are added on Authenticate, refreshed on every
	// touch, and forgotten on revoke / cleanup. Worst-case size is
	// MaxConcurrent * users active in the last IdleTTL window.
	sessionMu          sync.RWMutex
	sessionTokenIndex  map[string][]byte
}

// AuthRuntimeConfig holds the policy values Auth needs at every method
// call. Built from the standalone Config + dbadmin.Config in Bootstrap.
type AuthRuntimeConfig struct {
	IdleTTL         time.Duration
	AbsoluteTTL     time.Duration
	MaxConcurrent   int
	BindIPClass     bool
	BindUAHash      bool
	Password        PasswordPolicy
	LoginPerIP15m   int
	LoginPerUser15m int
	Escalation      []time.Duration
	StepUpTTL       map[dbadmin.Action]time.Duration
}

// DefaultStepUpTTL returns the per-action step-up window table.
func DefaultStepUpTTL() map[dbadmin.Action]time.Duration {
	return map[dbadmin.Action]time.Duration{
		dbadmin.ActionConnPwdView:    30 * time.Second,
		dbadmin.ActionConnUpdate:     60 * time.Second,
		dbadmin.ActionConnDelete:     60 * time.Second,
		dbadmin.ActionConnGrantMgmt:  5 * time.Minute,
		dbadmin.ActionQueryDDL:       5 * time.Minute,
		dbadmin.ActionQueryDangerous: 60 * time.Second,
		dbadmin.ActionRestore:        60 * time.Second,
		dbadmin.ActionAuditConfig:    60 * time.Second,
	}
}

// NewAuth constructs an Auth.
func NewAuth(store *Store, kek *KEK, cfg AuthRuntimeConfig) *Auth {
	if cfg.StepUpTTL == nil {
		cfg.StepUpTTL = DefaultStepUpTTL()
	}
	if cfg.Escalation == nil {
		cfg.Escalation = []time.Duration{
			15 * time.Minute,
			30 * time.Minute,
			1 * time.Hour,
			2 * time.Hour,
			4 * time.Hour,
			8 * time.Hour,
			24 * time.Hour,
		}
	}
	return &Auth{
		store:             store,
		kek:               kek,
		cfg:               cfg,
		clock:             store.clock,
		sessionTokenIndex: make(map[string][]byte),
	}
}

// rememberToken indexes the truncated session_id -> full token hash
// mapping used by step-up lookups. user-attrs-leak-token-hash: this
// keeps the full hash inside Auth instead of leaking it through
// User.Attrs.
func (a *Auth) rememberToken(shortID string, tokenHash []byte) {
	if a.sessionTokenIndex == nil {
		return
	}
	cp := make([]byte, len(tokenHash))
	copy(cp, tokenHash)
	a.sessionMu.Lock()
	a.sessionTokenIndex[shortID] = cp
	a.sessionMu.Unlock()
}

// forgetToken drops a (truncated) session_id from the in-process
// index. Called on revoke paths.
func (a *Auth) forgetToken(shortID string) {
	if a.sessionTokenIndex == nil {
		return
	}
	a.sessionMu.Lock()
	delete(a.sessionTokenIndex, shortID)
	a.sessionMu.Unlock()
}

// lookupToken returns the full token hash for a session_id (truncated
// 16-char hex) previously seen on this process.
func (a *Auth) lookupToken(shortID string) ([]byte, bool) {
	if a.sessionTokenIndex == nil {
		return nil, false
	}
	a.sessionMu.RLock()
	v, ok := a.sessionTokenIndex[shortID]
	a.sessionMu.RUnlock()
	if !ok {
		return nil, false
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, true
}

// Authenticate implements dbadmin.Auth.
func (a *Auth) Authenticate(r *http.Request) (dbadmin.User, error) {
	ctx := r.Context()
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		return dbadmin.User{}, dbadmin.ErrUnauthenticated
	}
	tokenHash := hashSessionToken(cookie.Value)

	sess, err := a.getSessionByTokenHash(ctx, tokenHash)
	if err != nil {
		if errors.Is(err, errSessionNotFound) {
			return dbadmin.User{}, dbadmin.ErrUnauthenticated
		}
		return dbadmin.User{}, err
	}
	now := a.clock()
	// C1: boundary is inclusive — a session whose ExpiresAt equals the
	// current nanosecond IS expired. The strict `>` previously let one
	// final request slip through at the boundary tick.
	if now.UnixNano() >= sess.ExpiresAt || now.UnixNano() >= sess.AbsoluteExpiresAt {
		_ = a.revokeSession(ctx, tokenHash)
		return dbadmin.User{}, dbadmin.ErrUnauthenticated
	}

	if a.cfg.BindIPClass {
		if got := IPClass(r); got != sess.IPClass {
			_ = a.revokeSession(ctx, tokenHash)
			return dbadmin.User{}, dbadmin.ErrUnauthenticated
		}
	}
	if a.cfg.BindUAHash {
		if got := UAHash(r); got != sess.UAHash {
			_ = a.revokeSession(ctx, tokenHash)
			return dbadmin.User{}, dbadmin.ErrUnauthenticated
		}
	}

	// Touch — slide IdleTTL but never past AbsoluteExpiresAt.
	newExp := now.Add(a.cfg.IdleTTL).UnixNano()
	if newExp > sess.AbsoluteExpiresAt {
		newExp = sess.AbsoluteExpiresAt
	}
	if _, err := a.store.DB.ExecContext(ctx,
		`UPDATE sessions SET last_used_at = ?, expires_at = ? WHERE token_hash = ?`,
		now.UnixNano(), newExp, tokenHash); err != nil {
		return dbadmin.User{}, err
	}

	user, err := a.store.GetUserByID(ctx, sess.UserID)
	if err != nil {
		return dbadmin.User{}, err
	}
	if user.Disabled {
		_ = a.revokeSession(ctx, tokenHash)
		return dbadmin.User{}, dbadmin.ErrUnauthenticated
	}

	roles, err := a.loadRoles(ctx, user.ID)
	if err != nil {
		return dbadmin.User{}, err
	}

	tokHex := hex.EncodeToString(tokenHash)
	shortID := tokHex[:16]
	// Index the truncated id -> full hash mapping so HasSteppedUp /
	// VerifyStepUp can look up step-up flags WITHOUT us having to
	// surface the full hash through User.Attrs
	// (user-attrs-leak-token-hash).
	a.rememberToken(shortID, tokenHash)
	return dbadmin.User{
		ID:       user.ID,
		Username: user.Username,
		Roles:    roles,
		Attrs: map[string]string{
			"ip_class":   sess.IPClass,
			"ua_hash":    sess.UAHash,
			"session_id": shortID,
		},
	}, nil
}

// HasPermission implements dbadmin.Auth.
func (a *Auth) HasPermission(u dbadmin.User, connID dbadmin.ConnectionID, action dbadmin.Action) (bool, error) {
	if u.ID == "" {
		return false, nil
	}
	if connID == "" {
		// Global actions.
		switch action {
		case dbadmin.ActionConnList:
			// Any role on any connection lets the user enumerate
			// (their) connections; a user with no grants gets an
			// empty list but still passes the auth check.
			return true, nil
		case dbadmin.ActionConnCreate:
			// Treat "is owner on at least one conn" as the
			// proxy-for-admin marker. A fresh deployment must
			// bootstrap the first owner out-of-band (aura-db
			// user-create --grant-all-owner ...).
			for _, r := range u.Roles {
				if r >= dbadmin.RoleOwner {
					return true, nil
				}
			}
			return false, nil
		default:
			return false, nil
		}
	}
	have := u.Roles[connID]
	min := action.MinRole()
	if min == dbadmin.RoleNone {
		return false, nil
	}
	return have >= min, nil
}

// StepUpRequired implements dbadmin.Auth.
func (a *Auth) StepUpRequired(action dbadmin.Action) bool {
	return action.RequiresStepUp()
}

// loadRoles returns the per-connection role map for a user.
func (a *Auth) loadRoles(ctx context.Context, userID string) (map[dbadmin.ConnectionID]dbadmin.Role, error) {
	rows, err := a.store.DB.QueryContext(ctx, `SELECT connection_id, role FROM connection_grants WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[dbadmin.ConnectionID]dbadmin.Role)
	for rows.Next() {
		var cid string
		var r int
		if err := rows.Scan(&cid, &r); err != nil {
			return nil, err
		}
		out[dbadmin.ConnectionID(cid)] = dbadmin.Role(r)
	}
	return out, rows.Err()
}

// hashSessionToken computes the storage-side digest for a raw token.
func hashSessionToken(raw string) []byte {
	sum := sha256.Sum256([]byte(strings.TrimSpace(raw)))
	return sum[:]
}

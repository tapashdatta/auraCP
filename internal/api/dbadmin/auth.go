package dbadmin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/auracp/auracp/internal/api"
	"github.com/auracp/auracp/internal/auth"
	"github.com/auracp/auracp/internal/perm"
	"github.com/auracp/auracp/internal/store"
	"github.com/auracp/auracp/pkg/dbadmin"
)

// CurrentUserFunc resolves the panel user from a request. FIX-7 / INT-11:
// returns the minimal IdentitySummary (no PasswordHash / TOTPSecret)
// rather than the full store.User. Panel composition wires
// api.ResolveIdentity here.
type CurrentUserFunc func(*http.Request) (api.IdentitySummary, bool)

// panelAuth implements dbadmin.Auth on top of the panel session cookie +
// the panel's existing TOTP secret.
type panelAuth struct {
	store     *store.Store
	currentFn CurrentUserFunc
	conns     *panelConns
	stepUp    *stepUpStore
	stepUpTTL time.Duration
}

const panelSessionCookie = "auracp_session"

// newPanelAuth constructs a panelAuth. The step-up store is owned by
// this adapter; close it via the io.Closer returned from Mount.
func newPanelAuth(st *store.Store, conns *panelConns, currentFn CurrentUserFunc, stepUp *stepUpStore) *panelAuth {
	return &panelAuth{
		store:     st,
		currentFn: currentFn,
		conns:     conns,
		stepUp:    stepUp,
		stepUpTTL: 5 * time.Minute,
	}
}

// Authenticate maps the panel session to a dbadmin.User. Returns
// dbadmin.ErrUnauthenticated on missing/expired session.
func (a *panelAuth) Authenticate(r *http.Request) (dbadmin.User, error) {
	u, ok := a.currentFn(r)
	if !ok {
		return dbadmin.User{}, dbadmin.ErrUnauthenticated
	}
	ctx := r.Context()
	roles, err := a.conns.RolesFor(ctx, u.UserID, u.Role)
	if err != nil {
		// Don't surface as ErrUnauthenticated — the engine maps non-
		// sentinel errors to 500. The user IS authenticated; we just
		// failed to load grants.
		return dbadmin.User{}, err
	}
	return dbadmin.User{
		ID:       strconv.FormatInt(u.UserID, 10),
		Username: u.Email,
		Roles:    roles,
		Attrs: map[string]string{
			"session_id":  sessionTokenFromRequest(r),
			"ua_hash":     uaHash(r),
			"role":        u.Role,
			"permissions": u.Permissions,
		},
	}, nil
}

// HasPermission consults the panel's role + per-connection grant map.
//
//   - ROLE_ADMIN: yes to everything.
//   - Connection-scoped action: user must hold a grant >= action.MinRole.
//   - ActionConnList: always permitted at the auth layer; List filters.
//   - ActionConnCreate: requires panel granular perm databases:create.
//   - ActionAuditConfig: ROLE_ADMIN only (already handled by the first
//     branch); falls through to default deny for everyone else.
func (a *panelAuth) HasPermission(u dbadmin.User, cid dbadmin.ConnectionID, act dbadmin.Action) (bool, error) {
	if u.Attrs != nil && u.Attrs["role"] == "ROLE_ADMIN" {
		return true, nil
	}
	if act == dbadmin.ActionConnList {
		return true, nil
	}
	if act == dbadmin.ActionConnCreate {
		role := ""
		permissions := ""
		if u.Attrs != nil {
			role = u.Attrs["role"]
			permissions = u.Attrs["permissions"]
		}
		return perm.Parse(permissions, role).Can("databases", "create"), nil
	}
	if cid == "" {
		// Global action other than List/Create — default deny.
		return false, nil
	}
	have, ok := u.Roles[cid]
	if !ok {
		return false, nil
	}
	min := act.MinRole()
	if min == dbadmin.RoleNone {
		// Unknown action — fail closed.
		return false, nil
	}
	return have >= min, nil
}

// StepUpRequired delegates to the canonical default. Hosts MAY require
// step-up for additional actions; we do not in PR #10.
func (a *panelAuth) StepUpRequired(act dbadmin.Action) bool {
	return act.RequiresStepUp()
}

// HasSteppedUp reports whether the user holds a valid step-up flag for
// the action. Keyed by (panel session token, action).
func (a *panelAuth) HasSteppedUp(u dbadmin.User, act dbadmin.Action) bool {
	if u.Attrs == nil {
		return false
	}
	return a.stepUp.has(u.Attrs["session_id"], act)
}

// VerifyStepUp consumes a TOTP code against the panel's existing user
// secret. The request body is JSON: { "code": "123456", "action": "<action>" }.
// On success, persists the step-up flag for the duration of stepUpTTL.
//
// Returns dbadmin.ErrUnauthenticated when:
//   - the user has no panel session (impossible reach via the router, but
//     defensive);
//   - the user has no TOTP secret enrolled;
//   - the code does not validate.
//
// FIX-7 / INT-11: the IdentitySummary returned by currentFn intentionally
// does NOT carry TOTPSecret. We fetch the secret directly from the
// store here, scoped to this single call, instead of leaking it across
// the package boundary on every Authenticate.
func (a *panelAuth) VerifyStepUp(r *http.Request) (dbadmin.Action, time.Duration, error) {
	ident, ok := a.currentFn(r)
	if !ok {
		return "", 0, dbadmin.ErrUnauthenticated
	}
	if !ident.MFAEnabled {
		return "", 0, dbadmin.ErrUnauthenticated
	}
	var in struct {
		Code   string         `json:"code"`
		Action dbadmin.Action `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return "", 0, dbadmin.ErrInvalidInput
	}
	if in.Action == "" {
		return "", 0, dbadmin.ErrInvalidInput
	}
	// Fetch the full user record only when we actually need TOTPSecret.
	u, err := a.store.UserByID(ident.UserID)
	if err != nil {
		return "", 0, dbadmin.ErrUnauthenticated
	}
	if !auth.VerifyTOTP(u.TOTPSecret.String, in.Code) {
		return "", 0, dbadmin.ErrUnauthenticated
	}
	a.stepUp.set(sessionTokenFromRequest(r), in.Action, a.stepUpTTL)
	return in.Action, a.stepUpTTL, nil
}

// sessionTokenFromRequest returns the raw panel session cookie value, or
// "" if no cookie is present. Note: the value is the secret session
// token; we use it only as a private in-process map key (never logged,
// never echoed back).
func sessionTokenFromRequest(r *http.Request) string {
	c, err := r.Cookie(panelSessionCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

// uaHash returns the first 16 hex chars of SHA-256(User-Agent header).
// Empty UA hashes to the empty string so missing UA is distinguishable
// from a real one in audit events.
func uaHash(r *http.Request) string {
	ua := r.Header.Get("User-Agent")
	if ua == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(ua))
	return hex.EncodeToString(sum[:])[:16]
}

// Compile-time interface assertion.
var _ dbadmin.Auth = (*panelAuth)(nil)

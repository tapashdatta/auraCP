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
//   - ROLE_ADMIN: yes to everything (but step-up still required if the
//     action calls for it — see HasSteppedUp / StepUpRequired).
//   - Connection-scoped action: user must hold a grant >= action.MinRole.
//   - ActionConnList: always permitted at the auth layer; List filters.
//   - ActionConnCreate: requires panel granular perm databases:create.
//   - ActionAuditConfig: ROLE_ADMIN only (already handled by the first
//     branch); falls through to default deny for everyone else.
//
// PR #10.5 / FIX-PD-SEC-06: prior to this revision the ROLE_ADMIN
// branch returned true unconditionally — true authorization-wise, but
// in callers that conflated "permitted" with "no step-up needed" it
// produced a parity gap where ROLE_ADMIN could perform a step-up-
// required action (DROP TABLE on prod) without ever presenting a
// second factor. HasPermission is intentionally narrow now: it answers
// only the "is the user authorized" question. The orthogonal "did they
// just step-up" question is asked separately via HasSteppedUp, and the
// engine asks both before invoking the action. Net effect: admins
// hitting a step-up action see the same MFA prompt non-admins do.
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
// the action. Keyed by (panel session token, action class, connection id).
//
// PR #10.5 / FIX-INT-12 + FIX-SDK-3: keying changed from (session,
// raw Action) to (session, ActionClass, connectionID). The class
// grouping means one step-up authorizes any sibling action in the
// class for the TTL (e.g., approving one DDL approves further DDL on
// the same connection); the connection-id binding stops a step-up for
// connection A from authorizing a destructive action on connection B
// (a category mistake the operator would not have intended).
//
// For global actions (audit config, etc.) the connection id is empty;
// the class key alone scopes the flag.
func (a *panelAuth) HasSteppedUp(u dbadmin.User, act dbadmin.Action) bool {
	if u.Attrs == nil {
		return false
	}
	return a.stepUp.hasClass(u.Attrs["session_id"], act.Class(), connIDFromAction(u, act))
}

// VerifyStepUp consumes a TOTP code against the panel's existing user
// secret. The request body is JSON: { "code": "123456", "action": "<action>",
// "connection_id": "<id>" }. On success, persists the step-up flag for
// the duration of stepUpTTL.
//
// Error sentinels:
//   - ErrUnauthenticated — no panel session, or the TOTP code is wrong;
//   - ErrStepUpUnavailable — session is valid but the user has no
//     second factor enrolled (PR #10.5 / FIX-SDK-1). Pre-#10.5 this
//     case was folded into ErrUnauthenticated which dead-ended the
//     panel SPA in a relogin loop the operator could not escape;
//   - ErrInvalidInput — malformed request body.
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
		// FIX-SDK-1: distinct sentinel so the SPA can route the
		// operator to MFA enrollment instead of a relogin loop.
		return "", 0, dbadmin.ErrStepUpUnavailable
	}
	var in struct {
		Code         string         `json:"code"`
		Action       dbadmin.Action `json:"action"`
		ConnectionID string         `json:"connection_id"`
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
	// FIX-INT-12 / FIX-SDK-3: store by (session, class, conn id) so
	// the grant authorizes related actions on the same connection only.
	a.stepUp.setClass(sessionTokenFromRequest(r), in.Action.Class(), in.ConnectionID, a.stepUpTTL)
	return in.Action, a.stepUpTTL, nil
}

// connIDFromAction extracts the connection id the engine is asking
// about, used as part of the step-up cache key (FIX-INT-12). For
// global / non-connection-scoped actions (audit config, etc.) the
// class alone is the key; we return "".
//
// The engine threads the connection id into User.Attrs["conn_id"]
// before invoking HasSteppedUp on connection-scoped paths (see
// pkg/dbadmin/engine.go's request decorator). When that's empty we
// fall back to "" and the step-up flag is class-scoped only.
func connIDFromAction(u dbadmin.User, act dbadmin.Action) string {
	if u.Attrs == nil {
		return ""
	}
	switch act.Class() {
	case dbadmin.ActionClassConnAdmin,
		dbadmin.ActionClassDDL,
		dbadmin.ActionClassDangerous,
		dbadmin.ActionClassRestore:
		return u.Attrs["conn_id"]
	default:
		return ""
	}
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

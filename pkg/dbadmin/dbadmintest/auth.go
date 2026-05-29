package dbadmintest

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// Auth is an in-memory dbadmin.Auth implementation for tests. Sessions are
// keyed by an opaque token the test code sets via the `X-Test-User` request
// header (or the `aura_test_user` cookie); the implementation looks up the
// matching User by ID.
//
// This is a TEST implementation. It performs no cryptographic verification
// of anything and grants step-up freely when configured to.
type Auth struct {
	mu               sync.RWMutex
	users            map[string]dbadmin.User
	grants           map[string]map[dbadmin.ConnectionID]dbadmin.Role
	steppedUp        map[string]map[dbadmin.Action]time.Time
	stepUpDefaultTTL time.Duration

	// authError, if non-nil, is returned by Authenticate regardless of
	// the request. Useful for testing 401 / 500 paths.
	authError error
}

// NewAuth constructs an empty Auth. Default step-up TTL is 5 minutes;
// override with WithStepUpTTL.
func NewAuth() *Auth {
	return &Auth{
		users:            map[string]dbadmin.User{},
		grants:           map[string]map[dbadmin.ConnectionID]dbadmin.Role{},
		steppedUp:        map[string]map[dbadmin.Action]time.Time{},
		stepUpDefaultTTL: 5 * time.Minute,
	}
}

// WithUser registers a user. id is what Authenticate returns; username is
// the display name in audit events. Returns the receiver for chaining.
func (a *Auth) WithUser(id, username string) *Auth {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.users[id] = dbadmin.User{ID: id, Username: username, Roles: map[dbadmin.ConnectionID]dbadmin.Role{}}
	return a
}

// WithGrant assigns a role on a connection to a user. The user must
// already exist via WithUser.
func (a *Auth) WithGrant(userID string, conn dbadmin.ConnectionID, role dbadmin.Role) *Auth {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.users[userID]; !ok {
		// Auto-register a placeholder user with no username — tests
		// that forget WithUser still work.
		a.users[userID] = dbadmin.User{ID: userID, Roles: map[dbadmin.ConnectionID]dbadmin.Role{}}
	}
	u := a.users[userID]
	if u.Roles == nil {
		u.Roles = map[dbadmin.ConnectionID]dbadmin.Role{}
	}
	u.Roles[conn] = role
	a.users[userID] = u
	if a.grants[userID] == nil {
		a.grants[userID] = map[dbadmin.ConnectionID]dbadmin.Role{}
	}
	a.grants[userID][conn] = role
	return a
}

// WithStepUpVerified pre-arms the step-up flag for a user + action class
// with the default TTL. Useful for testing the "happy path" where step-up
// has already been performed.
func (a *Auth) WithStepUpVerified(userID string, action dbadmin.Action) *Auth {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.steppedUp[userID] == nil {
		a.steppedUp[userID] = map[dbadmin.Action]time.Time{}
	}
	a.steppedUp[userID][action] = time.Now().Add(a.stepUpDefaultTTL)
	return a
}

// WithStepUpTTL overrides the default step-up TTL for subsequent
// WithStepUpVerified calls.
func (a *Auth) WithStepUpTTL(d time.Duration) *Auth {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stepUpDefaultTTL = d
	return a
}

// WithAuthError pins Authenticate to return the given error. Useful for
// testing 401 / 500 paths in the engine. Pass nil to clear.
func (a *Auth) WithAuthError(err error) *Auth {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.authError = err
	return a
}

// Authenticate reads the user ID from the X-Test-User header or the
// aura_test_user cookie and returns the matching User. Returns
// ErrUnauthenticated if neither is present or the user is unknown.
func (a *Auth) Authenticate(r *http.Request) (dbadmin.User, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.authError != nil {
		return dbadmin.User{}, a.authError
	}
	id := r.Header.Get("X-Test-User")
	if id == "" {
		if c, err := r.Cookie("aura_test_user"); err == nil {
			id = c.Value
		}
	}
	if id == "" {
		return dbadmin.User{}, dbadmin.ErrUnauthenticated
	}
	u, ok := a.users[id]
	if !ok {
		return dbadmin.User{}, dbadmin.ErrUnauthenticated
	}
	return u, nil
}

// HasPermission applies the default policy: action ≤ role on the
// connection.
func (a *Auth) HasPermission(u dbadmin.User, conn dbadmin.ConnectionID, action dbadmin.Action) (bool, error) {
	if u.ID == "" {
		return false, nil
	}
	role := u.Roles[conn]
	return action.MinRole() <= role, nil
}

// StepUpRequired reports the canonical default per Action.RequiresStepUp.
func (a *Auth) StepUpRequired(action dbadmin.Action) bool {
	return action.RequiresStepUp()
}

// VerifyStepUp accepts any request and arms the step-up flag for the
// user identified by X-Test-User. The action class is taken from the
// X-Test-StepUp-Action header.
//
// Returns ErrUnauthenticated if the user isn't recognized. Returns an
// error if the X-Test-StepUp-Action header is missing.
func (a *Auth) VerifyStepUp(r *http.Request) (dbadmin.Action, time.Duration, error) {
	id := r.Header.Get("X-Test-User")
	if id == "" {
		return "", 0, dbadmin.ErrUnauthenticated
	}
	action := dbadmin.Action(r.Header.Get("X-Test-StepUp-Action"))
	if action == "" {
		return "", 0, errors.New("dbadmintest: X-Test-StepUp-Action header required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.users[id]; !ok {
		return "", 0, dbadmin.ErrUnauthenticated
	}
	if a.steppedUp[id] == nil {
		a.steppedUp[id] = map[dbadmin.Action]time.Time{}
	}
	ttl := a.stepUpDefaultTTL
	a.steppedUp[id][action] = time.Now().Add(ttl)
	return action, ttl, nil
}

// HasSteppedUp reports whether the user has a valid, unexpired step-up
// flag for the action class.
func (a *Auth) HasSteppedUp(u dbadmin.User, action dbadmin.Action) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.steppedUp[u.ID] == nil {
		return false
	}
	expiry, ok := a.steppedUp[u.ID][action]
	return ok && time.Now().Before(expiry)
}

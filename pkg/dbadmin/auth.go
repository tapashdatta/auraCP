package dbadmin

import (
	"net/http"
	"time"
)

// Auth resolves operator identity and per-action authority. Implementations
// bridge to whatever authentication system hosts Aura DB:
//
//   - In integrated mode (auracpd), the implementation reads the panel's
//     session cookie and joins onto the panel's user/grant tables.
//   - In standalone mode (cmd/aura-db), the implementation uses the
//     SQLite-backed users + sessions + step-up-flags tables described in
//     pkg/dbadmin/standalone.
//
// The engine never sees the underlying authentication mechanism — only the
// resolved User. The engine never persists Users; it re-authenticates on
// every request via Authenticate.
//
// Authentication contract:
//
//   - Authenticate is called exactly once per request, before any other
//     Auth method. The engine's request-scoped User is captured from its
//     return value and reused for the remainder of the request.
//
//   - HasPermission may be called many times per request (once per
//     resource the request touches). It must be fast — cache role lookups
//     in the User struct, or the implementation's own cache.
//
//   - StepUpRequired is called with every action the engine is about to
//     perform. Implementations should return the canonical default
//     (Action.RequiresStepUp); they MAY return true for additional
//     actions but MUST NOT return false for actions where the default is
//     true.
//
//   - HasSteppedUp is consulted whenever StepUpRequired returned true.
//     The engine has no concept of step-up state; the Auth implementation
//     owns it (typically as session-scoped flags keyed by action class
//     with a TTL).
//
//   - VerifyStepUp is the bridge from "operator clicked 'verify'" to
//     "step-up flag exists." It validates a WebAuthn assertion / TOTP
//     code / recovery code embedded in the request body and persists the
//     step-up flag with the returned TTL. The engine then has the
//     authorized window during which HasSteppedUp returns true for the
//     returned Action class.
type Auth interface {
	// Authenticate inspects the incoming request and returns the
	// operator it represents.
	//
	// Returns ErrUnauthenticated if no valid session is present (no
	// cookie, expired token, forged signature, broken session
	// binding). The engine maps the error to HTTP 401 and short-
	// circuits the request.
	//
	// Other errors indicate I/O or system failure (e.g., the session
	// store is down). The engine maps these to HTTP 500 and logs the
	// underlying error server-side.
	Authenticate(*http.Request) (User, error)

	// HasPermission decides whether the user is authorized to perform
	// the action against the connection.
	//
	// For global actions (ActionConnList, ActionConnCreate),
	// ConnectionID is empty.
	//
	// Return false for "no" — do NOT return an error to signal
	// denial. Errors are reserved for I/O failures.
	HasPermission(User, ConnectionID, Action) (bool, error)

	// StepUpRequired reports whether the action requires fresh MFA
	// verification beyond the standing session.
	//
	// Implementations SHOULD return Action.RequiresStepUp() unless
	// they impose stricter policy. The engine calls this synchronously
	// before each potentially-protected action.
	StepUpRequired(Action) bool

	// VerifyStepUp validates a step-up assertion embedded in the
	// request and persists the resulting flag.
	//
	// Returns the Action class the user has just stepped up for and
	// the TTL of the step-up flag. The engine uses the returned class
	// to scope HasSteppedUp checks for the rest of the action.
	//
	// Implementations MUST validate the assertion against the user's
	// enrolled MFA factors (WebAuthn credential, TOTP secret, recovery
	// code) and reject anything unrecognized.
	VerifyStepUp(*http.Request) (Action, time.Duration, error)

	// HasSteppedUp reports whether the user holds a valid, unexpired
	// step-up flag for the action class.
	//
	// The engine calls this after StepUpRequired returns true and
	// before performing the action. A false here short-circuits the
	// request with ErrStepUpRequired so the frontend can initiate the
	// step-up flow.
	HasSteppedUp(User, Action) bool
}

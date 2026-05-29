package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/auracp/auracp/internal/auth"
	"github.com/auracp/auracp/internal/perm"
	"github.com/auracp/auracp/internal/store"
)

// safeNextPath returns next when it is a safe in-app redirect target, or
// "/" otherwise. FIX-6 (PR #11): the panel login flow accepts a ?next=
// query param so that an Aura DB 401 → /login?next=/dbadmin/ round-trip
// can drop the operator back where they were. Without validation, an
// attacker could construct /login?next=//evil.com/ or /login?next=/\evil
// and turn the panel into an open redirector.
//
// Allow rules:
//   - next must start with exactly one "/"
//   - next must NOT start with "//" or "/\" (protocol-relative)
//   - next must NOT contain a ":" before the first "/" (avoid javascript:
//     and other URL schemes)
//   - empty/missing next → "/"
func safeNextPath(next string) string {
	if next == "" {
		return "/"
	}
	if !strings.HasPrefix(next, "/") {
		return "/"
	}
	if strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	// Reject scheme-style ":" appearing before any "/" (defence in depth —
	// the leading "/" check already covers most cases, but URLs like
	// "/javascript:foo" would slip past). Look at chars BEFORE the next
	// "/" we encounter past index 0.
	if i := strings.IndexByte(next[1:], '/'); i >= 0 {
		if strings.ContainsAny(next[1:1+i], ":") {
			return "/"
		}
	} else if strings.ContainsAny(next[1:], ":") {
		return "/"
	}
	return next
}

const (
	sessionCookie = "auracp_session"
	sessionTTL    = 12 * time.Hour
	issuer        = "auraCP"
)

type userView struct {
	Email      string `json:"email"`
	Role       string `json:"role"`
	MFAEnabled bool   `json:"mfaEnabled"`
}

func view(u store.User) userView {
	return userView{Email: u.Email, Role: u.Role, MFAEnabled: u.MFAEnabled()}
}

// GET /api/auth/setup — is first-run admin creation still required?
func (s *Server) setupStatus(w http.ResponseWriter, r *http.Request) {
	n, _ := s.store.CountUsers()
	writeJSON(w, http.StatusOK, map[string]bool{"setupRequired": n == 0})
}

// POST /api/auth/setup — create the first admin (only allowed when no users
// exist yet), then log them in. Public, but self-disables after first use.
func (s *Server) setupAdmin(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.CountUsers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if n > 0 {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "setup already completed"})
		return
	}
	var in struct{ Email, Password string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.Email == "" || len(in.Password) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email and a password of at least 8 characters are required"})
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// First-run admin: all permissions (handled by Role=ADMIN), all sites scope.
	id, err := s.store.CreateUser(in.Email, hash, "ROLE_ADMIN", "", "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	token, err := auth.RandomToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	_ = s.store.CreateSession(token, id, false, sessionTTL)
	setSessionCookie(w, r, token)
	u, _ := s.store.UserByID(id)
	s.audit(r, "setup.admin-created", in.Email)
	writeJSON(w, http.StatusCreated, map[string]any{"user": view(u)})
}

// POST /api/auth/login — verify password; if MFA is enabled, start a pending session.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email, Password string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	u, err := s.store.UserByEmail(in.Email)
	if err != nil || !auth.CheckPassword(u.PasswordHash, in.Password) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}
	token, err := auth.RandomToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	pending := u.MFAEnabled()
	if err := s.store.CreateSession(token, u.ID, pending, sessionTTL); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	setSessionCookie(w, r, token)
	if pending {
		writeJSON(w, http.StatusOK, map[string]bool{"mfaRequired": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": view(u)})
}

// POST /api/auth/mfa/verify — complete a pending session with a TOTP code.
func (s *Server) mfaVerify(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no session"})
		return
	}
	userID, pending, ok := s.store.Session(c.Value)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no session"})
		return
	}
	u, err := s.store.UserByID(userID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if pending {
		var in struct{ Code string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if !auth.VerifyTOTP(u.TOTPSecret.String, in.Code) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid code"})
			return
		}
		if err := s.store.ClearSessionMFAPending(c.Value); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": view(u)})
}

// GET /api/auth/me — current user, or 401.
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	u, ok := s.currentUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": view(u)})
}

// POST /api/auth/logout
//
// PR #10.5 / FIX-PD-SEC-04: after deleting the session row we fire the
// LogoutHook (wired at startup by cmd/auracpd to the dbadmin adapter's
// stepUpStore) so any in-memory step-up flags bound to this session
// token are dropped immediately. Without that hook the flags survived
// until either the in-memory TTL expired (default 5 minutes) or the
// reaper ticked, which meant a logged-out operator's session token —
// if leaked — could still authorize step-up-required actions for that
// window.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.store.DeleteSession(c.Value)
		if s.logoutHook != nil {
			s.logoutHook(c.Value)
		}
	}
	clearSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// POST /api/auth/mfa/setup — generate a secret + otpauth URI (not yet enabled).
func (s *Server) mfaSetup(w http.ResponseWriter, r *http.Request) {
	u, _ := s.currentUser(r)
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"secret": secret,
		"uri":    auth.TOTPURI(secret, issuer, u.Email),
	})
}

// POST /api/auth/mfa/enable — confirm a code for the secret, then store it.
func (s *Server) mfaEnable(w http.ResponseWriter, r *http.Request) {
	u, _ := s.currentUser(r)
	var in struct{ Secret, Code string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !auth.VerifyTOTP(in.Secret, in.Code) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid code"})
		return
	}
	if err := s.store.SetUserTOTP(u.ID, in.Secret); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// POST /api/auth/mfa/disable
func (s *Server) mfaDisable(w http.ResponseWriter, r *http.Request) {
	u, _ := s.currentUser(r)
	var in struct{ Code string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if u.MFAEnabled() && !auth.VerifyTOTP(u.TOTPSecret.String, in.Code) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid code"})
		return
	}
	if err := s.store.SetUserTOTP(u.ID, ""); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// currentUser resolves a fully-authenticated (non-pending) session.
func (s *Server) currentUser(r *http.Request) (store.User, bool) {
	return resolveCurrentUser(s.store, r)
}

// resolveCurrentUser is the package-private form of currentUser. It is
// intentionally NOT exported: store.User carries PasswordHash and
// TOTPSecret, and we don't want any external caller to be able to grab
// those by reaching across the package boundary. External integrations
// must go through ResolveIdentity instead (FIX-7 / INT-11).
func resolveCurrentUser(st *store.Store, r *http.Request) (store.User, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return store.User{}, false
	}
	userID, pending, ok := st.Session(c.Value)
	if !ok || pending {
		return store.User{}, false
	}
	u, err := st.UserByID(userID)
	if err != nil {
		return store.User{}, false
	}
	return u, true
}

// IdentitySummary is the minimal projection of an authenticated panel
// user exposed to integrations outside the api package. It deliberately
// omits PasswordHash, TOTPSecret, Permissions JSON, CreatedAt, and any
// other column on store.User that could amplify the blast radius of an
// API-package leak (FIX-7 / INT-11).
//
// Add fields here only after auditing that they are safe to share with
// an integration that doesn't fully trust the surrounding code (e.g.
// internal/api/dbadmin runs adjacent to but does not own the panel
// session). Never add PasswordHash or TOTPSecret to this struct.
type IdentitySummary struct {
	UserID     int64
	Email      string
	Role       string
	MFAEnabled bool
	// Permissions is the raw permissions JSON. It carries no secrets
	// (it's a capability map), and dbadmin's HasPermission needs it
	// to authorize ActionConnCreate against the panel databases:create
	// capability.
	Permissions string
}

// ResolveIdentity is the integration-facing replacement for the
// previously-exported ResolveCurrentUser. It resolves the panel session
// cookie to an IdentitySummary — the smallest set of fields the
// internal/api/dbadmin adapter actually needs — and never echoes
// PasswordHash or TOTPSecret across the package boundary (FIX-7 /
// INT-11). Returns ok=false on missing cookie, expired session,
// mfa_pending, or unknown user, mirroring the contract of the legacy
// resolver.
func ResolveIdentity(st *store.Store, r *http.Request) (IdentitySummary, bool) {
	u, ok := resolveCurrentUser(st, r)
	if !ok {
		return IdentitySummary{}, false
	}
	return IdentitySummary{
		UserID:      u.ID,
		Email:       u.Email,
		Role:        u.Role,
		MFAEnabled:  u.MFAEnabled(),
		Permissions: u.Permissions,
	}, true
}

// protect wraps a handler so only authenticated requests reach it.
func (s *Server) protect(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.currentUser(r); !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
			return
		}
		h(w, r)
	})
}

// requireAdmin wraps a handler so only ROLE_ADMIN users reach it (used for
// destructive / instance-wide operations).
func (s *Server) requireAdmin(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := s.currentUser(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
			return
		}
		if u.Role != "ROLE_ADMIN" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin role required"})
			return
		}
		h(w, r)
	})
}

// requirePerm enforces a granular CRUD capability on a resource. ROLE_ADMIN
// always passes; others are checked against their permission matrix.
func (s *Server) requirePerm(resource, action string, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := s.currentUser(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
			return
		}
		if u.Role == "ROLE_ADMIN" || perm.Parse(u.Permissions, u.Role).Can(resource, action) {
			h(w, r)
			return
		}
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "permission denied: " + resource + ":" + action})
	})
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isTLS(r),
		MaxAge: int(sessionTTL / time.Second),
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isTLS(r), MaxAge: -1,
	})
}

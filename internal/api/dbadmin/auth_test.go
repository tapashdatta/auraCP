package dbadmin

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/auracp/auracp/internal/api"
	"github.com/auracp/auracp/internal/auth"
	"github.com/auracp/auracp/internal/secret"
	"github.com/auracp/auracp/internal/store"
	"github.com/auracp/auracp/pkg/dbadmin"
)

// testHarness wires a store + secret box + adapter trio against a fresh
// temp database. Most adapter tests reuse it.
type testHarness struct {
	t      *testing.T
	st     *store.Store
	box    *secret.Box
	conns  *panelConns
	stepUp *stepUpStore
	auth   *panelAuth
}

func newHarness(t *testing.T) *testHarness {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "auracp.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	box, err := secret.Open(dir)
	if err != nil {
		t.Fatalf("secret.Open: %v", err)
	}

	conns := newPanelConns(st, box)
	stepUp := newStepUpStore()
	t.Cleanup(stepUp.stop)

	h := &testHarness{
		t:      t,
		st:     st,
		box:    box,
		conns:  conns,
		stepUp: stepUp,
	}
	h.auth = newPanelAuth(st, conns, h.currentUserFn(), stepUp)
	return h
}

// currentUserFn returns a CurrentUserFunc that resolves panel sessions
// via the harness's store, matching the production resolver shape.
func (h *testHarness) currentUserFn() CurrentUserFunc {
	return func(r *http.Request) (api.IdentitySummary, bool) {
		c, err := r.Cookie(panelSessionCookie)
		if err != nil {
			return api.IdentitySummary{}, false
		}
		userID, pending, ok := h.st.Session(c.Value)
		if !ok || pending {
			return api.IdentitySummary{}, false
		}
		u, err := h.st.UserByID(userID)
		if err != nil {
			return api.IdentitySummary{}, false
		}
		return api.IdentitySummary{
			UserID:      u.ID,
			Email:       u.Email,
			Role:        u.Role,
			MFAEnabled:  u.MFAEnabled(),
			Permissions: u.Permissions,
		}, true
	}
}

// seedUser creates a panel user and a session cookie for them. Returns
// the session token (for cookie injection) and the user id.
func (h *testHarness) seedUser(role, email string) (token string, userID int64) {
	h.t.Helper()
	hash, err := auth.HashPassword("password1234")
	if err != nil {
		h.t.Fatalf("HashPassword: %v", err)
	}
	id, err := h.st.CreateUser(email, hash, role, "", "")
	if err != nil {
		h.t.Fatalf("CreateUser: %v", err)
	}
	tok, err := auth.RandomToken()
	if err != nil {
		h.t.Fatalf("RandomToken: %v", err)
	}
	if err := h.st.CreateSession(tok, id, false, time.Hour); err != nil {
		h.t.Fatalf("CreateSession: %v", err)
	}
	return tok, id
}

// reqWithSession returns a request with the panel session cookie set.
func reqWithSession(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/dbadmin/connections", nil)
	r.AddCookie(&http.Cookie{Name: panelSessionCookie, Value: token})
	return r
}

// TestAdapter_AuthFromPanelSession exercises the happy path: a panel
// session cookie maps to a dbadmin.User with the expected fields.
func TestAdapter_AuthFromPanelSession(t *testing.T) {
	h := newHarness(t)
	tok, _ := h.seedUser("ROLE_ADMIN", "admin@example.com")

	got, err := h.auth.Authenticate(reqWithSession(tok))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got.ID == "" {
		t.Fatal("authenticated user has empty ID")
	}
	if got.Username != "admin@example.com" {
		t.Fatalf("Username = %q, want admin@example.com", got.Username)
	}
	if got.Attrs["role"] != "ROLE_ADMIN" {
		t.Fatalf("Attrs[role] = %q, want ROLE_ADMIN", got.Attrs["role"])
	}
	if got.Attrs["session_id"] != tok {
		t.Fatalf("Attrs[session_id] mismatch — token not propagated")
	}
}

// TestAdapter_AuthFromPanelSession_NoSession verifies a request with no
// cookie returns dbadmin.ErrUnauthenticated.
func TestAdapter_AuthFromPanelSession_NoSession(t *testing.T) {
	h := newHarness(t)
	r := httptest.NewRequest(http.MethodGet, "/api/dbadmin/connections", nil)

	_, err := h.auth.Authenticate(r)
	if err != dbadmin.ErrUnauthenticated {
		t.Fatalf("Authenticate without cookie returned %v, want ErrUnauthenticated", err)
	}
}

// TestAdapter_HasPermission_AdminFullAccess verifies ROLE_ADMIN passes
// everything and gets an implicit RoleOwner role on every connection.
func TestAdapter_HasPermission_AdminFullAccess(t *testing.T) {
	h := newHarness(t)
	tok, _ := h.seedUser("ROLE_ADMIN", "admin@example.com")
	u, err := h.auth.Authenticate(reqWithSession(tok))
	if err != nil {
		t.Fatal(err)
	}
	for _, act := range []dbadmin.Action{
		dbadmin.ActionConnList,
		dbadmin.ActionConnCreate,
		dbadmin.ActionQueryDDL,
		dbadmin.ActionAuditConfig,
	} {
		ok, err := h.auth.HasPermission(u, "any-conn", act)
		if err != nil {
			t.Fatalf("HasPermission(%s): %v", act, err)
		}
		if !ok {
			t.Fatalf("ROLE_ADMIN denied %s", act)
		}
	}
}

// TestAdapter_HasPermission_NonAdminDefaultDeny verifies a non-admin
// user with no aura_db_grants row sees zero connections.
func TestAdapter_HasPermission_NonAdminDefaultDeny(t *testing.T) {
	h := newHarness(t)
	tok, _ := h.seedUser("ROLE_USER", "user@example.com")
	u, err := h.auth.Authenticate(reqWithSession(tok))
	if err != nil {
		t.Fatal(err)
	}
	ok, _ := h.auth.HasPermission(u, "conn-xyz", dbadmin.ActionConnView)
	if ok {
		t.Fatal("non-admin with no grant authorized for ActionConnView")
	}
	// List is always allowed at auth layer (List filters by grant).
	ok, _ = h.auth.HasPermission(u, "", dbadmin.ActionConnList)
	if !ok {
		t.Fatal("ActionConnList should always be allowed; conns.List filters")
	}
}

// TestAdapter_VerifyStepUp_UnavailableWhenNoMFA is the PR #10.5 /
// FIX-SDK-1 regression test: a logged-in user without TOTP enrolled
// must receive ErrStepUpUnavailable, not ErrUnauthenticated, when they
// hit a step-up flow. Without this distinction the SPA dead-ends the
// operator in a relogin loop they cannot escape.
func TestAdapter_VerifyStepUp_UnavailableWhenNoMFA(t *testing.T) {
	h := newHarness(t)
	tok, _ := h.seedUser("ROLE_ADMIN", "noMfa@example.com") // no MFA enrolled

	r := reqWithSession(tok)
	r.Method = "POST"
	r.Body = nil // VerifyStepUp short-circuits on missing MFA before body parsing
	_, _, err := h.auth.VerifyStepUp(r)
	if err != dbadmin.ErrStepUpUnavailable {
		t.Fatalf("VerifyStepUp(no-mfa) = %v, want ErrStepUpUnavailable", err)
	}
}

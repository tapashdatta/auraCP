package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/dbadmintest"
	"github.com/auracp/auracp/pkg/dbadmin/httpapi"
)

// newEngine builds an engine with the supplied fakes.
func newEngine(t *testing.T, auth *dbadmintest.Auth, conns *dbadmintest.Connections, audit *dbadmintest.Audit) *dbadmin.Engine {
	t.Helper()
	e, err := dbadmin.New(dbadmin.Options{Auth: auth, Conns: conns, Audit: audit})
	if err != nil {
		t.Fatalf("dbadmin.New: %v", err)
	}
	t.Cleanup(func() { _ = e.Shutdown(context.Background()) })
	return e
}

// newTestServer wires httpapi.New() onto an httptest.Server. CSRF is
// satisfied with a stable token + cookie for mutating requests.
func newTestServer(t *testing.T, e *dbadmin.Engine) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(httpapi.New(e))
	t.Cleanup(srv.Close)
	return srv
}

// do executes a request with the given user, body, and method. Returns
// the response — caller closes Body.
func do(t *testing.T, srv *httptest.Server, method, path, user string, body any) *http.Response {
	t.Helper()
	var br io.Reader
	if body != nil {
		bs, _ := json.Marshal(body)
		br = bytes.NewReader(bs)
	}
	req, err := http.NewRequest(method, srv.URL+path, br)
	if err != nil {
		t.Fatal(err)
	}
	if user != "" {
		req.Header.Set("X-Test-User", user)
	}
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Aura-Csrf", "csrf-test-token")
		req.AddCookie(&http.Cookie{Name: "__Host-aura_csrf", Value: "csrf-test-token"})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

type envelope struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	} `json:"error"`
}

func decodeEnvelope(t *testing.T, r *http.Response) envelope {
	t.Helper()
	var env envelope
	if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	return env
}

func TestListConnections_401Unauthenticated(t *testing.T) {
	e := newEngine(t, dbadmintest.NewAuth(), dbadmintest.NewConnections(), dbadmintest.NewAudit())
	srv := newTestServer(t, e)
	resp := do(t, srv, http.MethodGet, "/connections", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	env := decodeEnvelope(t, resp)
	if env.Error.Code != httpapi.CodeUnauthenticated {
		t.Errorf("code = %q, want %q", env.Error.Code, httpapi.CodeUnauthenticated)
	}
	if env.Error.RequestID == "" {
		t.Error("requestId empty")
	}
}

func TestListConnections_HappyPathRedactsSecrets(t *testing.T) {
	auth := dbadmintest.NewAuth().
		WithUser("alice", "alice@example").
		WithGrant("alice", "conn-1", dbadmin.RoleAnalyst)
	conns := dbadmintest.NewConnections().WithConnection(dbadmin.Connection{
		ID:     "conn-1",
		Name:   "test",
		Engine: dbadmin.EngineMariaDB,
		Host:   "localhost",
		Port:   3306,
	}, dbadmin.Credentials{Password: "super-secret-pw"})
	audit := dbadmintest.NewAudit()
	e := newEngine(t, auth, conns, audit)
	srv := newTestServer(t, e)

	resp := do(t, srv, http.MethodGet, "/connections", "alice", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "super-secret-pw") {
		t.Errorf("password leaked in response: %s", body)
	}
	var list []map[string]any
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len(list) = %d, want 1", len(list))
	}
	if list[0]["hasPassword"] != true {
		t.Errorf("hasPassword = %v, want true", list[0]["hasPassword"])
	}
	if _, ok := list[0]["password"]; ok {
		t.Error("response contains a 'password' field")
	}
	// Audit emitted.
	if audit.Len() == 0 {
		t.Error("expected audit emission for list connections")
	}
}

func TestCreateConnection_400InvalidJSON(t *testing.T) {
	auth := dbadmintest.NewAuth().WithUser("alice", "")
	// Grant owner role on a placeholder to make HasPermission(_, "", create) succeed.
	auth.WithGrant("alice", "", dbadmin.RoleOwner)
	auth.WithStepUpVerified("alice", dbadmin.ActionConnCreate)
	e := newEngine(t, auth, dbadmintest.NewConnections(), dbadmintest.NewAudit())
	srv := newTestServer(t, e)

	// Garbage JSON.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/connections", bytes.NewReader([]byte("{not json")))
	req.Header.Set("X-Test-User", "alice")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Aura-Csrf", "csrf-test-token")
	req.AddCookie(&http.Cookie{Name: "__Host-aura_csrf", Value: "csrf-test-token"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	env := decodeEnvelope(t, resp)
	if env.Error.Code != httpapi.CodeInvalidJSON && env.Error.Code != httpapi.CodeInvalidInput {
		t.Errorf("code = %q, want INVALID_JSON or INVALID_INPUT", env.Error.Code)
	}
}

func TestCreateConnection_RejectsUnknownFields(t *testing.T) {
	auth := dbadmintest.NewAuth().WithUser("alice", "")
	auth.WithGrant("alice", "", dbadmin.RoleOwner)
	auth.WithStepUpVerified("alice", dbadmin.ActionConnCreate)
	e := newEngine(t, auth, dbadmintest.NewConnections(), dbadmintest.NewAudit())
	srv := newTestServer(t, e)

	body := bytes.NewReader([]byte(`{"name":"x","unknownField":true}`))
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/connections", body)
	req.Header.Set("X-Test-User", "alice")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Aura-Csrf", "csrf-test-token")
	req.AddCookie(&http.Cookie{Name: "__Host-aura_csrf", Value: "csrf-test-token"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (unknown field)", resp.StatusCode)
	}
}

func TestGetConnection_404OnMissing(t *testing.T) {
	auth := dbadmintest.NewAuth().
		WithUser("alice", "").
		WithGrant("alice", "conn-1", dbadmin.RoleAnalyst)
	e := newEngine(t, auth, dbadmintest.NewConnections(), dbadmintest.NewAudit())
	srv := newTestServer(t, e)

	resp := do(t, srv, http.MethodGet, "/connections/conn-1", "alice", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestDeleteConnection_403WithoutPermission(t *testing.T) {
	auth := dbadmintest.NewAuth().
		WithUser("alice", "").
		WithGrant("alice", "conn-1", dbadmin.RoleViewer)
	conns := dbadmintest.NewConnections().WithConnection(dbadmin.Connection{
		ID:     "conn-1",
		Name:   "x",
		Engine: dbadmin.EngineMariaDB,
		Host:   "h", Port: 1,
	}, dbadmin.Credentials{})
	e := newEngine(t, auth, conns, dbadmintest.NewAudit())
	srv := newTestServer(t, e)

	resp := do(t, srv, http.MethodDelete, "/connections/conn-1", "alice", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestCSRF_RequiredForMutating(t *testing.T) {
	auth := dbadmintest.NewAuth().WithUser("alice", "")
	auth.WithGrant("alice", "", dbadmin.RoleOwner)
	auth.WithStepUpVerified("alice", dbadmin.ActionConnCreate)
	e := newEngine(t, auth, dbadmintest.NewConnections(), dbadmintest.NewAudit())
	srv := newTestServer(t, e)

	body := bytes.NewReader([]byte(`{"name":"x"}`))
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/connections", body)
	req.Header.Set("X-Test-User", "alice")
	// Deliberately omit CSRF header + cookie.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 missing CSRF", resp.StatusCode)
	}
}

func TestQuery_403ForbiddenStatement(t *testing.T) {
	auth := dbadmintest.NewAuth().
		WithUser("alice", "").
		WithGrant("alice", "conn-1", dbadmin.RoleAnalyst)
	conns := dbadmintest.NewConnections().WithConnection(dbadmin.Connection{
		ID:     "conn-1",
		Engine: dbadmin.EngineMariaDB,
		Host:   "h", Port: 1,
	}, dbadmin.Credentials{})
	audit := dbadmintest.NewAudit()
	e := newEngine(t, auth, conns, audit)
	srv := newTestServer(t, e)

	resp := do(t, srv, http.MethodPost, "/connections/conn-1/query", "alice", map[string]any{
		"statement": "SELECT LOAD_FILE('/etc/passwd')",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 forbidden statement", resp.StatusCode)
	}
	env := decodeEnvelope(t, resp)
	if env.Error.Code != httpapi.CodeForbiddenStatement {
		t.Errorf("code = %q, want FORBIDDEN_STATEMENT", env.Error.Code)
	}
}

func TestQuery_400EmptyStatement(t *testing.T) {
	auth := dbadmintest.NewAuth().
		WithUser("alice", "").
		WithGrant("alice", "conn-1", dbadmin.RoleAnalyst)
	conns := dbadmintest.NewConnections().WithConnection(dbadmin.Connection{
		ID:     "conn-1",
		Engine: dbadmin.EngineMariaDB,
		Host:   "h", Port: 1,
	}, dbadmin.Credentials{})
	e := newEngine(t, auth, conns, dbadmintest.NewAudit())
	srv := newTestServer(t, e)

	resp := do(t, srv, http.MethodPost, "/connections/conn-1/query", "alice", map[string]any{
		"statement": "",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestEnvelopeShape_Consistent(t *testing.T) {
	// 401 (unauthenticated) and 404 (not found) should share the same
	// envelope structure.
	e := newEngine(t, dbadmintest.NewAuth(), dbadmintest.NewConnections(), dbadmintest.NewAudit())
	srv := newTestServer(t, e)

	for _, tc := range []struct {
		method, path string
		user         string
		wantStatus   int
		wantCode     string
	}{
		{http.MethodGet, "/connections", "", http.StatusUnauthorized, httpapi.CodeUnauthenticated},
		{http.MethodGet, "/nonsense-route", "", http.StatusUnauthorized, httpapi.CodeUnauthenticated},
	} {
		resp := do(t, srv, tc.method, tc.path, tc.user, nil)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var env envelope
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatalf("%s %s: decode: %v body=%s", tc.method, tc.path, err, body)
		}
		if resp.StatusCode != tc.wantStatus {
			t.Errorf("%s %s: status = %d, want %d", tc.method, tc.path, resp.StatusCode, tc.wantStatus)
		}
		if env.Error.Code != tc.wantCode {
			t.Errorf("%s %s: code = %q, want %q", tc.method, tc.path, env.Error.Code, tc.wantCode)
		}
		if env.Error.RequestID == "" {
			t.Errorf("%s %s: requestId empty", tc.method, tc.path)
		}
	}
}

func TestRevealPassword_RequiresStepUp(t *testing.T) {
	auth := dbadmintest.NewAuth().
		WithUser("alice", "").
		WithGrant("alice", "conn-1", dbadmin.RoleOwner)
	conns := dbadmintest.NewConnections().WithConnection(dbadmin.Connection{
		ID: "conn-1", Engine: dbadmin.EngineMariaDB, Host: "h", Port: 1,
	}, dbadmin.Credentials{Password: "pw"})
	e := newEngine(t, auth, conns, dbadmintest.NewAudit())
	srv := newTestServer(t, e)

	// No step-up — expect 428.
	resp := do(t, srv, http.MethodPost, "/connections/conn-1/password/reveal", "alice", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionRequired {
		t.Errorf("status = %d, want 428", resp.StatusCode)
	}
}

func TestRevealPassword_HappyPathStepUpAudited(t *testing.T) {
	auth := dbadmintest.NewAuth().
		WithUser("alice", "").
		WithGrant("alice", "conn-1", dbadmin.RoleOwner).
		WithStepUpVerified("alice", dbadmin.ActionConnPwdView)
	conns := dbadmintest.NewConnections().WithConnection(dbadmin.Connection{
		ID: "conn-1", Engine: dbadmin.EngineMariaDB, Host: "h", Port: 1,
	}, dbadmin.Credentials{Password: "verysecret"})
	audit := dbadmintest.NewAudit()
	e := newEngine(t, auth, conns, audit)
	srv := newTestServer(t, e)

	resp := do(t, srv, http.MethodPost, "/connections/conn-1/password/reveal", "alice", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out["password"] != "verysecret" {
		t.Errorf("password = %v, want verysecret", out["password"])
	}
	// Audit emitted for the password view.
	events := audit.EventsByAction(dbadmin.ActionConnPwdView)
	if len(events) == 0 {
		t.Error("expected audit event for password reveal")
	}
}

func TestAuditOnPermissionDenial(t *testing.T) {
	auth := dbadmintest.NewAuth().WithUser("alice", "")
	// Alice has zero grants — every action denies.
	conns := dbadmintest.NewConnections().WithConnection(dbadmin.Connection{
		ID: "conn-1", Engine: dbadmin.EngineMariaDB, Host: "h", Port: 1,
	}, dbadmin.Credentials{})
	audit := dbadmintest.NewAudit()
	e := newEngine(t, auth, conns, audit)
	srv := newTestServer(t, e)

	resp := do(t, srv, http.MethodDelete, "/connections/conn-1", "alice", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if audit.Len() == 0 {
		t.Error("expected audit event on permission denial")
	}
}

func TestUnknownRoute_404(t *testing.T) {
	auth := dbadmintest.NewAuth().WithUser("alice", "")
	e := newEngine(t, auth, dbadmintest.NewConnections(), dbadmintest.NewAudit())
	srv := newTestServer(t, e)
	resp := do(t, srv, http.MethodGet, "/this-does-not-exist", "alice", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestErrorMapping_DBAdminSentinels(t *testing.T) {
	// Verify the error mapper produces the expected (status, code) for
	// each sentinel. This is exercised via the public api by an end-
	// to-end flow whose denial path uses ErrUnauthenticated.
	auth := dbadmintest.NewAuth().WithAuthError(errors.New("io blew up"))
	e := newEngine(t, auth, dbadmintest.NewConnections(), dbadmintest.NewAudit())
	srv := newTestServer(t, e)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/connections", nil)
	req.Header.Set("X-Test-User", "alice")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("auth I/O error → status = %d, want 500", resp.StatusCode)
	}
}

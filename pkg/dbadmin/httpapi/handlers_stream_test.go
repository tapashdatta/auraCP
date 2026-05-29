package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/dbadmintest"
	"github.com/auracp/auracp/pkg/dbadmin/httpapi"
	"github.com/gorilla/websocket"
)

// TestSQLStream_Forbidden verifies the WS path rejects forbidden
// statements with the canonical close code. The driver layer is never
// reached so this is a pure classifier-gate test.
func TestSQLStream_ForbiddenClassRejected(t *testing.T) {
	auth := dbadmintest.NewAuth().
		WithUser("alice", "").
		WithGrant("alice", "conn-1", dbadmin.RoleAnalyst)
	conns := dbadmintest.NewConnections().WithConnection(dbadmin.Connection{
		ID: "conn-1", Engine: dbadmin.EngineMariaDB, Host: "h", Port: 1,
	}, dbadmin.Credentials{})
	e, err := dbadmin.New(dbadmin.Options{Auth: auth, Conns: conns, Audit: dbadmintest.NewAudit()})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown(context.Background())

	srv := httptest.NewServer(httpapi.New(e))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/sql/stream"
	hdr := http.Header{}
	hdr.Set("X-Test-User", "alice")
	hdr.Set("Origin", srv.URL)
	hdr.Set("Cookie", "__Host-aura_csrf=t1")

	dialer := *websocket.DefaultDialer
	dialer.Subprotocols = []string{"aura.sql.v1", "aura.csrf.t1"}
	u, _ := url.Parse(wsURL)
	c, _, err := dialer.Dial(u.String(), hdr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	if err := c.WriteJSON(map[string]any{
		"type":         "open",
		"connectionId": "conn-1",
		"statement":    "SELECT LOAD_FILE('/etc/passwd')",
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Expect an error frame then close.
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	var msg map[string]any
	if err := c.ReadJSON(&msg); err != nil {
		t.Fatalf("read: %v", err)
	}
	if msg["type"] != "error" {
		t.Errorf("frame type = %v, want error", msg["type"])
	}
}

func TestSQLStream_RequiresAuth(t *testing.T) {
	e, err := dbadmin.New(dbadmin.Options{
		Auth: dbadmintest.NewAuth(), Conns: dbadmintest.NewConnections(), Audit: dbadmintest.NewAudit(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown(context.Background())
	srv := httptest.NewServer(httpapi.New(e))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/sql/stream"
	hdr := http.Header{}
	hdr.Set("Origin", srv.URL)
	dialer := *websocket.DefaultDialer
	dialer.Subprotocols = []string{"aura.sql.v1"}
	// No CSRF cookie/token — but this test asserts UNAUTH which fires
	// before the CSRF gate, so no setup needed.
	u, _ := url.Parse(wsURL)
	_, resp, err := dialer.Dial(u.String(), hdr)
	if err == nil {
		t.Fatal("expected dial to fail without auth")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		gotStatus := 0
		if resp != nil {
			gotStatus = resp.StatusCode
		}
		t.Errorf("unauth dial status = %d, want 401", gotStatus)
	}
}

func TestSQLStream_InvalidOpenFrame(t *testing.T) {
	auth := dbadmintest.NewAuth().
		WithUser("alice", "").
		WithGrant("alice", "conn-1", dbadmin.RoleAnalyst)
	conns := dbadmintest.NewConnections().WithConnection(dbadmin.Connection{
		ID: "conn-1", Engine: dbadmin.EngineMariaDB, Host: "h", Port: 1,
	}, dbadmin.Credentials{})
	e, err := dbadmin.New(dbadmin.Options{Auth: auth, Conns: conns, Audit: dbadmintest.NewAudit()})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown(context.Background())
	srv := httptest.NewServer(httpapi.New(e))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/sql/stream"
	hdr := http.Header{}
	hdr.Set("X-Test-User", "alice")
	hdr.Set("Origin", srv.URL)
	hdr.Set("Cookie", "__Host-aura_csrf=t1")
	dialer := *websocket.DefaultDialer
	dialer.Subprotocols = []string{"aura.sql.v1", "aura.csrf.t1"}
	u, _ := url.Parse(wsURL)
	c, _, err := dialer.Dial(u.String(), hdr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	// First frame is wrong type.
	if err := c.WriteJSON(map[string]any{"type": "cancel"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	var msg map[string]any
	if err := c.ReadJSON(&msg); err != nil {
		t.Fatalf("read: %v", err)
	}
	if msg["type"] != "error" {
		bs, _ := json.Marshal(msg)
		t.Errorf("frame = %s, want error frame", bs)
	}
}

// TestSQLStream_RejectsCrossOrigin asserts the WS upgrader refuses
// handshakes whose Origin header doesn't match the request Host
// (CSWSH defense). MUST-1 from PR #8 adversarial review.
func TestSQLStream_RejectsCrossOrigin(t *testing.T) {
	auth := dbadmintest.NewAuth().
		WithUser("alice", "").
		WithGrant("alice", "conn-1", dbadmin.RoleAnalyst)
	conns := dbadmintest.NewConnections().WithConnection(dbadmin.Connection{
		ID: "conn-1", Engine: dbadmin.EngineMariaDB, Host: "h", Port: 1,
	}, dbadmin.Credentials{})
	audit := dbadmintest.NewAudit()
	e, err := dbadmin.New(dbadmin.Options{Auth: auth, Conns: conns, Audit: audit})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown(context.Background())
	srv := httptest.NewServer(httpapi.New(e))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/sql/stream"
	hdr := http.Header{}
	hdr.Set("X-Test-User", "alice")
	hdr.Set("Origin", "https://evil.example.com")
	hdr.Set("Cookie", "__Host-aura_csrf=t1")
	dialer := *websocket.DefaultDialer
	dialer.Subprotocols = []string{"aura.sql.v1", "aura.csrf.t1"}
	u, _ := url.Parse(wsURL)
	_, resp, err := dialer.Dial(u.String(), hdr)
	if err == nil {
		t.Fatal("expected cross-origin dial to fail")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		gotStatus := 0
		if resp != nil {
			gotStatus = resp.StatusCode
		}
		t.Errorf("status = %d, want 403", gotStatus)
	}
}

// TestSQLStream_RejectsMissingCSRF asserts the WS upgrade refuses
// handshakes that don't present a CSRF token. MUST-1 from PR #8
// adversarial review.
func TestSQLStream_RejectsMissingCSRF(t *testing.T) {
	auth := dbadmintest.NewAuth().
		WithUser("alice", "").
		WithGrant("alice", "conn-1", dbadmin.RoleAnalyst)
	conns := dbadmintest.NewConnections().WithConnection(dbadmin.Connection{
		ID: "conn-1", Engine: dbadmin.EngineMariaDB, Host: "h", Port: 1,
	}, dbadmin.Credentials{})
	e, err := dbadmin.New(dbadmin.Options{Auth: auth, Conns: conns, Audit: dbadmintest.NewAudit()})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown(context.Background())
	srv := httptest.NewServer(httpapi.New(e))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/sql/stream"
	hdr := http.Header{}
	hdr.Set("X-Test-User", "alice")
	hdr.Set("Origin", srv.URL)
	// No Cookie, no subprotocol CSRF entry.
	dialer := *websocket.DefaultDialer
	dialer.Subprotocols = []string{"aura.sql.v1"}
	u, _ := url.Parse(wsURL)
	_, resp, err := dialer.Dial(u.String(), hdr)
	if err == nil {
		t.Fatal("expected dial without CSRF to fail")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		gotStatus := 0
		if resp != nil {
			gotStatus = resp.StatusCode
		}
		t.Errorf("status = %d, want 403", gotStatus)
	}
}

// TestAuditEmittedOnAuthnDenial asserts the authn middleware emits an
// audit event before short-circuiting on 401 — making permission probes
// forensically visible. MUST-2 from PR #8 adversarial review.
func TestAuditEmittedOnAuthnDenial(t *testing.T) {
	auth := dbadmintest.NewAuth() // no users — every Authenticate returns ErrUnauthenticated
	conns := dbadmintest.NewConnections()
	audit := dbadmintest.NewAudit()
	e, err := dbadmin.New(dbadmin.Options{Auth: auth, Conns: conns, Audit: audit})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown(context.Background())
	srv := httptest.NewServer(httpapi.New(e))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/connections")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	events := audit.Events()
	if len(events) == 0 {
		t.Fatal("no audit events recorded; authn denial must emit an event")
	}
	if events[0].Action != "auth.denied" {
		t.Errorf("first event Action = %q, want auth.denied", events[0].Action)
	}
}

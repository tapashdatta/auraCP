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

// TestSlowLogStream_RequiresAuth verifies an anonymous dial on
// /connections/{id}/slow-log/stream is rejected with 401. Mirrors
// TestSQLStream_RequiresAuth's setup so the same regression net catches
// authn-middleware misconfiguration for the new route.
func TestSlowLogStream_RequiresAuth(t *testing.T) {
	e, err := dbadmin.New(dbadmin.Options{
		Auth:  dbadmintest.NewAuth(),
		Conns: dbadmintest.NewConnections(),
		Audit: dbadmintest.NewAudit(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Shutdown(context.Background())
	srv := httptest.NewServer(httpapi.New(e))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/connections/conn-1/slow-log/stream"
	hdr := http.Header{}
	hdr.Set("Origin", srv.URL)
	dialer := *websocket.DefaultDialer
	dialer.Subprotocols = []string{"aura.slowlog.v1"}
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

// TestSlowLogStream_RejectsCrossOrigin asserts the WS upgrader refuses
// handshakes whose Origin header doesn't match the request Host (CSWSH).
func TestSlowLogStream_RejectsCrossOrigin(t *testing.T) {
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

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/connections/conn-1/slow-log/stream"
	hdr := http.Header{}
	hdr.Set("X-Test-User", "alice")
	hdr.Set("Origin", "https://evil.example.com")
	hdr.Set("Cookie", "__Host-aura_csrf=t1")
	dialer := *websocket.DefaultDialer
	dialer.Subprotocols = []string{"aura.slowlog.v1", "aura.csrf.t1"}
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

// TestSlowLogStream_RejectsMissingCSRF asserts the WS upgrade refuses
// handshakes that don't present a CSRF token.
func TestSlowLogStream_RejectsMissingCSRF(t *testing.T) {
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

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/connections/conn-1/slow-log/stream"
	hdr := http.Header{}
	hdr.Set("X-Test-User", "alice")
	hdr.Set("Origin", srv.URL)
	// Intentionally no Cookie / Subprotocol with CSRF.
	dialer := *websocket.DefaultDialer
	dialer.Subprotocols = []string{"aura.slowlog.v1"}
	u, _ := url.Parse(wsURL)
	_, resp, err := dialer.Dial(u.String(), hdr)
	if err == nil {
		t.Fatal("expected missing-CSRF dial to fail")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		gotStatus := 0
		if resp != nil {
			gotStatus = resp.StatusCode
		}
		t.Errorf("missing-CSRF status = %d, want 403", gotStatus)
	}
}

// TestSlowLogStream_AuthorizeDenied verifies a user without
// ActionSlowLogRead (RoleNone on the connection) is rejected after
// the open frame. The driver layer is never reached so this is a pure
// RBAC test.
func TestSlowLogStream_AuthorizeDenied(t *testing.T) {
	// alice has NO grant on conn-1 (default = RoleNone).
	auth := dbadmintest.NewAuth().WithUser("alice", "")
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

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/connections/conn-1/slow-log/stream"
	hdr := http.Header{}
	hdr.Set("X-Test-User", "alice")
	hdr.Set("Origin", srv.URL)
	hdr.Set("Cookie", "__Host-aura_csrf=t1")
	dialer := *websocket.DefaultDialer
	dialer.Subprotocols = []string{"aura.slowlog.v1", "aura.csrf.t1"}
	u, _ := url.Parse(wsURL)
	c, _, err := dialer.Dial(u.String(), hdr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	if err := c.WriteJSON(map[string]any{
		"type":         "open",
		"connectionId": "conn-1",
		"csrf":         "t1",
	}); err != nil {
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
	if msg["code"] != "forbidden" {
		t.Errorf("code = %v, want forbidden", msg["code"])
	}
}

// TestSlowLogStream_InvalidOpenFrame asserts that a cancel-as-first-
// frame is rejected with an error + close, matching the SQL stream's
// open-frame discipline.
func TestSlowLogStream_InvalidOpenFrame(t *testing.T) {
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

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/connections/conn-1/slow-log/stream"
	hdr := http.Header{}
	hdr.Set("X-Test-User", "alice")
	hdr.Set("Origin", srv.URL)
	hdr.Set("Cookie", "__Host-aura_csrf=t1")
	dialer := *websocket.DefaultDialer
	dialer.Subprotocols = []string{"aura.slowlog.v1", "aura.csrf.t1"}
	u, _ := url.Parse(wsURL)
	c, _, err := dialer.Dial(u.String(), hdr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	// Send a cancel as the first frame.
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

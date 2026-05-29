package dbadmin_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/dbadmintest"
)

func TestNew_RequiresAllInterfaces(t *testing.T) {
	cases := []struct {
		name string
		opt  dbadmin.Options
		want string
	}{
		{
			name: "missing auth",
			opt: dbadmin.Options{
				Conns: dbadmintest.NewConnections(),
				Audit: dbadmintest.NewAudit(),
			},
			want: "Auth is required",
		},
		{
			name: "missing conns",
			opt: dbadmin.Options{
				Auth:  dbadmintest.NewAuth(),
				Audit: dbadmintest.NewAudit(),
			},
			want: "Conns is required",
		},
		{
			name: "missing audit",
			opt: dbadmin.Options{
				Auth:  dbadmintest.NewAuth(),
				Conns: dbadmintest.NewConnections(),
			},
			want: "Audit is required",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := dbadmin.New(c.opt)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("expected error containing %q, got %q", c.want, err.Error())
			}
		})
	}
}

func TestNew_ValidatesConfig(t *testing.T) {
	bad := dbadmin.Config{}
	bad.Session.IdleTTL = 1 * time.Hour
	bad.Session.AbsoluteTTL = 30 * time.Minute // less than IdleTTL → invalid

	_, err := dbadmin.New(dbadmin.Options{
		Auth:   dbadmintest.NewAuth(),
		Conns:  dbadmintest.NewConnections(),
		Audit:  dbadmintest.NewAudit(),
		Config: bad,
	})
	if err == nil {
		t.Fatal("expected config validation error, got nil")
	}
	if !strings.Contains(err.Error(), "IdleTTL") {
		t.Fatalf("expected IdleTTL validation error, got %q", err.Error())
	}
}

func TestNew_AcceptsDefaultConfig(t *testing.T) {
	engine, err := dbadmin.New(dbadmin.Options{
		Auth:  dbadmintest.NewAuth(),
		Conns: dbadmintest.NewConnections(),
		Audit: dbadmintest.NewAudit(),
	})
	if err != nil {
		t.Fatalf("New failed with default config: %v", err)
	}
	cfg := engine.Config()
	if cfg.Session.IdleTTL != 15*time.Minute {
		t.Errorf("default IdleTTL = %v, want 15m", cfg.Session.IdleTTL)
	}
	if cfg.Query.TimeoutDefault != 30*time.Second {
		t.Errorf("default Query.TimeoutDefault = %v, want 30s", cfg.Query.TimeoutDefault)
	}
}

func TestHandler_Returns401_WhenUnauthenticated(t *testing.T) {
	engine, err := dbadmin.New(dbadmin.Options{
		Auth:  dbadmintest.NewAuth(),
		Conns: dbadmintest.NewConnections(),
		Audit: dbadmintest.NewAudit(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Shutdown(context.Background())

	srv := httptest.NewServer(engine.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/connections")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}

	var body struct {
		Error dbadmin.Error `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != dbadmin.CodeUnauthenticated {
		t.Errorf("error.code = %q, want %q", body.Error.Code, dbadmin.CodeUnauthenticated)
	}
}

func TestHandler_Returns500_OnAuthIOError(t *testing.T) {
	auth := dbadmintest.NewAuth().WithAuthError(errors.New("session store down"))
	engine, err := dbadmin.New(dbadmin.Options{
		Auth:  auth,
		Conns: dbadmintest.NewConnections(),
		Audit: dbadmintest.NewAudit(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Shutdown(context.Background())

	srv := httptest.NewServer(engine.Handler())
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/connections", nil)
	req.Header.Set("X-Test-User", "alice")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
}

func TestHandler_Returns501_OnAuthenticatedRoute(t *testing.T) {
	// PR #1: the handler doesn't yet implement any real routes. An
	// authenticated request should reach the "not implemented" branch
	// rather than 401. This test will be deleted (or repurposed) when
	// PR #8 wires the real routes.
	auth := dbadmintest.NewAuth().WithUser("alice", "alice@example")
	engine, err := dbadmin.New(dbadmin.Options{
		Auth:  auth,
		Conns: dbadmintest.NewConnections(),
		Audit: dbadmintest.NewAudit(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Shutdown(context.Background())

	srv := httptest.NewServer(engine.Handler())
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/connections", nil)
	req.Header.Set("X-Test-User", "alice")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", resp.StatusCode)
	}
}

func TestHandler_Returns503_AfterShutdown(t *testing.T) {
	engine, err := dbadmin.New(dbadmin.Options{
		Auth:  dbadmintest.NewAuth(),
		Conns: dbadmintest.NewConnections(),
		Audit: dbadmintest.NewAudit(),
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(engine.Handler())
	defer srv.Close()

	if err := engine.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/connections")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}

	// Shutdown is idempotent.
	if err := engine.Shutdown(context.Background()); err != nil {
		t.Errorf("second Shutdown returned %v, want nil", err)
	}
}

func TestRole_StringRoundTrip(t *testing.T) {
	cases := []struct {
		role dbadmin.Role
		want string
	}{
		{dbadmin.RoleNone, "none"},
		{dbadmin.RoleViewer, "viewer"},
		{dbadmin.RoleAnalyst, "analyst"},
		{dbadmin.RoleWriter, "writer"},
		{dbadmin.RoleDBA, "dba"},
		{dbadmin.RoleOwner, "owner"},
	}
	for _, c := range cases {
		if got := c.role.String(); got != c.want {
			t.Errorf("Role(%d).String() = %q, want %q", c.role, got, c.want)
		}
	}
}

func TestAction_MinRole(t *testing.T) {
	cases := []struct {
		action dbadmin.Action
		want   dbadmin.Role
	}{
		{dbadmin.ActionConnList, dbadmin.RoleViewer},
		{dbadmin.ActionConnView, dbadmin.RoleViewer},
		{dbadmin.ActionSchemaRead, dbadmin.RoleViewer},
		{dbadmin.ActionRowRead, dbadmin.RoleAnalyst},
		{dbadmin.ActionQueryRead, dbadmin.RoleAnalyst},
		{dbadmin.ActionExport, dbadmin.RoleAnalyst},
		{dbadmin.ActionRowWrite, dbadmin.RoleWriter},
		{dbadmin.ActionQueryWrite, dbadmin.RoleWriter},
		{dbadmin.ActionImport, dbadmin.RoleWriter},
		{dbadmin.ActionQueryDDL, dbadmin.RoleDBA},
		{dbadmin.ActionRestore, dbadmin.RoleDBA},
		{dbadmin.ActionConnCreate, dbadmin.RoleOwner},
		{dbadmin.ActionConnDelete, dbadmin.RoleOwner},
		{dbadmin.ActionConnPwdView, dbadmin.RoleOwner},
		{dbadmin.ActionConnGrantMgmt, dbadmin.RoleOwner},
		{dbadmin.ActionQueryDangerous, dbadmin.RoleOwner},
		{dbadmin.ActionAuditConfig, dbadmin.RoleOwner},
		{dbadmin.Action("bogus"), dbadmin.RoleNone},
	}
	for _, c := range cases {
		if got := c.action.MinRole(); got != c.want {
			t.Errorf("Action(%q).MinRole() = %v, want %v", c.action, got, c.want)
		}
	}
}

func TestAction_RequiresStepUp(t *testing.T) {
	wantStepUp := []dbadmin.Action{
		dbadmin.ActionConnPwdView,
		dbadmin.ActionConnUpdate,
		dbadmin.ActionConnDelete,
		dbadmin.ActionConnGrantMgmt,
		dbadmin.ActionQueryDDL,
		dbadmin.ActionQueryDangerous,
		dbadmin.ActionRestore,
		dbadmin.ActionAuditConfig,
	}
	for _, a := range wantStepUp {
		if !a.RequiresStepUp() {
			t.Errorf("Action(%q).RequiresStepUp() = false, want true", a)
		}
	}
	wantNoStepUp := []dbadmin.Action{
		dbadmin.ActionConnList,
		dbadmin.ActionConnView,
		dbadmin.ActionSchemaRead,
		dbadmin.ActionRowRead,
		dbadmin.ActionQueryRead,
		dbadmin.ActionExport,
	}
	for _, a := range wantNoStepUp {
		if a.RequiresStepUp() {
			t.Errorf("Action(%q).RequiresStepUp() = true, want false", a)
		}
	}
}

func TestConnection_TagHelpers(t *testing.T) {
	c := dbadmin.Connection{
		Tags: []dbadmin.Tag{dbadmin.TagProd, dbadmin.TagReadOnly},
	}
	if !c.HasTag(dbadmin.TagProd) {
		t.Error("HasTag(prod) = false, want true")
	}
	if !c.IsProd() {
		t.Error("IsProd() = false, want true")
	}
	if !c.IsReadOnly() {
		t.Error("IsReadOnly() = false, want true")
	}
	if c.HasTag(dbadmin.TagDev) {
		t.Error("HasTag(dev) = true, want false")
	}
}

func TestCredentials_Zero(t *testing.T) {
	c := dbadmin.Credentials{
		Password:   "hunter2",
		ClientCert: []byte("PEM..."),
		ClientKey:  []byte("KEY..."),
	}
	cert := c.ClientCert // capture the backing slice
	key := c.ClientKey
	c.Zero()
	if c.Password != "" {
		t.Error("Password not cleared")
	}
	if c.ClientCert != nil {
		t.Error("ClientCert not nil after Zero")
	}
	if c.ClientKey != nil {
		t.Error("ClientKey not nil after Zero")
	}
	for _, b := range cert {
		if b != 0 {
			t.Errorf("ClientCert backing bytes not zeroed: %v", cert)
			break
		}
	}
	for _, b := range key {
		if b != 0 {
			t.Errorf("ClientKey backing bytes not zeroed: %v", key)
			break
		}
	}
}

func TestTarget_String(t *testing.T) {
	cases := []struct {
		t    dbadmin.Target
		want string
	}{
		{dbadmin.Target{}, ""},
		{dbadmin.Target{ConnectionID: "c"}, "c"},
		{dbadmin.Target{ConnectionID: "c", Schema: "s"}, "c/s"},
		{dbadmin.Target{ConnectionID: "c", Schema: "s", Object: "users"}, "c/s/users"},
		{dbadmin.Target{ConnectionID: "c", Object: "users"}, "c"}, // no schema → object dropped
	}
	for _, c := range cases {
		if got := c.t.String(); got != c.want {
			t.Errorf("Target(%+v).String() = %q, want %q", c.t, got, c.want)
		}
	}
}

func TestDbadmintest_AuthFlow(t *testing.T) {
	auth := dbadmintest.NewAuth().
		WithUser("alice", "alice@example").
		WithGrant("alice", "conn-1", dbadmin.RoleDBA)

	req := httptest.NewRequest("GET", "/whatever", nil)
	req.Header.Set("X-Test-User", "alice")
	u, err := auth.Authenticate(req)
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}
	if u.ID != "alice" {
		t.Errorf("u.ID = %q, want alice", u.ID)
	}
	if u.Roles["conn-1"] != dbadmin.RoleDBA {
		t.Errorf("u.Roles[conn-1] = %v, want RoleDBA", u.Roles["conn-1"])
	}

	ok, err := auth.HasPermission(u, "conn-1", dbadmin.ActionQueryDDL)
	if err != nil || !ok {
		t.Errorf("HasPermission(DDL) = (%v, %v), want (true, nil)", ok, err)
	}
	ok, _ = auth.HasPermission(u, "conn-1", dbadmin.ActionConnDelete)
	if ok {
		t.Error("DBA should not have ConnDelete permission")
	}

	// Step-up: DDL requires it, and we haven't done it yet.
	if !auth.StepUpRequired(dbadmin.ActionQueryDDL) {
		t.Error("StepUpRequired(DDL) = false, want true")
	}
	if auth.HasSteppedUp(u, dbadmin.ActionQueryDDL) {
		t.Error("HasSteppedUp(DDL) = true before VerifyStepUp")
	}

	// Now arm step-up.
	sreq := httptest.NewRequest("POST", "/step-up", nil)
	sreq.Header.Set("X-Test-User", "alice")
	sreq.Header.Set("X-Test-StepUp-Action", string(dbadmin.ActionQueryDDL))
	gotAction, ttl, err := auth.VerifyStepUp(sreq)
	if err != nil {
		t.Fatalf("VerifyStepUp failed: %v", err)
	}
	if gotAction != dbadmin.ActionQueryDDL {
		t.Errorf("VerifyStepUp action = %v, want DDL", gotAction)
	}
	if ttl <= 0 {
		t.Errorf("VerifyStepUp ttl = %v, want > 0", ttl)
	}
	if !auth.HasSteppedUp(u, dbadmin.ActionQueryDDL) {
		t.Error("HasSteppedUp(DDL) = false after VerifyStepUp")
	}
}

func TestDbadmintest_ConnectionStoreRoundTrip(t *testing.T) {
	store := dbadmintest.NewConnections().
		WithConnection(dbadmin.Connection{
			Name:   "test",
			Engine: dbadmin.EngineMariaDB,
			Host:   "localhost",
			Port:   3306,
		}, dbadmin.Credentials{Password: "secret"})

	ids := store.IDs()
	if len(ids) != 1 {
		t.Fatalf("len(IDs) = %d, want 1", len(ids))
	}
	id := ids[0]

	ctx := context.Background()
	conn, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if conn.Engine != dbadmin.EngineMariaDB {
		t.Errorf("Engine = %v, want MariaDB", conn.Engine)
	}
	if conn.Origin != dbadmin.OriginManual {
		t.Errorf("Origin = %v, want OriginManual", conn.Origin)
	}

	creds, err := store.Credentials(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if creds.Password != "secret" {
		t.Errorf("Password = %q, want secret", creds.Password)
	}

	// Zero on the returned copy must not affect a fresh fetch.
	creds.Zero()
	creds2, _ := store.Credentials(ctx, id)
	if creds2.Password != "secret" {
		t.Errorf("after Zero on copy, fresh Password = %q, want secret", creds2.Password)
	}

	// Delete.
	if err := store.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, id); !errors.Is(err, dbadmin.ErrNotFound) {
		t.Errorf("Get after Delete = %v, want ErrNotFound", err)
	}
}

func TestDbadmintest_AuditCapture(t *testing.T) {
	audit := dbadmintest.NewAudit()
	if audit.Len() != 0 {
		t.Errorf("fresh Audit Len = %d, want 0", audit.Len())
	}
	audit.Record(context.Background(), dbadmin.Event{
		EventID: "01H1",
		Action:  dbadmin.ActionConnView,
	})
	audit.Record(context.Background(), dbadmin.Event{
		EventID: "01H2",
		Action:  dbadmin.ActionQueryRead,
	})
	if audit.Len() != 2 {
		t.Errorf("Len after 2 Records = %d, want 2", audit.Len())
	}
	if got := audit.EventsByAction(dbadmin.ActionConnView); len(got) != 1 {
		t.Errorf("EventsByAction(ConnView) returned %d, want 1", len(got))
	}
	audit.Reset()
	if audit.Len() != 0 {
		t.Errorf("Len after Reset = %d, want 0", audit.Len())
	}
}

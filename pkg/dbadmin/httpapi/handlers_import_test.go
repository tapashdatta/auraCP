package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/dbadmintest"
	"github.com/auracp/auracp/pkg/dbadmin/httpapi"
)

// Test plan (v0.3.2-E):
//   - 401: no user.
//   - 403: user lacks RoleWriter (viewer / analyst-without-write).
//   - CSRF rejection.
//   - Invalid format → 400 invalid-input.
//   - Invalid onConflict → 400 invalid-input.
//   - Missing required field → 400.
//   - Missing file part → 400.
//   - SQL format rejected (security boundary).
//
// We don't exercise rows.Insert here — that requires a live driver.Conn.
// The tableimport package's own tests cover decoder correctness; these
// tests cover the handler-side gating + wiring.

func importBody(t *testing.T, fields map[string]string, fileName, fileBody string) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if fileName != "" {
		fw, err := mw.CreateFormFile("file", fileName)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(fileBody)); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, mw.FormDataContentType()
}

func doImport(t *testing.T, srv *httptest.Server, user, csrf string, body io.Reader, ct string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/connections/conn-1/import", body)
	if err != nil {
		t.Fatal(err)
	}
	if user != "" {
		req.Header.Set("X-Test-User", user)
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if csrf != "" {
		req.Header.Set("X-Aura-Csrf", csrf)
		req.AddCookie(&http.Cookie{Name: "__Host-aura_csrf", Value: csrf})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func newImportTestServer(t *testing.T) (*httptest.Server, *dbadmintest.Audit) {
	t.Helper()
	auth := dbadmintest.NewAuth().
		WithUser("alice", "").
		WithGrant("alice", "conn-1", dbadmin.RoleWriter).
		WithUser("viewer", "").
		WithGrant("viewer", "conn-1", dbadmin.RoleViewer)
	conns := dbadmintest.NewConnections().WithConnection(dbadmin.Connection{
		ID: "conn-1", Engine: dbadmin.EngineMariaDB, Host: "127.0.0.1", Port: 1,
		Database: "myapp", Name: "test-conn",
	}, dbadmin.Credentials{})
	audit := dbadmintest.NewAudit()
	e, err := dbadmin.New(dbadmin.Options{Auth: auth, Conns: conns, Audit: audit})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Shutdown(context.Background()) })
	srv := httptest.NewServer(httpapi.New(e))
	t.Cleanup(srv.Close)
	return srv, audit
}

func TestImport_Unauthenticated(t *testing.T) {
	srv, _ := newImportTestServer(t)
	body, ct := importBody(t, map[string]string{
		"schema": "public", "table": "users", "format": "csv",
	}, "x.csv", "id\n1\n")
	resp := doImport(t, srv, "", "csrf-tok", body, ct)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestImport_ForbiddenForViewer(t *testing.T) {
	srv, _ := newImportTestServer(t)
	body, ct := importBody(t, map[string]string{
		"schema": "public", "table": "users", "format": "csv",
	}, "x.csv", "id\n1\n")
	resp := doImport(t, srv, "viewer", "csrf-tok", body, ct)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestImport_CSRFRejection(t *testing.T) {
	srv, _ := newImportTestServer(t)
	body, ct := importBody(t, map[string]string{
		"schema": "public", "table": "users", "format": "csv",
	}, "x.csv", "id\n1\n")
	resp := doImport(t, srv, "alice", "", body, ct)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (csrf)", resp.StatusCode)
	}
	var env envelope
	_ = json.NewDecoder(resp.Body).Decode(&env)
	if env.Error.Code != httpapi.CodeCSRFRejected {
		t.Errorf("code = %q, want %q", env.Error.Code, httpapi.CodeCSRFRejected)
	}
}

func TestImport_InvalidFormat(t *testing.T) {
	srv, _ := newImportTestServer(t)
	body, ct := importBody(t, map[string]string{
		"schema": "public", "table": "users", "format": "xml",
	}, "x.csv", "id\n1\n")
	resp := doImport(t, srv, "alice", "csrf-tok", body, ct)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestImport_SQLFormatRejected(t *testing.T) {
	// Security boundary: the import endpoint MUST NOT accept format=sql
	// because the SQL editor is the only path with the classifier +
	// per-statement ActionQueryWrite gating.
	srv, _ := newImportTestServer(t)
	body, ct := importBody(t, map[string]string{
		"schema": "public", "table": "users", "format": "sql",
	}, "x.sql", "INSERT INTO users VALUES (1);")
	resp := doImport(t, srv, "alice", "csrf-tok", body, ct)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for sql format", resp.StatusCode)
	}
}

func TestImport_InvalidOnConflict(t *testing.T) {
	srv, _ := newImportTestServer(t)
	body, ct := importBody(t, map[string]string{
		"schema": "public", "table": "users", "format": "csv", "onConflict": "ignore",
	}, "x.csv", "id\n1\n")
	resp := doImport(t, srv, "alice", "csrf-tok", body, ct)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestImport_MissingFields(t *testing.T) {
	srv, _ := newImportTestServer(t)
	body, ct := importBody(t, map[string]string{
		"format": "csv",
	}, "x.csv", "id\n1\n")
	resp := doImport(t, srv, "alice", "csrf-tok", body, ct)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestImport_MissingFilePart(t *testing.T) {
	srv, _ := newImportTestServer(t)
	body, ct := importBody(t, map[string]string{
		"schema": "public", "table": "users", "format": "csv",
	}, "", "")
	resp := doImport(t, srv, "alice", "csrf-tok", body, ct)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (missing file)", resp.StatusCode)
	}
}

func TestImport_InvalidIdentifier(t *testing.T) {
	srv, _ := newImportTestServer(t)
	body, ct := importBody(t, map[string]string{
		"schema": "public", "table": "users; DROP TABLE users", "format": "csv",
	}, "x.csv", "id\n1\n")
	resp := doImport(t, srv, "alice", "csrf-tok", body, ct)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (bad identifier)", resp.StatusCode)
	}
}

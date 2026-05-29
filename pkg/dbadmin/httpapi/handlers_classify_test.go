package httpapi_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/dbadmintest"
)

type classifyOut struct {
	Class      string                   `json:"class"`
	Statements []map[string]interface{} `json:"statements"`
	Forbidden  []map[string]interface{} `json:"forbidden"`
}

func decodeClassify(t *testing.T, r *http.Response) classifyOut {
	t.Helper()
	b, _ := io.ReadAll(r.Body)
	var out classifyOut
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode classify: %v body=%s", err, string(b))
	}
	return out
}

// TestClassify_ReadSelect verifies the standalone /sql/classify endpoint
// classifies a SELECT as ClassRead.
func TestClassify_ReadSelect(t *testing.T) {
	auth := dbadmintest.NewAuth().WithUser("alice", "")
	auth.WithGrant("alice", "", dbadmin.RoleOwner)
	e := newEngine(t, auth, dbadmintest.NewConnections(), dbadmintest.NewAudit())
	srv := newTestServer(t, e)

	resp := do(t, srv, http.MethodPost, "/sql/classify", "alice", map[string]any{
		"engine":    "mariadb",
		"statement": "SELECT 1",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	out := decodeClassify(t, resp)
	if out.Class != "read" {
		t.Errorf("class = %q, want read", out.Class)
	}
	if len(out.Statements) != 1 {
		t.Fatalf("len(statements) = %d, want 1", len(out.Statements))
	}
	if out.Statements[0]["kind"] != "SELECT" {
		t.Errorf("kind = %v, want SELECT", out.Statements[0]["kind"])
	}
}

// TestClassify_DDL verifies a DROP returns ClassDDL.
func TestClassify_DDL(t *testing.T) {
	auth := dbadmintest.NewAuth().WithUser("alice", "")
	auth.WithGrant("alice", "", dbadmin.RoleOwner)
	e := newEngine(t, auth, dbadmintest.NewConnections(), dbadmintest.NewAudit())
	srv := newTestServer(t, e)

	resp := do(t, srv, http.MethodPost, "/sql/classify", "alice", map[string]any{
		"engine":    "postgres",
		"statement": "DROP TABLE foo",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	out := decodeClassify(t, resp)
	if out.Class != "ddl" {
		t.Errorf("class = %q, want ddl", out.Class)
	}
}

// TestClassify_Forbidden verifies LOAD_FILE returns ClassForbidden with
// non-empty Forbidden matches. The endpoint MUST NOT 422 — it returns 200
// because /sql/classify is a UX endpoint, not a security gate.
func TestClassify_Forbidden(t *testing.T) {
	auth := dbadmintest.NewAuth().WithUser("alice", "")
	auth.WithGrant("alice", "", dbadmin.RoleOwner)
	e := newEngine(t, auth, dbadmintest.NewConnections(), dbadmintest.NewAudit())
	srv := newTestServer(t, e)

	resp := do(t, srv, http.MethodPost, "/sql/classify", "alice", map[string]any{
		"engine":    "mariadb",
		"statement": "SELECT LOAD_FILE('/etc/passwd')",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (classify is UX, not a gate)", resp.StatusCode)
	}
	out := decodeClassify(t, resp)
	if out.Class != "forbidden" {
		t.Errorf("class = %q, want forbidden", out.Class)
	}
	if len(out.Forbidden) == 0 {
		t.Error("Forbidden matches list is empty")
	}
}

// TestClassify_BadEngine rejects unknown engines with 400.
func TestClassify_BadEngine(t *testing.T) {
	auth := dbadmintest.NewAuth().WithUser("alice", "")
	auth.WithGrant("alice", "", dbadmin.RoleOwner)
	e := newEngine(t, auth, dbadmintest.NewConnections(), dbadmintest.NewAudit())
	srv := newTestServer(t, e)

	resp := do(t, srv, http.MethodPost, "/sql/classify", "alice", map[string]any{
		"engine":    "oracle",
		"statement": "SELECT 1",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestClassify_Empty returns ClassRead + empty statements list for an
// empty statement (UX: don't bark at an empty editor).
func TestClassify_Empty(t *testing.T) {
	auth := dbadmintest.NewAuth().WithUser("alice", "")
	auth.WithGrant("alice", "", dbadmin.RoleOwner)
	e := newEngine(t, auth, dbadmintest.NewConnections(), dbadmintest.NewAudit())
	srv := newTestServer(t, e)

	resp := do(t, srv, http.MethodPost, "/sql/classify", "alice", map[string]any{
		"engine":    "mariadb",
		"statement": "",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	out := decodeClassify(t, resp)
	if out.Class != "read" {
		t.Errorf("empty class = %q, want read", out.Class)
	}
	if len(out.Statements) != 0 {
		t.Errorf("len(statements) = %d, want 0", len(out.Statements))
	}
}

// TestClassifyForConnection_UsesConnectionEngine verifies the
// per-connection variant ignores the body's engine and uses the
// connection's engine instead.
func TestClassifyForConnection_UsesConnectionEngine(t *testing.T) {
	auth := dbadmintest.NewAuth().
		WithUser("alice", "").
		WithGrant("alice", "conn-1", dbadmin.RoleAnalyst)
	conns := dbadmintest.NewConnections().WithConnection(dbadmin.Connection{
		ID:     "conn-1",
		Engine: dbadmin.EnginePostgres,
		Host:   "h", Port: 1,
	}, dbadmin.Credentials{})
	e := newEngine(t, auth, conns, dbadmintest.NewAudit())
	srv := newTestServer(t, e)

	resp := do(t, srv, http.MethodPost, "/connections/conn-1/classify", "alice", map[string]any{
		"statement": "SELECT 1",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	out := decodeClassify(t, resp)
	if out.Class != "read" {
		t.Errorf("class = %q, want read", out.Class)
	}
}

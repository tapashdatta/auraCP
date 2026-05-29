package httpapi_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/dbadmintest"
)

// SEC-1: saved queries must be scoped to the saving user. A list issued
// by another user MUST NOT include alice's row, and a delete attempt on
// alice's sid by bob MUST 404 (existence-leak protection, not 403).

type savedDTO struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Statement string   `json:"statement"`
	Tags      []string `json:"tags"`
}

func decodeSavedList(t *testing.T, r *http.Response) []savedDTO {
	t.Helper()
	b, _ := io.ReadAll(r.Body)
	var out []savedDTO
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode saved list: %v body=%s", err, b)
	}
	return out
}

func decodeSavedOne(t *testing.T, r *http.Response) savedDTO {
	t.Helper()
	b, _ := io.ReadAll(r.Body)
	var out savedDTO
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode saved one: %v body=%s", err, b)
	}
	return out
}

func TestSavedQueries_ScopedToUser_ListIsolation(t *testing.T) {
	auth := dbadmintest.NewAuth().
		WithUser("alice", "alice@x").
		WithUser("bob", "bob@x").
		WithGrant("alice", "conn-1", dbadmin.RoleOwner).
		WithGrant("bob", "conn-1", dbadmin.RoleOwner).
		WithStepUpVerified("alice", dbadmin.ActionConnUpdate).
		WithStepUpVerified("bob", dbadmin.ActionConnUpdate)
	conns := dbadmintest.NewConnections().WithConnection(dbadmin.Connection{
		ID: "conn-1", Engine: dbadmin.EngineMariaDB, Host: "h", Port: 1,
	}, dbadmin.Credentials{})
	e := newEngine(t, auth, conns, dbadmintest.NewAudit())
	srv := newTestServer(t, e)

	// Alice creates a saved query.
	resp := do(t, srv, http.MethodPost, "/connections/conn-1/saved-queries", "alice", map[string]any{
		"name":      "alice-q",
		"statement": "SELECT 1",
	})
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("alice create: status=%d body=%s", resp.StatusCode, b)
	}
	resp.Body.Close()

	// Alice sees her query.
	resp = do(t, srv, http.MethodGet, "/connections/conn-1/saved-queries", "alice", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("alice list status=%d", resp.StatusCode)
	}
	aliceList := decodeSavedList(t, resp)
	if len(aliceList) != 1 {
		t.Fatalf("alice list len=%d, want 1", len(aliceList))
	}
	if aliceList[0].Name != "alice-q" {
		t.Errorf("alice[0].Name=%q, want alice-q", aliceList[0].Name)
	}

	// Bob lists same connection → empty.
	resp2 := do(t, srv, http.MethodGet, "/connections/conn-1/saved-queries", "bob", nil)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("bob list status=%d", resp2.StatusCode)
	}
	bobList := decodeSavedList(t, resp2)
	if len(bobList) != 0 {
		t.Errorf("bob list len=%d, want 0 (SEC-1: must not see alice's queries)", len(bobList))
	}
}

func TestSavedQueries_DeleteOtherUserReturns404(t *testing.T) {
	auth := dbadmintest.NewAuth().
		WithUser("alice", "alice@x").
		WithUser("bob", "bob@x").
		WithGrant("alice", "conn-1", dbadmin.RoleOwner).
		WithGrant("bob", "conn-1", dbadmin.RoleOwner).
		WithStepUpVerified("alice", dbadmin.ActionConnUpdate).
		WithStepUpVerified("bob", dbadmin.ActionConnUpdate)
	conns := dbadmintest.NewConnections().WithConnection(dbadmin.Connection{
		ID: "conn-1", Engine: dbadmin.EngineMariaDB, Host: "h", Port: 1,
	}, dbadmin.Credentials{})
	e := newEngine(t, auth, conns, dbadmintest.NewAudit())
	srv := newTestServer(t, e)

	// Alice saves.
	resp := do(t, srv, http.MethodPost, "/connections/conn-1/saved-queries", "alice", map[string]any{
		"name":      "secret-q",
		"statement": "SELECT 42",
	})
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("alice create: status=%d body=%s", resp.StatusCode, b)
	}
	dto := decodeSavedOne(t, resp)
	resp.Body.Close()
	if dto.ID == "" {
		t.Fatal("expected non-empty saved query id")
	}

	// Bob attempts to delete alice's saved query → must 404 (NOT 200, NOT 403).
	resp = do(t, srv, http.MethodDelete, "/connections/conn-1/saved-queries/"+dto.ID, "bob", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("bob delete alice's query: status=%d, want 404 (SEC-1 existence-leak guard)", resp.StatusCode)
	}

	// Alice can still see her query (delete attempt did not remove it).
	resp2 := do(t, srv, http.MethodGet, "/connections/conn-1/saved-queries", "alice", nil)
	defer resp2.Body.Close()
	aliceList := decodeSavedList(t, resp2)
	if len(aliceList) != 1 {
		t.Fatalf("alice list len=%d after bob delete attempt, want 1", len(aliceList))
	}
}

func TestSavedQueries_OwnerCanDeleteOwnRow(t *testing.T) {
	auth := dbadmintest.NewAuth().
		WithUser("alice", "alice@x").
		WithGrant("alice", "conn-1", dbadmin.RoleOwner).
		WithStepUpVerified("alice", dbadmin.ActionConnUpdate)
	conns := dbadmintest.NewConnections().WithConnection(dbadmin.Connection{
		ID: "conn-1", Engine: dbadmin.EngineMariaDB, Host: "h", Port: 1,
	}, dbadmin.Credentials{})
	e := newEngine(t, auth, conns, dbadmintest.NewAudit())
	srv := newTestServer(t, e)

	resp := do(t, srv, http.MethodPost, "/connections/conn-1/saved-queries", "alice", map[string]any{
		"name":      "q",
		"statement": "SELECT 1",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d", resp.StatusCode)
	}
	dto := decodeSavedOne(t, resp)
	resp.Body.Close()

	resp = do(t, srv, http.MethodDelete, "/connections/conn-1/saved-queries/"+dto.ID, "alice", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("alice delete own: status=%d, want 200", resp.StatusCode)
	}

	// List is now empty.
	resp2 := do(t, srv, http.MethodGet, "/connections/conn-1/saved-queries", "alice", nil)
	defer resp2.Body.Close()
	list := decodeSavedList(t, resp2)
	if len(list) != 0 {
		t.Errorf("post-delete list len=%d, want 0", len(list))
	}
}

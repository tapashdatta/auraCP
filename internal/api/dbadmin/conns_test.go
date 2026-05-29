package dbadmin

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// TestAdapter_ConnectionStore_RoundTrip walks the full lifecycle:
// Save (create) → List → Get → Credentials → Delete.
func TestAdapter_ConnectionStore_RoundTrip(t *testing.T) {
	h := newHarness(t)
	_, ownerID := h.seedUser("ROLE_SITE_MANAGER", "manager@example.com")
	ownerStr := strconv.FormatInt(ownerID, 10)

	ctx := context.Background()
	conn := dbadmin.Connection{
		Name:     "prod-primary",
		Engine:   dbadmin.EngineMariaDB,
		Host:     "db.internal",
		Port:     3306,
		Database: "app",
		Username: "appuser",
		Tags:     []dbadmin.Tag{dbadmin.TagProd},
		UseSSL:   true,
		SSLMode:  "require",
		Origin:   dbadmin.OriginManual,
		Owner:    ownerStr,
	}
	creds := dbadmin.Credentials{Password: "s3cret-pass"}

	id, err := h.conns.Save(ctx, conn, creds)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if id == "" {
		t.Fatal("Save returned empty ID")
	}

	// Owner should have an implicit RoleOwner grant.
	roles, err := h.conns.RolesFor(ctx, ownerID, "ROLE_SITE_MANAGER")
	if err != nil {
		t.Fatalf("RolesFor: %v", err)
	}
	if roles[id] != dbadmin.RoleOwner {
		t.Fatalf("auto-grant missing: roles[%s] = %v", id, roles[id])
	}

	// List as the owner (non-admin) — should see the connection.
	u := dbadmin.User{ID: ownerStr, Roles: roles, Attrs: map[string]string{"role": "ROLE_SITE_MANAGER"}}
	listed, err := h.conns.List(ctx, u)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != id {
		t.Fatalf("List = %v, want 1 connection with ID %s", listed, id)
	}
	if listed[0].Name != "prod-primary" || !listed[0].HasTag(dbadmin.TagProd) {
		t.Fatalf("List returned unexpected payload: %+v", listed[0])
	}

	// Get.
	got, err := h.conns.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Host != "db.internal" || got.Port != 3306 {
		t.Fatalf("Get: %+v", got)
	}

	// Credentials round-trip (encrypt → decrypt).
	gotCreds, err := h.conns.Credentials(ctx, id)
	if err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if gotCreds.Password != "s3cret-pass" {
		t.Fatalf("Credentials.Password = %q, want %q", gotCreds.Password, "s3cret-pass")
	}

	// Delete cascades grants.
	if err := h.conns.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	roles, err = h.conns.RolesFor(ctx, ownerID, "ROLE_SITE_MANAGER")
	if err != nil {
		t.Fatalf("RolesFor after delete: %v", err)
	}
	if _, ok := roles[id]; ok {
		t.Fatalf("grant not cascade-deleted with connection")
	}
}

// TestAdapter_ConnectionStore_CredsAADBound is the PR #10.5 /
// FIX-PD-SEC-03 regression test. The encrypted creds_enc blob must be
// bound to the CredsAAD label so a ciphertext extracted from the row
// cannot be decrypted under a different label (which would let a
// future bug or SQL-write primitive replay it into another encrypted-
// at-rest column under the same KEK).
func TestAdapter_ConnectionStore_CredsAADBound(t *testing.T) {
	h := newHarness(t)
	_, ownerID := h.seedUser("ROLE_SITE_MANAGER", "manager@example.com")
	ownerStr := strconv.FormatInt(ownerID, 10)
	ctx := context.Background()

	id, err := h.conns.Save(ctx, dbadmin.Connection{
		Name:   "aad-conn",
		Engine: dbadmin.EnginePostgres,
		Host:   "h",
		Port:   5432,
		Owner:  ownerStr,
	}, dbadmin.Credentials{Password: "aad-secret"})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Pull the raw ciphertext.
	var enc string
	if err := h.st.DB.QueryRow(`SELECT creds_enc FROM aura_db_connections WHERE id = ?`, string(id)).Scan(&enc); err != nil {
		t.Fatalf("scan enc: %v", err)
	}
	// Correct AAD decrypts.
	plain, err := h.box.DecryptAAD(enc, CredsAAD)
	if err != nil {
		t.Fatalf("DecryptAAD(correct): %v", err)
	}
	if !strings.Contains(plain, "aad-secret") {
		t.Fatalf("decrypted plain = %q, want it to contain aad-secret", plain)
	}
	// Wrong AAD must fail.
	if _, err := h.box.DecryptAAD(enc, "wrong:label"); err == nil {
		t.Fatal("DecryptAAD with wrong label succeeded — AAD binding broken")
	}
	// Legacy AAD-less Decrypt path must also fail (the ciphertext was
	// written under AAD-derived sub-key, not the raw KEK).
	if _, err := h.box.Decrypt(enc); err == nil {
		t.Fatal("box.Decrypt of AAD-bound ciphertext succeeded — cross-key replay possible")
	}
}

// TestAdapter_ConnectionStore_DuplicateNameMapsConflict is the PR #10.5 /
// FIX-C7 regression test: duplicate UNIQUE-name inserts must surface
// as dbadmin.ErrConflict, not a raw SQLite error string.
func TestAdapter_ConnectionStore_DuplicateNameMapsConflict(t *testing.T) {
	h := newHarness(t)
	_, ownerID := h.seedUser("ROLE_SITE_MANAGER", "manager@example.com")
	ownerStr := strconv.FormatInt(ownerID, 10)
	ctx := context.Background()

	conn := dbadmin.Connection{
		Name:   "dup-name",
		Engine: dbadmin.EnginePostgres,
		Host:   "h",
		Port:   5432,
		Owner:  ownerStr,
	}
	if _, err := h.conns.Save(ctx, conn, dbadmin.Credentials{Password: "x"}); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	// Second insert with the same name must surface ErrConflict.
	conn.ID = "" // force create path
	_, err := h.conns.Save(ctx, conn, dbadmin.Credentials{Password: "x"})
	if !errors.Is(err, dbadmin.ErrConflict) {
		t.Fatalf("second Save: err = %v, want ErrConflict", err)
	}
}

// TestAdapter_ConnectionStore_OrphanGrantsCascadeOnUserDelete verifies
// PR #10.5 / FIX-INT-6: deleting a panel_users row must cascade into
// aura_db_grants via the migration trigger.
func TestAdapter_ConnectionStore_OrphanGrantsCascadeOnUserDelete(t *testing.T) {
	h := newHarness(t)
	_, ownerID := h.seedUser("ROLE_SITE_MANAGER", "owner@example.com")
	ownerStr := strconv.FormatInt(ownerID, 10)
	ctx := context.Background()

	id, err := h.conns.Save(ctx, dbadmin.Connection{
		Name:   "orphan-test",
		Engine: dbadmin.EnginePostgres,
		Host:   "h",
		Port:   5432,
		Owner:  ownerStr,
	}, dbadmin.Credentials{Password: "x"})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Owner should have a grant row.
	var n int
	if err := h.st.DB.QueryRow(`SELECT COUNT(*) FROM aura_db_grants WHERE connection_id = ? AND user_id = ?`,
		string(id), ownerStr).Scan(&n); err != nil {
		t.Fatalf("count pre-delete: %v", err)
	}
	if n != 1 {
		t.Fatalf("pre-delete grant count = %d, want 1", n)
	}
	// Delete the panel user.
	if _, err := h.st.DB.Exec(`DELETE FROM panel_users WHERE id = ?`, ownerID); err != nil {
		t.Fatalf("delete panel_user: %v", err)
	}
	// Trigger must have cascaded the grant.
	if err := h.st.DB.QueryRow(`SELECT COUNT(*) FROM aura_db_grants WHERE user_id = ?`,
		ownerStr).Scan(&n); err != nil {
		t.Fatalf("count post-delete: %v", err)
	}
	if n != 0 {
		t.Fatalf("post-delete grant count = %d, want 0 (orphan grant survived)", n)
	}
}

// TestAdapter_ConnectionStore_NotFound verifies the typed sentinel
// errors flow through (engine maps them to 404).
func TestAdapter_ConnectionStore_NotFound(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	_, err := h.conns.Get(ctx, "nonexistent")
	if !errors.Is(err, dbadmin.ErrNotFound) {
		t.Fatalf("Get nonexistent: %v, want ErrNotFound", err)
	}
	if err := h.conns.Delete(ctx, "nonexistent"); !errors.Is(err, dbadmin.ErrNotFound) {
		t.Fatalf("Delete nonexistent: %v, want ErrNotFound", err)
	}
	if _, err := h.conns.Credentials(ctx, "nonexistent"); !errors.Is(err, dbadmin.ErrNotFound) {
		t.Fatalf("Credentials nonexistent: %v, want ErrNotFound", err)
	}
}

// TestAdapter_ConnectionStore_AdminSeesAll verifies ROLE_ADMIN gets
// access to every connection via the admin short-circuit in
// HasPermission + the admin SQL branch in List, without RolesFor
// having to enumerate (PR #10.5 / FIX-INT-14). The map is intentionally
// nil for admins; List handles admin visibility directly.
func TestAdapter_ConnectionStore_AdminSeesAll(t *testing.T) {
	h := newHarness(t)
	_, ownerID := h.seedUser("ROLE_SITE_MANAGER", "manager@example.com")
	_, adminID := h.seedUser("ROLE_ADMIN", "admin@example.com")

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_, err := h.conns.Save(ctx, dbadmin.Connection{
			Name:     "conn-" + strconv.Itoa(i),
			Engine:   dbadmin.EnginePostgres,
			Host:     "h",
			Port:     5432,
			Username: "u",
			Owner:    strconv.FormatInt(ownerID, 10),
		}, dbadmin.Credentials{Password: "p"})
		if err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	// RolesFor(admin) returns nil — admin's permission gate is
	// short-circuited inside HasPermission and inside List's admin
	// SQL branch, neither of which consults the map.
	roles, err := h.conns.RolesFor(ctx, adminID, "ROLE_ADMIN")
	if err != nil {
		t.Fatalf("RolesFor admin: %v", err)
	}
	if roles != nil {
		t.Fatalf("RolesFor admin = %v, want nil (FIX-INT-14 short-circuit)", roles)
	}

	// The authoritative admin path: List must surface all 3 rows.
	adminUser := dbadmin.User{
		ID:    strconv.FormatInt(adminID, 10),
		Attrs: map[string]string{"role": "ROLE_ADMIN"},
	}
	listed, err := h.conns.List(ctx, adminUser)
	if err != nil {
		t.Fatalf("List admin: %v", err)
	}
	if len(listed) != 3 {
		t.Fatalf("admin List = %d connections, want 3", len(listed))
	}
}

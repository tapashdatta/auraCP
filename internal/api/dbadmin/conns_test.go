package dbadmin

import (
	"context"
	"errors"
	"strconv"
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

// TestAdapter_ConnectionStore_AdminSeesAll verifies ROLE_ADMIN gets an
// implicit RoleOwner on every connection.
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

	roles, err := h.conns.RolesFor(ctx, adminID, "ROLE_ADMIN")
	if err != nil {
		t.Fatalf("RolesFor admin: %v", err)
	}
	if len(roles) != 3 {
		t.Fatalf("admin sees %d connections, want 3", len(roles))
	}
	for cid, r := range roles {
		if r != dbadmin.RoleOwner {
			t.Fatalf("admin role on %s = %v, want RoleOwner", cid, r)
		}
	}
}

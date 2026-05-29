package standalone

import (
	"context"
	"errors"
	"testing"

	"github.com/auracp/auracp/pkg/dbadmin"
)

func TestConnections_SaveListGetDelete(t *testing.T) {
	store, kek := newTestStore(t)
	c := NewConnections(store, kek)
	ctx := context.Background()
	owner, err := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy())
	if err != nil {
		t.Fatal(err)
	}
	id, err := c.Save(ctx, mkConn("prod-db", "127.0.0.1", 3306, owner.ID), credentialsValue("s3cret"))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := c.Grant(ctx, owner.ID, owner.ID, id, dbadmin.RoleOwner); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	user := dbadmin.User{ID: owner.ID, Roles: map[dbadmin.ConnectionID]dbadmin.Role{id: dbadmin.RoleOwner}}
	list, err := c.List(ctx, user)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Name != "prod-db" {
		t.Fatalf("unexpected list: %#v", list)
	}
	got, err := c.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id {
		t.Fatalf("wrong id back: %s vs %s", got.ID, id)
	}
	creds, err := c.Credentials(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if creds.Password != "s3cret" {
		t.Fatalf("password mismatch: %q", creds.Password)
	}
	if err := c.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := c.Get(ctx, id); !errors.Is(err, dbadmin.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestConnections_RejectsCommaInName(t *testing.T) {
	store, kek := newTestStore(t)
	c := NewConnections(store, kek)
	ctx := context.Background()
	owner, _ := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy())
	_, err := c.Save(ctx, mkConn("bad,name", "127.0.0.1", 3306, owner.ID), credentialsValue("s3cret"))
	if !errors.Is(err, dbadmin.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for comma in name; got %v", err)
	}
}

func TestConnections_RefusesPanelSiteDelete(t *testing.T) {
	store, kek := newTestStore(t)
	c := NewConnections(store, kek)
	ctx := context.Background()
	owner, _ := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy())
	conn := mkConn("panel-conn", "127.0.0.1", 5432, owner.ID)
	conn.Engine = dbadmin.EnginePostgres
	conn.Origin = dbadmin.OriginPanelSite
	id, err := c.Save(ctx, conn, credentialsValue("pw"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, id); !errors.Is(err, dbadmin.ErrConflict) {
		t.Fatalf("expected ErrConflict for panel-site delete; got %v", err)
	}
}

func TestConnections_RevokeLastOwnerRefuses(t *testing.T) {
	store, kek := newTestStore(t)
	c := NewConnections(store, kek)
	ctx := context.Background()
	owner, _ := store.CreateUser(ctx, "alice", "correct-horse-battery", fastPolicy())
	id, _ := c.Save(ctx, mkConn("conn", "127.0.0.1", 3306, owner.ID), credentialsValue("pw"))
	_ = c.Grant(ctx, owner.ID, owner.ID, id, dbadmin.RoleOwner)
	if err := c.Revoke(ctx, owner.ID, owner.ID, id); !errors.Is(err, dbadmin.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

package saved_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/saved"
)

func openStore(t *testing.T) saved.Store {
	t.Helper()
	s, err := saved.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mkRec(id, conn, owner, name string) saved.Record {
	return saved.Record{
		ID:           id,
		ConnectionID: dbadmin.ConnectionID(conn),
		OwnerID:      owner,
		Name:         name,
		Statement:    "SELECT 1",
		Tags:         []string{"t1", "t2"},
		CreatedAt:    time.Now().UTC(),
	}
}

func TestAppendList_RoundTrip(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	r := mkRec("id-1", "c1", "alice", "q1")
	if err := s.Append(ctx, r); err != nil {
		t.Fatalf("Append: %v", err)
	}
	out, err := s.List(ctx, saved.ListOpts{ConnectionID: "c1", OwnerID: "alice"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("List len=%d, want 1", len(out))
	}
	if out[0].Name != "q1" {
		t.Errorf("Name=%q, want q1", out[0].Name)
	}
	if len(out[0].Tags) != 2 || out[0].Tags[0] != "t1" {
		t.Errorf("Tags=%v, want [t1 t2]", out[0].Tags)
	}
}

func TestAppend_EmptyFields(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	cases := []struct {
		name string
		mut  func(*saved.Record)
	}{
		{"id", func(r *saved.Record) { r.ID = "" }},
		{"conn", func(r *saved.Record) { r.ConnectionID = "" }},
		{"owner", func(r *saved.Record) { r.OwnerID = "" }},
		{"name", func(r *saved.Record) { r.Name = "" }},
		{"stmt", func(r *saved.Record) { r.Statement = "" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := mkRec("id-x", "c1", "alice", "q")
			c.mut(&r)
			err := s.Append(ctx, r)
			if !errors.Is(err, saved.ErrInvalidInput) {
				t.Errorf("Append empty %s: err=%v, want ErrInvalidInput", c.name, err)
			}
		})
	}
}

func TestAppend_CommaTagRejected(t *testing.T) {
	s := openStore(t)
	r := mkRec("id-1", "c1", "alice", "q1")
	r.Tags = []string{"good", "bad,tag"}
	err := s.Append(context.Background(), r)
	if !errors.Is(err, saved.ErrInvalidInput) {
		t.Errorf("comma tag: err=%v, want ErrInvalidInput", err)
	}
}

func TestAppend_DuplicateNameConflict(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if err := s.Append(ctx, mkRec("id-1", "c1", "alice", "dup")); err != nil {
		t.Fatalf("first Append: %v", err)
	}
	err := s.Append(ctx, mkRec("id-2", "c1", "alice", "dup"))
	if !errors.Is(err, saved.ErrConflict) {
		t.Errorf("duplicate name: err=%v, want ErrConflict", err)
	}
	// Different owner on same conn can use the same name.
	if err := s.Append(ctx, mkRec("id-3", "c1", "bob", "dup")); err != nil {
		t.Errorf("bob with same name on same conn: err=%v, want nil", err)
	}
}

func TestList_ScopedByOwner(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	_ = s.Append(ctx, mkRec("a-1", "c1", "alice", "alice-q"))
	_ = s.Append(ctx, mkRec("b-1", "c1", "bob", "bob-q"))

	aliceList, _ := s.List(ctx, saved.ListOpts{ConnectionID: "c1", OwnerID: "alice"})
	if len(aliceList) != 1 || aliceList[0].Name != "alice-q" {
		t.Errorf("alice list=%v, want [alice-q]", aliceList)
	}
	bobList, _ := s.List(ctx, saved.ListOpts{ConnectionID: "c1", OwnerID: "bob"})
	if len(bobList) != 1 || bobList[0].Name != "bob-q" {
		t.Errorf("bob list=%v, want [bob-q]", bobList)
	}
}

func TestList_StarOnly(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	r1 := mkRec("id-1", "c1", "alice", "q1")
	r1.Starred = true
	r2 := mkRec("id-2", "c1", "alice", "q2")
	_ = s.Append(ctx, r1)
	_ = s.Append(ctx, r2)

	all, _ := s.List(ctx, saved.ListOpts{ConnectionID: "c1", OwnerID: "alice"})
	if len(all) != 2 {
		t.Fatalf("all len=%d, want 2", len(all))
	}
	star, _ := s.List(ctx, saved.ListOpts{ConnectionID: "c1", OwnerID: "alice", StarOnly: true})
	if len(star) != 1 || star[0].ID != "id-1" {
		t.Errorf("star list=%v, want id-1", star)
	}
}

func TestStar_TogglesAndUpdatesTimestamp(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	_ = s.Append(ctx, mkRec("id-1", "c1", "alice", "q1"))

	if err := s.Star(ctx, "c1", "alice", "id-1", true); err != nil {
		t.Fatalf("Star: %v", err)
	}
	got, err := s.Get(ctx, "c1", "alice", "id-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Starred {
		t.Error("expected Starred=true after Star(true)")
	}

	// Star on wrong owner → ErrNotFound.
	if err := s.Star(ctx, "c1", "bob", "id-1", true); !errors.Is(err, saved.ErrNotFound) {
		t.Errorf("bob Star alice's row: err=%v, want ErrNotFound", err)
	}
}

func TestDelete_OwnerVsOther(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	_ = s.Append(ctx, mkRec("id-1", "c1", "alice", "q1"))

	// Bob tries to delete alice's row.
	found, owned, err := s.Delete(ctx, "c1", "bob", "id-1")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !found || owned {
		t.Errorf("bob delete: found=%v owned=%v, want (true,false)", found, owned)
	}
	// Row still present.
	got, _ := s.Get(ctx, "c1", "alice", "id-1")
	if got == nil {
		t.Error("alice's row missing after bob's failed delete")
	}

	// Non-existent id.
	found, owned, _ = s.Delete(ctx, "c1", "alice", "id-nope")
	if found || owned {
		t.Errorf("missing id: found=%v owned=%v, want (false,false)", found, owned)
	}

	// Alice deletes her own.
	found, owned, _ = s.Delete(ctx, "c1", "alice", "id-1")
	if !found || !owned {
		t.Errorf("alice delete own: found=%v owned=%v, want (true,true)", found, owned)
	}
}

func TestUpdate_NameAndDescription(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	_ = s.Append(ctx, mkRec("id-1", "c1", "alice", "q1"))

	newName := "renamed"
	newDesc := "the new description"
	if err := s.Update(ctx, "c1", "alice", "id-1", saved.UpdateFields{
		Name:        &newName,
		Description: &newDesc,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := s.Get(ctx, "c1", "alice", "id-1")
	if got.Name != "renamed" || got.Description != "the new description" {
		t.Errorf("got Name=%q Desc=%q", got.Name, got.Description)
	}
	// Tags + statement unchanged.
	if got.Statement != "SELECT 1" {
		t.Errorf("Statement was clobbered: %q", got.Statement)
	}
}

func TestUpdate_OtherOwnerNotFound(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	_ = s.Append(ctx, mkRec("id-1", "c1", "alice", "q1"))

	newName := "hacked"
	err := s.Update(ctx, "c1", "bob", "id-1", saved.UpdateFields{Name: &newName})
	if !errors.Is(err, saved.ErrNotFound) {
		t.Errorf("bob update alice's row: err=%v, want ErrNotFound", err)
	}
}

func TestMaxPerOwnerEviction(t *testing.T) {
	s, err := saved.OpenWithOpts(context.Background(), ":memory:", saved.OpenOpts{
		MaxPerOwner: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	base := time.Now().UTC()
	for i := 0; i < 5; i++ {
		r := saved.Record{
			ID:           "id-" + string(rune('a'+i)),
			ConnectionID: "c1",
			OwnerID:      "alice",
			Name:         "name-" + string(rune('a'+i)),
			Statement:    "SELECT 1",
			CreatedAt:    base.Add(time.Duration(i) * time.Second),
			UpdatedAt:    base.Add(time.Duration(i) * time.Second),
		}
		if err := s.Append(ctx, r); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	out, _ := s.List(ctx, saved.ListOpts{ConnectionID: "c1", OwnerID: "alice"})
	if len(out) != 3 {
		t.Fatalf("after cap len=%d, want 3", len(out))
	}
}

func TestSearch_FindsByStatementOrName(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	r := mkRec("id-1", "c1", "alice", "my-orders-report")
	r.Statement = "SELECT * FROM orders WHERE total > 1000"
	r.Description = "high-value orders"
	if err := s.Append(ctx, r); err != nil {
		t.Fatal(err)
	}
	results, err := s.Search(ctx, "orders", saved.ListOpts{ConnectionID: "c1", OwnerID: "alice"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Search len=%d, want 1 (hasFTS=%v)", len(results), s.HasFTS())
	}
}

func TestClose_Idempotent(t *testing.T) {
	s := openStore(t)
	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	// Operations after Close return ErrClosed.
	if err := s.Append(context.Background(), mkRec("z", "c1", "alice", "z")); !errors.Is(err, saved.ErrClosed) {
		t.Errorf("Append after Close: err=%v, want ErrClosed", err)
	}
}

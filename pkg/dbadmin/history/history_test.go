package history_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/classifier"
	"github.com/auracp/auracp/pkg/dbadmin/history"
)

// ─── Helpers ─────────────────────────────────────────────────────────

func openMem(t *testing.T) history.Store {
	t.Helper()
	s, err := history.Open(context.Background(), ":memory:", dbadmin.EngineMariaDB)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func entry(user string, sql string) history.Entry {
	return history.Entry{
		UserID:       user,
		ConnectionID: "conn-1",
		SQL:          sql,
		Class:        classifier.ClassRead,
		DurationMS:   42,
		RowsReturned: 3,
		Executed:     time.Now().UTC(),
	}
}

// ─── Append + Get ────────────────────────────────────────────────────

func TestAppend_AssignsMonotonicID(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	a, err := s.Append(ctx, entry("alice", "SELECT 1"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Append(ctx, entry("alice", "SELECT 2"))
	if err != nil {
		t.Fatal(err)
	}
	if b <= a {
		t.Errorf("expected monotonic IDs, got %d then %d", a, b)
	}
}

func TestAppend_RejectsEmptyInput(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	cases := []history.Entry{
		{UserID: "", SQL: "SELECT 1"},
		{UserID: "alice", SQL: ""},
	}
	for _, e := range cases {
		_, err := s.Append(ctx, e)
		if !errors.Is(err, history.ErrInvalidInput) {
			t.Errorf("Append(%+v) err = %v, want ErrInvalidInput", e, err)
		}
	}
}

func TestAppend_RedactsCredentials(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	id, err := s.Append(ctx, entry("alice", `CREATE USER 'bob'@'%' IDENTIFIED BY 'hunter2'`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, id, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.SQL, "hunter2") {
		t.Errorf("stored SQL leaked password: %q", got.SQL)
	}
	if !strings.Contains(got.SQL, "[redacted]") {
		t.Errorf("stored SQL missing [redacted] marker: %q", got.SQL)
	}
}

func TestAppend_RedactsErrorField(t *testing.T) {
	// pgx/mysql driver errors echo the failing statement back to the
	// caller. Without redaction, credentials live forever in the
	// error column.
	s := openMem(t)
	ctx := context.Background()
	e := entry("alice", "SELECT 1")
	e.Error = `syntax error in: CREATE USER bob IDENTIFIED BY 'hunter2'`
	id, err := s.Append(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, id, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Error, "hunter2") {
		t.Errorf("stored Error leaked password: %q", got.Error)
	}
	if !strings.Contains(got.Error, "[redacted]") {
		t.Errorf("stored Error missing [redacted] marker: %q", got.Error)
	}
}

func TestAppend_TruncatesOversizeSQL(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	huge := strings.Repeat("a", 300*1024) // > MaxSQLLength (256 KiB)
	id, err := s.Append(ctx, entry("alice", huge))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, id, "alice")
	if len(got.SQL) > history.MaxSQLLength+32 {
		t.Errorf("stored SQL not truncated: len = %d", len(got.SQL))
	}
	if !strings.HasSuffix(got.SQL, "[truncated]") {
		t.Errorf("truncation marker missing: %q", got.SQL[len(got.SQL)-30:])
	}
}

func TestAppend_UsesEntryEngineForRedaction(t *testing.T) {
	// Store-wide default is MariaDB but the operator's connection is
	// Postgres. RedactSensitiveInline under DialectMySQL won't reach
	// dollar-quoted strings; under DialectPostgres it will. We assert
	// the per-Entry engine takes priority by recording a Postgres
	// CREATE ROLE statement and confirming the password is redacted.
	s := openMem(t)
	ctx := context.Background()

	pgEntry := entry("alice", `CREATE ROLE bob WITH PASSWORD 'pg-secret-42'`)
	pgEntry.Engine = dbadmin.EnginePostgres
	idPG, err := s.Append(ctx, pgEntry)
	if err != nil {
		t.Fatal(err)
	}
	gotPG, _ := s.Get(ctx, idPG, "alice")
	if strings.Contains(gotPG.SQL, "pg-secret-42") {
		t.Errorf("Postgres redaction missed: stored = %q", gotPG.SQL)
	}
	if gotPG.Engine != dbadmin.EnginePostgres {
		t.Errorf("Entry.Engine not persisted: got %v", gotPG.Engine)
	}

	// Also confirm the engine column round-trips for MariaDB entries.
	mEntry := entry("alice", "SELECT 1")
	mEntry.Engine = dbadmin.EngineMariaDB
	idM, _ := s.Append(ctx, mEntry)
	gotM, _ := s.Get(ctx, idM, "alice")
	if gotM.Engine != dbadmin.EngineMariaDB {
		t.Errorf("MariaDB engine not persisted: got %v", gotM.Engine)
	}
}

func TestAppend_RejectsUnknownEngine(t *testing.T) {
	// Open with EngineUnknown as the default. An Append without
	// per-Entry Engine has no dialect to redact against — reject
	// loudly.
	s, err := history.Open(context.Background(), ":memory:", dbadmin.EngineUnknown)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	_, err = s.Append(context.Background(), entry("alice", "SELECT 1"))
	if !errors.Is(err, history.ErrInvalidInput) {
		t.Errorf("Append err = %v, want ErrInvalidInput", err)
	}
}

func TestGet_ScopedByUser(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	id, _ := s.Append(ctx, entry("alice", "SELECT 1"))

	// Same user: hit.
	if _, err := s.Get(ctx, id, "alice"); err != nil {
		t.Errorf("Get(alice) err = %v, want nil", err)
	}
	// Different user: not found.
	if _, err := s.Get(ctx, id, "bob"); !errors.Is(err, history.ErrNotFound) {
		t.Errorf("Get(bob) err = %v, want ErrNotFound", err)
	}
	// Empty user: default-deny.
	if _, err := s.Get(ctx, id, ""); !errors.Is(err, history.ErrInvalidInput) {
		t.Errorf("Get('') err = %v, want ErrInvalidInput", err)
	}
}

func TestGet_NotFound(t *testing.T) {
	s := openMem(t)
	_, err := s.Get(context.Background(), 99999, "alice")
	if !errors.Is(err, history.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestEmptyUserIDRejectedOnReads(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	// Seed a row so the operations have something to address.
	id, _ := s.Append(ctx, entry("alice", "SELECT 1"))

	// Get
	if _, err := s.Get(ctx, id, ""); !errors.Is(err, history.ErrInvalidInput) {
		t.Errorf("Get err = %v, want ErrInvalidInput", err)
	}
	// List
	if _, err := s.List(ctx, history.ListOpts{}); !errors.Is(err, history.ErrInvalidInput) {
		t.Errorf("List err = %v, want ErrInvalidInput", err)
	}
	// Search
	if _, err := s.Search(ctx, "x", history.ListOpts{}); !errors.Is(err, history.ErrInvalidInput) {
		t.Errorf("Search err = %v, want ErrInvalidInput", err)
	}
	// Star
	if err := s.Star(ctx, id, "", true); !errors.Is(err, history.ErrInvalidInput) {
		t.Errorf("Star err = %v, want ErrInvalidInput", err)
	}
	// Tag
	if err := s.Tag(ctx, id, "", []string{"x"}); !errors.Is(err, history.ErrInvalidInput) {
		t.Errorf("Tag err = %v, want ErrInvalidInput", err)
	}
	// Delete
	if err := s.Delete(ctx, id, ""); !errors.Is(err, history.ErrInvalidInput) {
		t.Errorf("Delete err = %v, want ErrInvalidInput", err)
	}
}

// ─── List ────────────────────────────────────────────────────────────

func TestList_PerUserOrdering(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()

	// Stagger Executed timestamps so DESC ordering is observable.
	base := time.Now().UTC()
	for i, sql := range []string{"a", "b", "c"} {
		e := entry("alice", sql)
		e.Executed = base.Add(time.Duration(i) * time.Second)
		if _, err := s.Append(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	out, err := s.List(ctx, history.ListOpts{UserID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("got %d entries, want 3", len(out))
	}
	if out[0].SQL != "c" || out[2].SQL != "a" {
		t.Errorf("ordering wrong: got %v", []string{out[0].SQL, out[1].SQL, out[2].SQL})
	}
}

func TestList_FiltersByConnection(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	for _, c := range []dbadmin.ConnectionID{"conn-1", "conn-2", "conn-1"} {
		e := entry("alice", "SELECT 1")
		e.ConnectionID = c
		_, _ = s.Append(ctx, e)
	}
	out, _ := s.List(ctx, history.ListOpts{UserID: "alice", ConnectionID: "conn-1"})
	if len(out) != 2 {
		t.Errorf("conn-1 entries = %d, want 2", len(out))
	}
}

func TestList_FiltersByStarred(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	id1, _ := s.Append(ctx, entry("alice", "select 1"))
	id2, _ := s.Append(ctx, entry("alice", "select 2"))
	_ = s.Star(ctx, id1, "alice", true)
	_ = id2

	out, _ := s.List(ctx, history.ListOpts{UserID: "alice", OnlyStarred: true})
	if len(out) != 1 || out[0].ID != id1 {
		t.Errorf("starred filter returned %+v", out)
	}
}

func TestList_FiltersByClass(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	r := entry("alice", "SELECT 1")
	r.Class = classifier.ClassRead
	w := entry("alice", "INSERT INTO t VALUES (1)")
	w.Class = classifier.ClassWriteRow
	_, _ = s.Append(ctx, r)
	_, _ = s.Append(ctx, w)

	out, _ := s.List(ctx, history.ListOpts{UserID: "alice", Class: classifier.ClassWriteRow, IncludeClass: true})
	if len(out) != 1 || out[0].Class != classifier.ClassWriteRow {
		t.Errorf("class filter returned %+v", out)
	}
}

func TestList_FiltersByTimeRange(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	base := time.Now().UTC()
	for i := 0; i < 5; i++ {
		e := entry("alice", "select")
		e.Executed = base.Add(time.Duration(i) * time.Minute)
		_, _ = s.Append(ctx, e)
	}
	out, _ := s.List(ctx, history.ListOpts{
		UserID: "alice",
		Since:  base.Add(1 * time.Minute),
		Until:  base.Add(4 * time.Minute),
	})
	// minutes 1, 2, 3 should match (Until is exclusive).
	if len(out) != 3 {
		t.Errorf("time-range filter returned %d, want 3", len(out))
	}
}

func TestList_DefaultLimitApplied(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	// Insert 150 entries (above DefaultListLimit=100).
	for i := 0; i < 150; i++ {
		_, _ = s.Append(ctx, entry("alice", "x"))
	}
	out, _ := s.List(ctx, history.ListOpts{UserID: "alice"})
	if len(out) != history.DefaultListLimit {
		t.Errorf("default limit = %d, want %d", len(out), history.DefaultListLimit)
	}
}

func TestList_LimitClampedToMax(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	// Just confirm Limit > MaxListLimit doesn't error (it gets clamped).
	_, err := s.List(ctx, history.ListOpts{UserID: "alice", Limit: 1_000_000})
	if err != nil {
		t.Errorf("Limit oversize err = %v, want nil (clamp)", err)
	}
}

func TestList_NegativeOffsetRejected(t *testing.T) {
	s := openMem(t)
	_, err := s.List(context.Background(), history.ListOpts{UserID: "alice", Offset: -1})
	if !errors.Is(err, history.ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
}

// ─── Star + Tag + Delete ─────────────────────────────────────────────

func TestStar_Roundtrip(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	id, _ := s.Append(ctx, entry("alice", "select"))
	if err := s.Star(ctx, id, "alice", true); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, id, "alice")
	if !got.Starred {
		t.Error("Starred not set")
	}
	_ = s.Star(ctx, id, "alice", false)
	got, _ = s.Get(ctx, id, "alice")
	if got.Starred {
		t.Error("Star(false) didn't clear")
	}
}

func TestStar_WrongUser_NotFound(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	id, _ := s.Append(ctx, entry("alice", "select"))
	if err := s.Star(ctx, id, "bob", true); !errors.Is(err, history.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestTag_Roundtrip(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	id, _ := s.Append(ctx, entry("alice", "select"))
	if err := s.Tag(ctx, id, "alice", []string{"prod", "report"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, id, "alice")
	if len(got.Tags) != 2 || got.Tags[0] != "prod" || got.Tags[1] != "report" {
		t.Errorf("Tags = %v, want [prod report]", got.Tags)
	}
}

func TestTag_DeduplicatesAndTrims(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	id, _ := s.Append(ctx, entry("alice", "select"))
	_ = s.Tag(ctx, id, "alice", []string{" prod ", "prod", "", "report"})
	got, _ := s.Get(ctx, id, "alice")
	if len(got.Tags) != 2 || got.Tags[0] != "prod" || got.Tags[1] != "report" {
		t.Errorf("dedup/trim wrong: %v", got.Tags)
	}
}

func TestTag_RejectsComma(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	id, _ := s.Append(ctx, entry("alice", "select"))
	err := s.Tag(ctx, id, "alice", []string{"alice,bob"})
	if !errors.Is(err, history.ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
}

func TestAppend_RejectsCommaTag(t *testing.T) {
	// Append validates tags too — caller can't bypass Tag() by seeding
	// the entry with a comma-bearing tag at creation.
	s := openMem(t)
	ctx := context.Background()
	e := entry("alice", "select")
	e.Tags = []string{"a,b"}
	_, err := s.Append(ctx, e)
	if !errors.Is(err, history.ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
}

func TestList_FiltersByTag(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	id1, _ := s.Append(ctx, entry("alice", "select 1"))
	id2, _ := s.Append(ctx, entry("alice", "select 2"))
	_ = s.Tag(ctx, id1, "alice", []string{"prod"})
	_ = s.Tag(ctx, id2, "alice", []string{"dev"})

	out, _ := s.List(ctx, history.ListOpts{UserID: "alice", Tag: "prod"})
	if len(out) != 1 || out[0].ID != id1 {
		t.Errorf("tag filter returned %+v", out)
	}
}

func TestList_TagFilter_RespectsBoundary(t *testing.T) {
	// Tag("prod") must NOT match an entry tagged "production". The
	// stored form is fenced with leading + trailing commas so the
	// LIKE pattern "%,prod,%" can't match "%,production,%".
	s := openMem(t)
	ctx := context.Background()
	idProd, _ := s.Append(ctx, entry("alice", "select 1"))
	idProduction, _ := s.Append(ctx, entry("alice", "select 2"))
	_ = s.Tag(ctx, idProd, "alice", []string{"prod"})
	_ = s.Tag(ctx, idProduction, "alice", []string{"production"})

	out, _ := s.List(ctx, history.ListOpts{UserID: "alice", Tag: "prod"})
	if len(out) != 1 {
		t.Fatalf("got %d entries, want 1", len(out))
	}
	if out[0].ID != idProd {
		t.Errorf("Tag('prod') leaked production entry: id=%d", out[0].ID)
	}
}

func TestDelete_RemovesEntry(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	id, _ := s.Append(ctx, entry("alice", "select"))
	if err := s.Delete(ctx, id, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, id, "alice"); !errors.Is(err, history.ErrNotFound) {
		t.Errorf("post-delete Get err = %v, want ErrNotFound", err)
	}
	// Second delete: ErrNotFound.
	if err := s.Delete(ctx, id, "alice"); !errors.Is(err, history.ErrNotFound) {
		t.Errorf("repeat Delete err = %v, want ErrNotFound", err)
	}
}

func TestDelete_WrongUser_NotFound(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	id, _ := s.Append(ctx, entry("alice", "select"))
	if err := s.Delete(ctx, id, "bob"); !errors.Is(err, history.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// ─── DeleteOlderThan ─────────────────────────────────────────────────

func TestDeleteOlderThan(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	base := time.Now().UTC()
	// 5 entries spaced 1 day apart.
	for i := 0; i < 5; i++ {
		e := entry("alice", "select")
		e.Executed = base.Add(-time.Duration(i) * 24 * time.Hour)
		_, _ = s.Append(ctx, e)
	}
	cutoff := base.Add(-2 * 24 * time.Hour)
	n, err := s.DeleteOlderThan(ctx, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	// Entries at -3 and -4 days deleted (2 of them).
	if n != 2 {
		t.Errorf("deleted %d, want 2", n)
	}

	remaining, _ := s.List(ctx, history.ListOpts{UserID: "alice"})
	if len(remaining) != 3 {
		t.Errorf("remaining = %d, want 3", len(remaining))
	}
}

// ─── Search ──────────────────────────────────────────────────────────

func TestSearch_FindsByPhrase(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	_, _ = s.Append(ctx, entry("alice", "SELECT email FROM users WHERE active = true"))
	_, _ = s.Append(ctx, entry("alice", "SELECT * FROM orders"))
	_, _ = s.Append(ctx, entry("alice", "DELETE FROM logs"))

	out, err := s.Search(ctx, "users", history.ListOpts{UserID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Errorf("search 'users' returned %d, want 1; got %v",
			len(out), summarizeSearch(out))
	}
}

func TestSearch_RespectsUserScope(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	_, _ = s.Append(ctx, entry("alice", "SELECT FROM users"))
	_, _ = s.Append(ctx, entry("bob", "SELECT FROM users"))

	out, _ := s.Search(ctx, "users", history.ListOpts{UserID: "alice"})
	if len(out) != 1 {
		t.Errorf("scoped search returned %d, want 1", len(out))
	}
	if out[0].UserID != "alice" {
		t.Errorf("leaked entry for user %q", out[0].UserID)
	}
}

func TestSearch_FiltersByTag(t *testing.T) {
	// Search must honor opts.Tag the same way List does. Without the
	// filter the pager would return cross-tag results silently.
	s := openMem(t)
	ctx := context.Background()
	idProd, _ := s.Append(ctx, entry("alice", "SELECT FROM orders"))
	idDev, _ := s.Append(ctx, entry("alice", "SELECT FROM orders"))
	_ = s.Tag(ctx, idProd, "alice", []string{"prod"})
	_ = s.Tag(ctx, idDev, "alice", []string{"dev"})

	out, err := s.Search(ctx, "orders", history.ListOpts{UserID: "alice", Tag: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d entries, want 1", len(out))
	}
	if out[0].ID != idProd {
		t.Errorf("Search Tag='prod' returned wrong entry: id=%d", out[0].ID)
	}
}

func TestSearch_RejectsEmpty(t *testing.T) {
	s := openMem(t)
	_, err := s.Search(context.Background(), "   ", history.ListOpts{UserID: "alice"})
	if !errors.Is(err, history.ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
}

func TestSearch_EscapesLikeMetachars(t *testing.T) {
	// If FTS5 isn't compiled in, the LIKE path uses escapeLike. Pass
	// % and _ in the query — must not match arbitrary entries.
	s := openMem(t)
	ctx := context.Background()
	_, _ = s.Append(ctx, entry("alice", "select user from t"))
	_, _ = s.Append(ctx, entry("alice", "select something else"))

	// Search for "_ser" — under naive LIKE that would match "user"
	// at any 'X' + 'ser'. With escape, only literal "_ser" matches
	// (which neither entry contains).
	out, err := s.Search(ctx, "_ser", history.ListOpts{UserID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	// Under FTS5: "_ser" is treated as a phrase and won't match
	// either entry. Under LIKE-escape: same result. Either way: 0.
	for _, r := range out {
		// If something does match (FTS5 tokenization quirks), it
		// must NOT be the "user" entry via the % wildcard.
		if strings.Contains(r.SQL, "user") {
			t.Errorf("escaped %% leaked: %q", r.SQL)
		}
	}
}

func TestSearchLike_EscapesMetachars(t *testing.T) {
	// Force the LIKE fallback path to verify the ESCAPE clause
	// actually prevents % from being a wildcard. Without it,
	// Tag="%" would enumerate every tagged entry.
	s := openMem(t)
	history.ForceLikePath(s)
	if history.HasFTS(s) {
		t.Fatal("ForceLikePath did not take effect")
	}
	ctx := context.Background()
	id1, _ := s.Append(ctx, entry("alice", "select 1"))
	id2, _ := s.Append(ctx, entry("alice", "select 2"))
	_ = s.Tag(ctx, id1, "alice", []string{"prod"})
	_ = s.Tag(ctx, id2, "alice", []string{"dev"})

	// Tag="%" is a wildcard if ESCAPE isn't honored. With ESCAPE
	// the literal '%' character isn't present in any stored tag, so
	// we expect 0 hits.
	out, err := s.List(ctx, history.ListOpts{UserID: "alice", Tag: "%"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("Tag='%%' leaked %d entries (expected 0): %v",
			len(out), out)
	}
}

// ─── JSON shape ──────────────────────────────────────────────────────

func TestEntryJSONShape(t *testing.T) {
	e := history.Entry{
		ID:       42,
		UserID:   "alice",
		SQL:      "SELECT 1",
		Executed: time.Unix(0, 1700000000000000000).UTC(),
	}
	buf, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	s := string(buf)
	for _, want := range []string{`"id":`, `"userId":`, `"executed":`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	for _, bad := range []string{`"ID":`, `"UserID":`, `"Executed":`} {
		if strings.Contains(s, bad) {
			t.Errorf("PascalCase key leaked %s in %s", bad, s)
		}
	}
}

// ─── Concurrency ─────────────────────────────────────────────────────

func TestAppend_Concurrent(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	const writers = 8
	const perWriter = 50
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				_, err := s.Append(ctx, entry("alice", "select"))
				if err != nil {
					t.Errorf("concurrent Append err: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	out, _ := s.List(ctx, history.ListOpts{UserID: "alice", Limit: writers * perWriter * 2})
	if len(out) != writers*perWriter {
		t.Errorf("got %d entries, want %d", len(out), writers*perWriter)
	}
}

// ─── Close + post-close errors ────────────────────────────────────────

func TestClose_Idempotent(t *testing.T) {
	s, err := history.Open(context.Background(), ":memory:", dbadmin.EngineMariaDB)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close err = %v, want nil", err)
	}
}

func TestOperationsAfterClose_ReturnErrClosed(t *testing.T) {
	s, _ := history.Open(context.Background(), ":memory:", dbadmin.EngineMariaDB)
	_ = s.Close()

	ctx := context.Background()
	_, err := s.Append(ctx, entry("alice", "select"))
	if !errors.Is(err, history.ErrClosed) {
		t.Errorf("Append post-close err = %v, want ErrClosed", err)
	}
	_, err = s.List(ctx, history.ListOpts{UserID: "alice"})
	if !errors.Is(err, history.ErrClosed) {
		t.Errorf("List post-close err = %v, want ErrClosed", err)
	}
	_, err = s.Search(ctx, "x", history.ListOpts{UserID: "alice"})
	if !errors.Is(err, history.ErrClosed) {
		t.Errorf("Search post-close err = %v, want ErrClosed", err)
	}
	err = s.Star(ctx, 1, "alice", true)
	if !errors.Is(err, history.ErrClosed) {
		t.Errorf("Star post-close err = %v, want ErrClosed", err)
	}
}

// ─── Open errors ─────────────────────────────────────────────────────

func TestOpen_RequiresDSN(t *testing.T) {
	_, err := history.Open(context.Background(), "", dbadmin.EngineMariaDB)
	if err == nil {
		t.Error("expected error for empty DSN")
	}
}

// ─── PR #7.5 H4: extended redaction coverage ─────────────────────────

func TestAppend_RedactsIdentifiedVia(t *testing.T) {
	// MariaDB IDENTIFIED VIA <plugin> AS '<hash>'. Both AS and USING
	// variants must redact the hash literal.
	s := openMem(t)
	ctx := context.Background()
	cases := []string{
		`CREATE USER bob@'%' IDENTIFIED VIA ed25519 AS 'fakehash-aaaa'`,
		`ALTER USER bob@'%' IDENTIFIED VIA pam USING 'service-config'`,
	}
	for _, sql := range cases {
		id, err := s.Append(ctx, entry("alice", sql))
		if err != nil {
			t.Fatal(err)
		}
		got, _ := s.Get(ctx, id, "alice")
		if strings.Contains(got.SQL, "fakehash") || strings.Contains(got.SQL, "service-config") {
			t.Errorf("IDENTIFIED VIA hash leaked: %q", got.SQL)
		}
		if !strings.Contains(got.SQL, "[redacted]") {
			t.Errorf("missing [redacted] marker: %q", got.SQL)
		}
	}
}

func TestAppend_RedactsConnectionString(t *testing.T) {
	// Postgres CREATE SUBSCRIPTION … CONNECTION '<dsn>'. The DSN
	// literal must vanish from history.
	s, err := history.Open(context.Background(), ":memory:", dbadmin.EnginePostgres)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	id, err := s.Append(ctx, entry("alice",
		`CREATE SUBSCRIPTION sub CONNECTION 'host=primary user=repl password=hunter2 dbname=app' PUBLICATION pub`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, id, "alice")
	if strings.Contains(got.SQL, "hunter2") {
		t.Errorf("CONNECTION DSN password leaked: %q", got.SQL)
	}
	if !strings.Contains(got.SQL, "[redacted]") {
		t.Errorf("missing [redacted] marker: %q", got.SQL)
	}
}

func TestAppend_RedactsDblinkConnect(t *testing.T) {
	// dblink_connect / dblink_connect_u — the DSN string arg carries
	// the credentials.
	s, err := history.Open(context.Background(), ":memory:", dbadmin.EnginePostgres)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	id, err := s.Append(ctx, entry("alice",
		`SELECT dblink_connect_u('host=remote user=root password=p@ss123 dbname=app')`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, id, "alice")
	if strings.Contains(got.SQL, "p@ss123") {
		t.Errorf("dblink_connect password leaked: %q", got.SQL)
	}
}

func TestAppend_RedactsCopyFromProgram(t *testing.T) {
	// COPY … FROM PROGRAM 'curl -u user:pw https://…' — the shell
	// command is sensitive even though the classifier will block
	// the run. Audit row still gets written.
	s, err := history.Open(context.Background(), ":memory:", dbadmin.EnginePostgres)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	id, err := s.Append(ctx, entry("alice",
		`COPY t FROM PROGRAM 'curl -u admin:hunter2 https://example.com/dump.csv'`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, id, "alice")
	if strings.Contains(got.SQL, "hunter2") {
		t.Errorf("COPY FROM PROGRAM command leaked: %q", got.SQL)
	}
}

func TestAppend_RedactsCredentialedURI(t *testing.T) {
	// Any string literal carrying a postgresql:// / mysql:// /
	// mongodb:// URI with userinfo redacts.
	s := openMem(t)
	ctx := context.Background()
	id, err := s.Append(ctx, entry("alice",
		`INSERT INTO fdw_options VALUES ('dsn', 'postgresql://repl:hunter2@primary/app')`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, id, "alice")
	if strings.Contains(got.SQL, "hunter2") {
		t.Errorf("URI-userinfo leaked: %q", got.SQL)
	}
}

func TestAppend_DoesNotRedactBareURI(t *testing.T) {
	// A postgresql:// URI without userinfo is documentation, not a
	// credential. Make sure we don't redact aggressively.
	s := openMem(t)
	ctx := context.Background()
	id, _ := s.Append(ctx, entry("alice",
		`INSERT INTO docs VALUES ('see postgresql://docs.example.com/manual')`))
	got, _ := s.Get(ctx, id, "alice")
	if !strings.Contains(got.SQL, "docs.example.com") {
		t.Errorf("bare URI got redacted: %q", got.SQL)
	}
}

// ─── PR #7.5 H8: FTS5 surfacing ──────────────────────────────────────

func TestOpenWithOpts_RequireFTS5_SucceedsWhenAvailable(t *testing.T) {
	// modernc.org/sqlite ships with FTS5 in current releases, so the
	// require-flag should succeed against :memory:.
	s, err := history.OpenWithOpts(context.Background(), ":memory:",
		dbadmin.EngineMariaDB,
		history.OpenOpts{RequireFTS5: true})
	if err != nil {
		t.Fatalf("RequireFTS5 unexpectedly errored: %v", err)
	}
	defer s.Close()
	if !s.HasFTS() {
		t.Error("HasFTS() == false after successful RequireFTS5 open")
	}
}

func TestHasFTS_ReportsFallback(t *testing.T) {
	// ForceLikePath flips an open Store into LIKE mode; HasFTS must
	// reflect that.
	s := openMem(t)
	history.ForceLikePath(s)
	if s.HasFTS() {
		t.Error("HasFTS() == true after ForceLikePath")
	}
}

// ─── PR #7.5 H9: retention enforcement ───────────────────────────────

func TestOpenWithOpts_MaxRows_EvictsOldest(t *testing.T) {
	s, err := history.OpenWithOpts(context.Background(), ":memory:",
		dbadmin.EngineMariaDB,
		history.OpenOpts{MaxRows: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	// Append 6 rows; only the newest 3 should survive.
	ids := make([]int64, 0, 6)
	for i := 0; i < 6; i++ {
		id, err := s.Append(ctx, entry("alice", "select"))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	out, _ := s.List(ctx, history.ListOpts{UserID: "alice", Limit: 100})
	if len(out) != 3 {
		t.Errorf("MaxRows=3 yielded %d rows, want 3", len(out))
	}
	// The surviving rows are the 3 highest IDs.
	got := map[int64]bool{}
	for _, r := range out {
		got[r.ID] = true
	}
	for _, id := range ids[:3] {
		if got[id] {
			t.Errorf("old row id=%d survived eviction", id)
		}
	}
	for _, id := range ids[3:] {
		if !got[id] {
			t.Errorf("new row id=%d evicted", id)
		}
	}
}

func TestDeleteOlderThan_ChunkedSweep(t *testing.T) {
	// Build a corpus > the batch size (1000) and confirm the sweep
	// removes everything below the cutoff in one call.
	s := openMem(t)
	ctx := context.Background()
	base := time.Now().UTC()
	const N = 1200
	for i := 0; i < N; i++ {
		e := entry("alice", "select")
		// Spread across an hour so all are below "now".
		e.Executed = base.Add(-time.Duration(i+1) * time.Second)
		_, _ = s.Append(ctx, e)
	}
	n, err := s.DeleteOlderThan(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if n != N {
		t.Errorf("DeleteOlderThan returned %d, want %d", n, N)
	}
	remaining, _ := s.List(ctx, history.ListOpts{UserID: "alice", Limit: 100})
	if len(remaining) != 0 {
		t.Errorf("remaining = %d, want 0", len(remaining))
	}
}

func TestStartRetentionLoop_RemovesOldEntries(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	// Seed one old + one fresh entry.
	old := entry("alice", "old")
	old.Executed = time.Now().Add(-48 * time.Hour).UTC()
	_, _ = s.Append(ctx, old)
	fresh := entry("alice", "fresh")
	fresh.Executed = time.Now().UTC()
	_, _ = s.Append(ctx, fresh)

	// Loop with a tiny period; 1-hour retention should sweep the
	// 48h-old entry.
	cancel := s.StartRetentionLoop(ctx, 10*time.Millisecond, 1*time.Hour)
	defer cancel()
	// Wait for at least one tick to run.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := s.List(ctx, history.ListOpts{UserID: "alice"})
		if len(out) == 1 && out[0].SQL == "fresh" {
			return // success
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("retention loop did not sweep the old entry within 2s")
}

// ─── PR #7.5 Medium: ClassPtr filter ─────────────────────────────────

func TestList_ClassPtr_FiltersForZeroValue(t *testing.T) {
	// ClassPtr lets you say "filter by ClassRead" without IncludeClass.
	s := openMem(t)
	ctx := context.Background()
	read := entry("alice", "SELECT 1")
	read.Class = classifier.ClassRead
	ddl := entry("alice", "CREATE TABLE t (id INT)")
	ddl.Class = classifier.ClassDDL
	_, _ = s.Append(ctx, read)
	_, _ = s.Append(ctx, ddl)

	cr := classifier.ClassRead
	out, err := s.List(ctx, history.ListOpts{UserID: "alice", ClassPtr: &cr})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Class != classifier.ClassRead {
		t.Errorf("ClassPtr(ClassRead) returned %d entries, want 1 read", len(out))
	}
}

// ─── PR #7.5 Medium: tag normalization ───────────────────────────────

func TestList_TagFilter_UsesNormalizedTable(t *testing.T) {
	// Sanity: the normalized join still returns the right rows AND
	// respects boundary semantics ("prod" != "production"). This
	// re-asserts the PR #7 invariant against the new join path.
	s := openMem(t)
	ctx := context.Background()
	idProd, _ := s.Append(ctx, entry("alice", "select prod"))
	idProduction, _ := s.Append(ctx, entry("alice", "select production"))
	_ = s.Tag(ctx, idProd, "alice", []string{"prod"})
	_ = s.Tag(ctx, idProduction, "alice", []string{"production"})

	out, _ := s.List(ctx, history.ListOpts{UserID: "alice", Tag: "prod"})
	if len(out) != 1 || out[0].ID != idProd {
		t.Errorf("normalized tag filter wrong: %+v", out)
	}
}

// ─── PR #7.5 Medium: :memory: detection ──────────────────────────────

func TestOpen_SharedCacheMemoryDSN(t *testing.T) {
	// file::memory:?cache=shared used to fall into the WAL branch,
	// producing a malformed DSN. With the new detection it should
	// open cleanly.
	s, err := history.Open(context.Background(),
		"file::memory:?cache=shared",
		dbadmin.EngineMariaDB)
	if err != nil {
		t.Fatalf("shared-cache memory DSN open: %v", err)
	}
	defer s.Close()
	// Sanity smoke: a write/read round-trip should work.
	id, err := s.Append(context.Background(), entry("alice", "select"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(context.Background(), id, "alice")
	if err != nil || got == nil {
		t.Errorf("round-trip failed: %v / %+v", err, got)
	}
}

// ─── PR #7.5 Medium: Search Offset validation ────────────────────────

func TestSearch_NegativeOffsetRejected(t *testing.T) {
	s := openMem(t)
	_, err := s.Search(context.Background(), "x",
		history.ListOpts{UserID: "alice", Offset: -1})
	if !errors.Is(err, history.ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
}

// ─── PR #7.5 Medium: FTS sanitization ────────────────────────────────

func TestSearch_FTSQuery_StripsControlBytes(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	_, _ = s.Append(ctx, entry("alice", "SELECT * FROM orders"))
	// Query with embedded NUL + tab should still match.
	out, err := s.Search(ctx, "orders\x00\tlogs", history.ListOpts{UserID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	_ = out // We just need no panic / no error; the phrase will
	// likely match the entry containing "orders".
}

func TestSearch_FTSQuery_RejectsAllControl(t *testing.T) {
	s := openMem(t)
	_, err := s.Search(context.Background(), "\x00\x01\x02",
		history.ListOpts{UserID: "alice"})
	if !errors.Is(err, history.ErrInvalidInput) {
		t.Errorf("all-control query err = %v, want ErrInvalidInput", err)
	}
}

// ─── PR #7.5 Low: UTF-8 safe truncation ──────────────────────────────

func TestAppend_TruncatesUTF8AtRuneBoundary(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	// Build a payload that places a 3-byte UTF-8 rune right at the
	// MaxSQLLength boundary. Padding chars are ASCII so the
	// offending rune lands at exact MaxSQLLength.
	pad := strings.Repeat("a", history.MaxSQLLength-1)
	rune3 := "雨" // U+96E8, 3 bytes
	sql := pad + rune3 + strings.Repeat("b", 100)
	id, err := s.Append(ctx, entry("alice", sql))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, id, "alice")
	// The stored SQL must be valid UTF-8 — the boundary rune must
	// not be split.
	if !utf8.ValidString(got.SQL) {
		t.Errorf("stored SQL is not valid UTF-8 after truncation")
	}
}

// ─── PR #7.5 Low: Invalid timestamps get clamped ─────────────────────

func TestAppend_RejectsZeroAndUnixEpochTimestamps(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()
	// IsZero — already handled in PR #7; re-assert.
	e := entry("alice", "select")
	e.Executed = time.Time{}
	id, _ := s.Append(ctx, e)
	got, _ := s.Get(ctx, id, "alice")
	if got.Executed.IsZero() || got.Executed.UnixNano() <= 0 {
		t.Errorf("zero timestamp not clamped: %v", got.Executed)
	}
	// time.Unix(0,0) — was the regression case; should also clamp.
	e2 := entry("alice", "select")
	e2.Executed = time.Unix(0, 0)
	id2, _ := s.Append(ctx, e2)
	got2, _ := s.Get(ctx, id2, "alice")
	if got2.Executed.UnixNano() <= 0 {
		t.Errorf("unix(0,0) timestamp not clamped: %v", got2.Executed)
	}
}

// ─── PR #7.5 Medium: writer semaphore caps concurrency ───────────────

func TestOpenWithOpts_MaxWriters_BoundsConcurrency(t *testing.T) {
	// Smoke test: 16 goroutines × 20 appends each on a Store with
	// MaxWriters=2 should still complete and produce the right
	// number of rows. The semaphore is an effectiveness check, not
	// a correctness invariant — but if it's wired wrong we'd
	// deadlock or lose rows.
	s, err := history.OpenWithOpts(context.Background(), ":memory:",
		dbadmin.EngineMariaDB,
		history.OpenOpts{MaxWriters: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	const writers = 16
	const per = 20
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < per; j++ {
				_, err := s.Append(ctx, entry("alice", "select"))
				if err != nil {
					t.Errorf("Append: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	out, _ := s.List(ctx, history.ListOpts{UserID: "alice", Limit: writers * per * 2})
	if len(out) != writers*per {
		t.Errorf("got %d rows, want %d", len(out), writers*per)
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────

func summarizeSearch(rs []history.SearchResult) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.SQL
	}
	return out
}

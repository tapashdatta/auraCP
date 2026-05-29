package history

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/classifier"
)

// sqliteStore is the default Store implementation, backed by a single
// SQLite database. Works against:
//
//   - A panel-shared file (integrated mode points it at the panel's
//     existing /etc/auracp/aura.db).
//   - A dedicated file (standalone mode uses /var/lib/aura-db/history.db).
//   - ":memory:" (tests).
//
// The schema is created on first open if absent; subsequent opens are
// no-ops. Schema migrations (PR #7.5) will use a version table; for now
// the schema is v1 and any column change is a manual migration.
//
// Unexported to keep consumers on the Store interface (Open returns
// Store). Same SDK-stability stance as the explain package.
type sqliteStore struct {
	db     *sql.DB
	dsn    string
	closed atomic.Bool

	// defaultEngine is recorded so Append's redaction picks the right
	// dialect when the per-Entry Engine isn't set. Append rejects
	// Entries where both Entry.Engine and defaultEngine are zero.
	defaultEngine dbadmin.EngineKind

	// hasFTS is set after init when the SQLite build supports FTS5.
	// Search falls back to LIKE when false.
	hasFTS bool

	// mu serializes schema-init + close. Reads and Append do not
	// hold this; SQLite's busy-timeout + WAL handles concurrency.
	mu sync.Mutex
}

// Open creates / opens a SQLite-backed Store. dsn is a path or
// ":memory:". defaultEngine is the dialect to use for SQL redaction
// when the caller doesn't override via Entry.Engine. Pass a real
// engine — EngineUnknown is rejected by Append.
func Open(ctx context.Context, dsn string, defaultEngine dbadmin.EngineKind) (Store, error) {
	if dsn == "" {
		return nil, fmt.Errorf("history: dsn required")
	}

	// modernc.org/sqlite accepts the standard SQLite DSN. We append
	// WAL + busy_timeout so concurrent panel operators don't
	// serialize entirely on a single writer.
	pragmaSuffix := ""
	if !strings.Contains(dsn, "_journal_mode") && dsn != ":memory:" {
		// In-memory DBs can't use WAL but file DBs benefit.
		pragmaSuffix = "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	} else if dsn == ":memory:" {
		pragmaSuffix = "?_pragma=busy_timeout(5000)"
	}

	db, err := sql.Open("sqlite", dsn+pragmaSuffix)
	if err != nil {
		return nil, fmt.Errorf("history: sql.Open: %w", err)
	}
	// One writer at a time is the SQLite sweet spot. For :memory:
	// databases we pin to a single connection — modernc.org/sqlite
	// creates a fresh in-memory DB per connection unless you use a
	// shared-cache URI, so a connection pool would give each goroutine
	// its own empty DB and writes would never converge.
	if dsn == ":memory:" {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	} else {
		db.SetMaxOpenConns(4)
		db.SetMaxIdleConns(2)
	}
	db.SetConnMaxIdleTime(5 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("history: ping: %w", err)
	}

	s := &sqliteStore{
		db:            db,
		dsn:           dsn,
		defaultEngine: defaultEngine,
	}
	if err := s.initSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// initSchema creates the tables + FTS5 virtual table on first open.
// Idempotent; later calls do nothing.
func (s *sqliteStore) initSchema(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	const tableSQL = `
		CREATE TABLE IF NOT EXISTS entries (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id       TEXT NOT NULL,
			connection_id TEXT NOT NULL,
			sql           TEXT NOT NULL,
			class         INTEGER NOT NULL DEFAULT 0,
			tags          TEXT NOT NULL DEFAULT '',
			starred       INTEGER NOT NULL DEFAULT 0,
			duration_ms   INTEGER NOT NULL DEFAULT 0,
			rows_returned INTEGER NOT NULL DEFAULT 0,
			error         TEXT NOT NULL DEFAULT '',
			executed      INTEGER NOT NULL,
			engine        INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_entries_user_executed ON entries(user_id, executed DESC);
		CREATE INDEX IF NOT EXISTS idx_entries_user_conn ON entries(user_id, connection_id);
		CREATE INDEX IF NOT EXISTS idx_entries_starred ON entries(user_id, starred, executed DESC) WHERE starred = 1;
	`
	if _, err := s.db.ExecContext(ctx, tableSQL); err != nil {
		return fmt.Errorf("history: init schema: %w", err)
	}

	// Probe for FTS5 support. modernc.org/sqlite builds with FTS5 by
	// default in current releases, but we're defensive: a CREATE
	// VIRTUAL TABLE failure makes us fall back to LIKE.
	const ftsSQL = `
		CREATE VIRTUAL TABLE IF NOT EXISTS entries_fts USING fts5(
			sql, tags,
			content='entries', content_rowid='id',
			tokenize='porter unicode61'
		);
		CREATE TRIGGER IF NOT EXISTS entries_ai AFTER INSERT ON entries BEGIN
			INSERT INTO entries_fts(rowid, sql, tags) VALUES (new.id, new.sql, new.tags);
		END;
		CREATE TRIGGER IF NOT EXISTS entries_ad AFTER DELETE ON entries BEGIN
			INSERT INTO entries_fts(entries_fts, rowid, sql, tags) VALUES ('delete', old.id, old.sql, old.tags);
		END;
		CREATE TRIGGER IF NOT EXISTS entries_au AFTER UPDATE ON entries BEGIN
			INSERT INTO entries_fts(entries_fts, rowid, sql, tags) VALUES ('delete', old.id, old.sql, old.tags);
			INSERT INTO entries_fts(rowid, sql, tags) VALUES (new.id, new.sql, new.tags);
		END;
	`
	if _, err := s.db.ExecContext(ctx, ftsSQL); err != nil {
		// FTS5 not available — leave hasFTS=false. Search will
		// degrade to LIKE without breaking the API.
		return nil
	}
	s.hasFTS = true
	return nil
}

// Append records one entry. Auto-redacts SQL + Error.
func (s *sqliteStore) Append(ctx context.Context, e Entry) (int64, error) {
	if s.closed.Load() {
		return 0, ErrClosed
	}
	if e.UserID == "" {
		return 0, fmt.Errorf("%w: empty UserID", ErrInvalidInput)
	}
	if e.SQL == "" {
		return 0, fmt.Errorf("%w: empty SQL", ErrInvalidInput)
	}
	if err := validateTags(e.Tags); err != nil {
		return 0, fmt.Errorf("%w: %s", ErrInvalidInput, err.Error())
	}

	// Resolve the engine used for redaction. Per-Entry wins; falls
	// back to store-wide default. Both zero is a programmer error —
	// reject loudly rather than silently picking the wrong dialect
	// (e.g., parsing a Postgres CREATE ROLE statement under MySQL
	// rules silently no-ops on dollar-quoted passwords).
	engine := e.Engine
	if engine == dbadmin.EngineUnknown {
		engine = s.defaultEngine
	}
	if engine == dbadmin.EngineUnknown {
		return 0, fmt.Errorf("%w: engine unknown (set Entry.Engine or Open with non-zero defaultEngine)", ErrInvalidInput)
	}
	e.Engine = engine

	if e.Executed.IsZero() {
		e.Executed = time.Now().UTC()
	}

	redactedSQL := redactSQL(e.SQL, engine)
	// Driver errors routinely echo the offending statement (pgx
	// prints "ERROR: syntax error at or near ..." with the source
	// text inline; mysql does the same). Pass the error text
	// through the same redactor.
	redactedError := e.Error
	if redactedError != "" {
		redactedError = redactSQL(redactedError, engine)
	}
	tagsStr := serializeTags(e.Tags)
	starredInt := 0
	if e.Starred {
		starredInt = 1
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO entries (user_id, connection_id, sql, class, tags, starred, duration_ms, rows_returned, error, executed, engine)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.UserID, string(e.ConnectionID), redactedSQL,
		int(e.Class), tagsStr, starredInt,
		e.DurationMS, e.RowsReturned, redactedError,
		e.Executed.UnixNano(), int(engine),
	)
	if err != nil {
		return 0, fmt.Errorf("history: insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("history: last insert id: %w", err)
	}
	return id, nil
}

// Get fetches a single entry.
func (s *sqliteStore) Get(ctx context.Context, id int64, userID string) (*Entry, error) {
	if s.closed.Load() {
		return nil, ErrClosed
	}
	if userID == "" {
		return nil, fmt.Errorf("%w: empty UserID", ErrInvalidInput)
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, user_id, connection_id, sql, class, tags, starred, duration_ms, rows_returned, error, executed, engine
		FROM entries WHERE id = ? AND user_id = ?`, id, userID)
	e, err := scanEntry(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

// List enumerates entries matching opts.
func (s *sqliteStore) List(ctx context.Context, opts ListOpts) ([]Entry, error) {
	if s.closed.Load() {
		return nil, ErrClosed
	}
	if opts.UserID == "" {
		return nil, fmt.Errorf("%w: empty UserID", ErrInvalidInput)
	}
	if opts.Offset < 0 {
		return nil, fmt.Errorf("%w: negative Offset", ErrInvalidInput)
	}
	limit := clampLimit(opts.Limit)

	var b strings.Builder
	b.WriteString(`SELECT id, user_id, connection_id, sql, class, tags, starred, duration_ms, rows_returned, error, executed, engine
		FROM entries`)
	args := []any{}
	whereParts := []string{"user_id = ?"}
	args = append(args, opts.UserID)

	if opts.ConnectionID != "" {
		whereParts = append(whereParts, "connection_id = ?")
		args = append(args, string(opts.ConnectionID))
	}
	if opts.OnlyStarred {
		whereParts = append(whereParts, "starred = 1")
	}
	if opts.Tag != "" {
		// Tags are stored as ",tag1,tag2," (with leading + trailing
		// commas as fences). LIKE match on "%,tag,%" so "prod"
		// doesn't match "production" and so on. ESCAPE '\' is
		// required because escapeLike emits backslash-prefixed
		// metacharacters — SQLite's LIKE doesn't recognize
		// backslash as an escape unless an ESCAPE clause is set.
		whereParts = append(whereParts, `tags LIKE ? ESCAPE '\'`)
		args = append(args, "%,"+escapeLike(opts.Tag)+",%")
	}
	if opts.IncludeClass {
		whereParts = append(whereParts, "class = ?")
		args = append(args, int(opts.Class))
	}
	if !opts.Since.IsZero() {
		whereParts = append(whereParts, "executed >= ?")
		args = append(args, opts.Since.UnixNano())
	}
	if !opts.Until.IsZero() {
		whereParts = append(whereParts, "executed < ?")
		args = append(args, opts.Until.UnixNano())
	}
	b.WriteString(" WHERE ")
	b.WriteString(strings.Join(whereParts, " AND "))
	b.WriteString(" ORDER BY executed DESC, id DESC LIMIT ? OFFSET ?")
	args = append(args, limit, opts.Offset)

	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("history: list: %w", err)
	}
	defer rows.Close()

	out := make([]Entry, 0, limit)
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// Search returns entries matching the query string.
//
// With FTS5 available: ranks by bm25() in entries_fts; falls back to
// LIKE when FTS5 isn't compiled in. Same ListOpts filters.
func (s *sqliteStore) Search(ctx context.Context, query string, opts ListOpts) ([]SearchResult, error) {
	if s.closed.Load() {
		return nil, ErrClosed
	}
	if opts.UserID == "" {
		return nil, fmt.Errorf("%w: empty UserID", ErrInvalidInput)
	}
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("%w: empty search query", ErrInvalidInput)
	}
	limit := clampLimit(opts.Limit)

	if s.hasFTS {
		return s.searchFTS(ctx, query, opts, limit)
	}
	return s.searchLike(ctx, query, opts, limit)
}

func (s *sqliteStore) searchFTS(ctx context.Context, query string, opts ListOpts, limit int) ([]SearchResult, error) {
	// Sanitize: FTS5 queries can contain operators; for the basic
	// search UX we treat the input as a phrase. Replace " with
	// nothing (avoids FTS5 quote-parsing edge cases), then wrap in
	// quotes for phrase search.
	clean := strings.ReplaceAll(query, `"`, "")
	ftsQuery := `"` + clean + `"`

	var b strings.Builder
	b.WriteString(`
		SELECT e.id, e.user_id, e.connection_id, e.sql, e.class, e.tags, e.starred,
		       e.duration_ms, e.rows_returned, e.error, e.executed, e.engine,
		       bm25(entries_fts) AS score
		FROM entries_fts
		JOIN entries e ON e.id = entries_fts.rowid
		WHERE entries_fts MATCH ? AND e.user_id = ?`)
	args := []any{ftsQuery, opts.UserID}

	if opts.ConnectionID != "" {
		b.WriteString(" AND e.connection_id = ?")
		args = append(args, string(opts.ConnectionID))
	}
	if opts.OnlyStarred {
		b.WriteString(" AND e.starred = 1")
	}
	if opts.Tag != "" {
		// Same fenced-tag pattern as List; needs ESCAPE because
		// escapeLike emits backslash-prefixed metacharacters.
		b.WriteString(` AND e.tags LIKE ? ESCAPE '\'`)
		args = append(args, "%,"+escapeLike(opts.Tag)+",%")
	}
	if opts.IncludeClass {
		b.WriteString(" AND e.class = ?")
		args = append(args, int(opts.Class))
	}
	if !opts.Since.IsZero() {
		b.WriteString(" AND e.executed >= ?")
		args = append(args, opts.Since.UnixNano())
	}
	if !opts.Until.IsZero() {
		b.WriteString(" AND e.executed < ?")
		args = append(args, opts.Until.UnixNano())
	}
	// Lower bm25 score = better match. We surface ascending.
	b.WriteString(" ORDER BY score ASC, e.executed DESC LIMIT ? OFFSET ?")
	args = append(args, limit, opts.Offset)

	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("history: fts search: %w", err)
	}
	defer rows.Close()

	out := make([]SearchResult, 0, limit)
	for rows.Next() {
		r, err := scanSearchResult(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *sqliteStore) searchLike(ctx context.Context, query string, opts ListOpts, limit int) ([]SearchResult, error) {
	var b strings.Builder
	b.WriteString(`
		SELECT id, user_id, connection_id, sql, class, tags, starred,
		       duration_ms, rows_returned, error, executed, engine,
		       1.0 AS score
		FROM entries
		WHERE (sql LIKE ? ESCAPE '\' OR tags LIKE ? ESCAPE '\') AND user_id = ?`)
	pat := "%" + escapeLike(query) + "%"
	args := []any{pat, pat, opts.UserID}

	if opts.ConnectionID != "" {
		b.WriteString(" AND connection_id = ?")
		args = append(args, string(opts.ConnectionID))
	}
	if opts.OnlyStarred {
		b.WriteString(" AND starred = 1")
	}
	if opts.Tag != "" {
		// Fenced match — same as List.
		b.WriteString(` AND tags LIKE ? ESCAPE '\'`)
		args = append(args, "%,"+escapeLike(opts.Tag)+",%")
	}
	if opts.IncludeClass {
		b.WriteString(" AND class = ?")
		args = append(args, int(opts.Class))
	}
	if !opts.Since.IsZero() {
		b.WriteString(" AND executed >= ?")
		args = append(args, opts.Since.UnixNano())
	}
	if !opts.Until.IsZero() {
		b.WriteString(" AND executed < ?")
		args = append(args, opts.Until.UnixNano())
	}
	b.WriteString(" ORDER BY executed DESC LIMIT ? OFFSET ?")
	args = append(args, limit, opts.Offset)

	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("history: like search: %w", err)
	}
	defer rows.Close()

	out := make([]SearchResult, 0, limit)
	for rows.Next() {
		r, err := scanSearchResult(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// Star pins / unpins an entry.
func (s *sqliteStore) Star(ctx context.Context, id int64, userID string, starred bool) error {
	if s.closed.Load() {
		return ErrClosed
	}
	if userID == "" {
		return fmt.Errorf("%w: empty UserID", ErrInvalidInput)
	}
	v := 0
	if starred {
		v = 1
	}
	return s.mutate(ctx, "UPDATE entries SET starred = ?", id, userID, v)
}

// Tag replaces the entry's tag set.
func (s *sqliteStore) Tag(ctx context.Context, id int64, userID string, tags []string) error {
	if s.closed.Load() {
		return ErrClosed
	}
	if userID == "" {
		return fmt.Errorf("%w: empty UserID", ErrInvalidInput)
	}
	if err := validateTags(tags); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidInput, err.Error())
	}
	return s.mutate(ctx, "UPDATE entries SET tags = ?", id, userID, serializeTags(tags))
}

// Delete removes one entry.
func (s *sqliteStore) Delete(ctx context.Context, id int64, userID string) error {
	if s.closed.Load() {
		return ErrClosed
	}
	if userID == "" {
		return fmt.Errorf("%w: empty UserID", ErrInvalidInput)
	}
	res, err := s.db.ExecContext(ctx, "DELETE FROM entries WHERE id = ? AND user_id = ?", id, userID)
	if err != nil {
		return fmt.Errorf("history: delete: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteOlderThan removes entries before the cutoff. Returns the count.
// Admin-scoped (no userID arg); the engine layer wraps it with admin
// authorization.
func (s *sqliteStore) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	if s.closed.Load() {
		return 0, ErrClosed
	}
	res, err := s.db.ExecContext(ctx, "DELETE FROM entries WHERE executed < ?", cutoff.UnixNano())
	if err != nil {
		return 0, fmt.Errorf("history: retention sweep: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// Close releases the SQLite handle. Idempotent.
func (s *sqliteStore) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Close()
}

// ─── Helpers ─────────────────────────────────────────────────────────

// mutate is the shared body of Star + Tag: a parameterized UPDATE that
// scopes by id + user_id (both required), then errors with
// ErrNotFound when no rows affected.
func (s *sqliteStore) mutate(ctx context.Context, setClause string, id int64, userID string, value any) error {
	res, err := s.db.ExecContext(ctx, setClause+" WHERE id = ? AND user_id = ?", value, id, userID)
	if err != nil {
		return fmt.Errorf("history: mutate: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// rowScanner is the common subset of *sql.Row and *sql.Rows that we
// scan into an Entry.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanEntry(row rowScanner) (*Entry, error) {
	var (
		e        Entry
		connID   string
		classInt int
		tagsStr  string
		starInt  int
		executed int64
		engineInt int
	)
	if err := row.Scan(
		&e.ID, &e.UserID, &connID, &e.SQL,
		&classInt, &tagsStr, &starInt,
		&e.DurationMS, &e.RowsReturned, &e.Error, &executed,
		&engineInt,
	); err != nil {
		return nil, err
	}
	e.ConnectionID = dbadmin.ConnectionID(connID)
	e.Class = classifier.QueryClass(classInt)
	e.Tags = deserializeTags(tagsStr)
	e.Starred = starInt == 1
	e.Executed = time.Unix(0, executed).UTC()
	e.Engine = dbadmin.EngineKind(engineInt)
	return &e, nil
}

func scanSearchResult(row rowScanner) (*SearchResult, error) {
	var (
		r        SearchResult
		connID   string
		classInt int
		tagsStr  string
		starInt  int
		executed int64
		engineInt int
	)
	if err := row.Scan(
		&r.ID, &r.UserID, &connID, &r.SQL,
		&classInt, &tagsStr, &starInt,
		&r.DurationMS, &r.RowsReturned, &r.Error, &executed,
		&engineInt, &r.Score,
	); err != nil {
		return nil, err
	}
	r.ConnectionID = dbadmin.ConnectionID(connID)
	r.Class = classifier.QueryClass(classInt)
	r.Tags = deserializeTags(tagsStr)
	r.Starred = starInt == 1
	r.Executed = time.Unix(0, executed).UTC()
	r.Engine = dbadmin.EngineKind(engineInt)
	return &r, nil
}

// serializeTags joins tags with "," for storage with leading + trailing
// commas as fences (",tag1,tag2," — empty list returns ""). The fences
// let the List/Search tag filter use the pattern "%,tag,%" so that
// "prod" doesn't match "production". Callers must pre-validate that no
// tag contains "," (validateTags); this function trusts its input.
func serializeTags(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	// De-dup while preserving order.
	seen := make(map[string]bool, len(tags))
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) == 0 {
		return ""
	}
	return "," + strings.Join(out, ",") + ","
}

func deserializeTags(s string) []string {
	if s == "" {
		return nil
	}
	// Strip the leading + trailing comma fences if present.
	s = strings.TrimPrefix(s, ",")
	s = strings.TrimSuffix(s, ",")
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// escapeLike escapes SQLite LIKE metacharacters in user-supplied
// search strings. SQLite's LIKE does NOT treat backslash as an escape
// unless the query explicitly adds ESCAPE '\' — every call site that
// uses LIKE with a pattern from escapeLike must include that clause.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "_", `\_`)
	return s
}

// Compile-time assertion that *sqliteStore satisfies Store.
var _ Store = (*sqliteStore)(nil)

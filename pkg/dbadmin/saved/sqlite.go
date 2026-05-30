package saved

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// sqliteStore is the default Store implementation, backed by a single
// SQLite database. Mirrors history/sqlite.go (WAL pragma, busy_timeout,
// bounded writer semaphore, prepared statements, in-memory pinning).
type sqliteStore struct {
	db     *sql.DB
	dsn    string
	closed atomic.Bool

	hasFTS bool

	busyTimeoutMS int
	maxOpenConns  int
	maxPerOwner   int

	mu       sync.Mutex
	writeSem chan struct{}

	// Prepared statements cached at init. Saved-query writes are
	// rarer than history Appends but the prepared cache still avoids
	// per-call parse for the hot list / read paths.
	stmtAppend *sql.Stmt
	stmtsOnce  sync.Once
}

// OpenOpts tunes Store behavior at Open time. Zero values mean "use
// the package defaults."
type OpenOpts struct {
	// RequireFTS5 makes Open fail when the underlying SQLite build
	// lacks FTS5 support. Without it the Store silently degrades to
	// LIKE-based Search.
	RequireFTS5 bool

	// BusyTimeoutMS is the SQLite busy_timeout pragma value applied
	// at Open. Default 5000ms.
	BusyTimeoutMS int

	// MaxOpenConns is the database/sql open-connection ceiling.
	// Default 4. Pinned to 1 for in-memory DBs.
	MaxOpenConns int

	// MaxPerOwner caps stored entries per (connection, owner). When
	// Create would push the count over the cap, the OLDEST entry for
	// the same (conn, owner) is deleted in the same transaction.
	// Default DefaultMaxPerOwner (256).
	MaxPerOwner int

	// MaxWriters bounds concurrent in-flight write calls. Default 8.
	// Zero disables the bound.
	MaxWriters int
}

func defaultOpenOpts() OpenOpts {
	return OpenOpts{
		BusyTimeoutMS: 5000,
		MaxOpenConns:  4,
		MaxPerOwner:   DefaultMaxPerOwner,
		MaxWriters:    8,
	}
}

// Open is the simple constructor. Equivalent to OpenWithOpts(ctx, dsn,
// OpenOpts{}).
func Open(ctx context.Context, dsn string) (Store, error) {
	return OpenWithOpts(ctx, dsn, OpenOpts{})
}

// OpenWithOpts is the option-bearing constructor. See OpenOpts.
func OpenWithOpts(ctx context.Context, dsn string, opts OpenOpts) (Store, error) {
	if dsn == "" {
		return nil, fmt.Errorf("saved: dsn required")
	}
	def := defaultOpenOpts()
	if opts.BusyTimeoutMS <= 0 {
		opts.BusyTimeoutMS = def.BusyTimeoutMS
	}
	if opts.MaxOpenConns <= 0 {
		opts.MaxOpenConns = def.MaxOpenConns
	}
	if opts.MaxPerOwner <= 0 {
		opts.MaxPerOwner = def.MaxPerOwner
	}
	if opts.MaxWriters < 0 {
		opts.MaxWriters = def.MaxWriters
	}

	memory := isMemoryDSN(dsn)
	pragmas := []string{
		fmt.Sprintf("busy_timeout(%d)", opts.BusyTimeoutMS),
	}
	if !memory {
		pragmas = append(pragmas, "journal_mode(WAL)", "foreign_keys(1)")
	}
	openDSN := buildDSN(dsn, pragmas)

	db, err := sql.Open("sqlite", openDSN)
	if err != nil {
		return nil, fmt.Errorf("saved: sql.Open: %w", err)
	}
	if memory {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	} else {
		db.SetMaxOpenConns(opts.MaxOpenConns)
		db.SetMaxIdleConns(opts.MaxOpenConns / 2)
	}
	db.SetConnMaxIdleTime(5 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("saved: ping: %w", err)
	}

	s := &sqliteStore{
		db:            db,
		dsn:           dsn,
		busyTimeoutMS: opts.BusyTimeoutMS,
		maxOpenConns:  opts.MaxOpenConns,
		maxPerOwner:   opts.MaxPerOwner,
	}
	if opts.MaxWriters > 0 {
		s.writeSem = make(chan struct{}, opts.MaxWriters)
	}
	if err := s.initSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if opts.RequireFTS5 && !s.hasFTS {
		_ = db.Close()
		return nil, fmt.Errorf("saved: FTS5 required but not available in this SQLite build")
	}
	if err := s.prepareStmts(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// initSchema creates the saved_queries table + FTS5 virtual table on
// first open. Idempotent.
func (s *sqliteStore) initSchema(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	const tableSQL = `
		CREATE TABLE IF NOT EXISTS saved_queries (
			id              TEXT PRIMARY KEY,
			connection_id   TEXT NOT NULL,
			owner_id        TEXT NOT NULL,
			name            TEXT NOT NULL,
			statement       TEXT NOT NULL,
			description     TEXT NOT NULL DEFAULT '',
			tags            TEXT NOT NULL DEFAULT '',
			starred         INTEGER NOT NULL DEFAULT 0,
			created_at      INTEGER NOT NULL,
			updated_at      INTEGER NOT NULL
		);
		-- Per-(connection, owner) name uniqueness. Two users can each
		-- own a snippet called "my-query" on the same connection.
		CREATE UNIQUE INDEX IF NOT EXISTS idx_saved_conn_owner_name
			ON saved_queries(connection_id, owner_id, name);
		-- Range scan for List ordering: starred DESC then updated_at DESC.
		CREATE INDEX IF NOT EXISTS idx_saved_conn_owner_updated
			ON saved_queries(connection_id, owner_id, updated_at DESC);
		-- Partial index for "show me my starred saves on this connection".
		CREATE INDEX IF NOT EXISTS idx_saved_conn_owner_starred
			ON saved_queries(connection_id, owner_id, updated_at DESC) WHERE starred = 1;
	`
	if _, err := s.db.ExecContext(ctx, tableSQL); err != nil {
		return fmt.Errorf("saved: init schema: %w", err)
	}

	// Probe FTS5 in its own statement.
	const ftsProbe = `
		CREATE VIRTUAL TABLE IF NOT EXISTS saved_queries_fts USING fts5(
			name, statement, description, tags,
			content='saved_queries', content_rowid='rowid',
			tokenize='porter unicode61'
		);
	`
	if _, err := s.db.ExecContext(ctx, ftsProbe); err != nil {
		// FTS5 not available; LIKE fallback remains.
		return nil
	}
	s.hasFTS = true

	const ftsTriggers = `
		CREATE TRIGGER IF NOT EXISTS saved_queries_ai AFTER INSERT ON saved_queries BEGIN
			INSERT INTO saved_queries_fts(rowid, name, statement, description, tags)
				VALUES (new.rowid, new.name, new.statement, new.description, new.tags);
		END;
		CREATE TRIGGER IF NOT EXISTS saved_queries_ad AFTER DELETE ON saved_queries BEGIN
			INSERT INTO saved_queries_fts(saved_queries_fts, rowid, name, statement, description, tags)
				VALUES ('delete', old.rowid, old.name, old.statement, old.description, old.tags);
		END;
		CREATE TRIGGER IF NOT EXISTS saved_queries_au AFTER UPDATE ON saved_queries BEGIN
			INSERT INTO saved_queries_fts(saved_queries_fts, rowid, name, statement, description, tags)
				VALUES ('delete', old.rowid, old.name, old.statement, old.description, old.tags);
			INSERT INTO saved_queries_fts(rowid, name, statement, description, tags)
				VALUES (new.rowid, new.name, new.statement, new.description, new.tags);
		END;
	`
	if _, err := s.db.ExecContext(ctx, ftsTriggers); err != nil {
		return fmt.Errorf("saved: install FTS triggers: %w", err)
	}
	return nil
}

func (s *sqliteStore) prepareStmts(ctx context.Context) error {
	var perr error
	s.stmtsOnce.Do(func() {
		stmtAppend, err := s.db.PrepareContext(ctx, `
			INSERT INTO saved_queries (id, connection_id, owner_id, name, statement, description, tags, starred, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			perr = fmt.Errorf("saved: prepare append: %w", err)
			return
		}
		s.stmtAppend = stmtAppend
	})
	return perr
}

func (s *sqliteStore) acquireWriter(ctx context.Context) (release func(), err error) {
	if s.writeSem == nil {
		return func() {}, nil
	}
	select {
	case s.writeSem <- struct{}{}:
		return func() { <-s.writeSem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Append inserts one saved query.
func (s *sqliteStore) Append(ctx context.Context, r Record) error {
	if s.closed.Load() {
		return ErrClosed
	}
	if r.ID == "" {
		return fmt.Errorf("%w: empty ID", ErrInvalidInput)
	}
	if r.ConnectionID == "" {
		return fmt.Errorf("%w: empty ConnectionID", ErrInvalidInput)
	}
	if r.OwnerID == "" {
		return fmt.Errorf("%w: empty OwnerID", ErrInvalidInput)
	}
	if r.Name == "" {
		return fmt.Errorf("%w: empty Name", ErrInvalidInput)
	}
	if r.Statement == "" {
		return fmt.Errorf("%w: empty Statement", ErrInvalidInput)
	}
	if err := validateTags(r.Tags); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidInput, err.Error())
	}

	if !isValidTimestamp(r.CreatedAt) {
		r.CreatedAt = time.Now().UTC()
	}
	if !isValidTimestamp(r.UpdatedAt) {
		r.UpdatedAt = r.CreatedAt
	}

	tagsStr := serializeTags(r.Tags)
	starredInt := 0
	if r.Starred {
		starredInt = 1
	}

	release, err := s.acquireWriter(ctx)
	if err != nil {
		return err
	}
	defer release()

	_, err = s.stmtAppend.ExecContext(ctx,
		r.ID, string(r.ConnectionID), r.OwnerID,
		r.Name, r.Statement, r.Description,
		tagsStr, starredInt,
		r.CreatedAt.UnixNano(), r.UpdatedAt.UnixNano(),
	)
	if err != nil {
		// SQLite returns a generic constraint message; the UNIQUE
		// index on (connection_id, owner_id, name) means the most
		// likely cause is a duplicate Name for this owner. Recognize
		// the constraint marker so callers can use errors.Is.
		if isUniqueConstraint(err) {
			return ErrConflict
		}
		return fmt.Errorf("saved: insert: %w", err)
	}

	// Per-(conn, owner) cap enforcement. Same pattern as
	// history.maxRows: count + LIMIT-style DELETE of the oldest rows
	// for the same scope. Best-effort — the row we just inserted is
	// already committed; momentary over-cap during a burst is fine.
	if s.maxPerOwner > 0 {
		if _, err := s.db.ExecContext(ctx, `
			DELETE FROM saved_queries WHERE id IN (
				SELECT id FROM saved_queries
				WHERE connection_id = ? AND owner_id = ?
				ORDER BY updated_at ASC, id ASC
				LIMIT MAX(0, (
					SELECT COUNT(*) FROM saved_queries
					WHERE connection_id = ? AND owner_id = ?
				) - ?)
			)`, string(r.ConnectionID), r.OwnerID, string(r.ConnectionID), r.OwnerID, s.maxPerOwner); err != nil {
			_ = err
		}
	}
	return nil
}

// Get fetches one record by id, scoped to (connID, ownerID).
func (s *sqliteStore) Get(ctx context.Context, connID dbadmin.ConnectionID, ownerID, id string) (*Record, error) {
	if s.closed.Load() {
		return nil, ErrClosed
	}
	if connID == "" {
		return nil, fmt.Errorf("%w: empty ConnectionID", ErrInvalidInput)
	}
	if ownerID == "" {
		return nil, fmt.Errorf("%w: empty OwnerID", ErrInvalidInput)
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, connection_id, owner_id, name, statement, description, tags, starred, created_at, updated_at
		FROM saved_queries WHERE id = ? AND connection_id = ? AND owner_id = ?`,
		id, string(connID), ownerID)
	r, err := scanRecord(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

// List enumerates records matching opts.
func (s *sqliteStore) List(ctx context.Context, opts ListOpts) ([]Record, error) {
	if s.closed.Load() {
		return nil, ErrClosed
	}
	if opts.ConnectionID == "" {
		return nil, fmt.Errorf("%w: empty ConnectionID", ErrInvalidInput)
	}
	if opts.OwnerID == "" {
		return nil, fmt.Errorf("%w: empty OwnerID", ErrInvalidInput)
	}
	if opts.Offset < 0 {
		return nil, fmt.Errorf("%w: negative Offset", ErrInvalidInput)
	}
	limit := clampLimit(opts.Limit)

	var b strings.Builder
	b.WriteString(`SELECT id, connection_id, owner_id, name, statement, description, tags, starred, created_at, updated_at
		FROM saved_queries WHERE connection_id = ? AND owner_id = ?`)
	args := []any{string(opts.ConnectionID), opts.OwnerID}
	if opts.StarOnly {
		b.WriteString(" AND starred = 1")
	}
	if opts.Tag != "" {
		// Tag filter uses the fenced-comma LIKE pattern so "prod"
		// doesn't match "production". Tags column always has the
		// leading/trailing comma fences when non-empty.
		b.WriteString(" AND tags LIKE ? ESCAPE '\\'")
		args = append(args, "%,"+escapeLike(opts.Tag)+",%")
	}
	// Ordering: starred first, then most-recently-updated, then id
	// for deterministic tiebreak.
	b.WriteString(" ORDER BY starred DESC, updated_at DESC, id DESC LIMIT ? OFFSET ?")
	args = append(args, limit, opts.Offset)

	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("saved: list: %w", err)
	}
	defer rows.Close()

	out := make([]Record, 0, limit)
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// MaxFTSQueryBytes caps the FTS5 query length.
const MaxFTSQueryBytes = 4096

func sanitizeFTSQuery(q string) string {
	var b strings.Builder
	b.Grow(len(q))
	for _, r := range q {
		switch {
		case r == '"':
			continue
		case r < 0x20, r == 0x7f:
			continue
		default:
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(out) > MaxFTSQueryBytes {
		out = out[:MaxFTSQueryBytes]
	}
	return out
}

// Search runs an FTS5-or-LIKE search.
func (s *sqliteStore) Search(ctx context.Context, query string, opts ListOpts) ([]SearchResult, error) {
	if s.closed.Load() {
		return nil, ErrClosed
	}
	if opts.ConnectionID == "" {
		return nil, fmt.Errorf("%w: empty ConnectionID", ErrInvalidInput)
	}
	if opts.OwnerID == "" {
		return nil, fmt.Errorf("%w: empty OwnerID", ErrInvalidInput)
	}
	if opts.Offset < 0 {
		return nil, fmt.Errorf("%w: negative Offset", ErrInvalidInput)
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
	clean := sanitizeFTSQuery(query)
	if clean == "" {
		return nil, fmt.Errorf("%w: empty search query (after sanitization)", ErrInvalidInput)
	}
	ftsQuery := `"` + clean + `"`

	var b strings.Builder
	b.WriteString(`
		SELECT e.id, e.connection_id, e.owner_id, e.name, e.statement, e.description, e.tags, e.starred,
		       e.created_at, e.updated_at, bm25(saved_queries_fts) AS score
		FROM saved_queries_fts
		JOIN saved_queries e ON e.rowid = saved_queries_fts.rowid
		WHERE saved_queries_fts MATCH ? AND e.connection_id = ? AND e.owner_id = ?`)
	args := []any{ftsQuery, string(opts.ConnectionID), opts.OwnerID}
	if opts.StarOnly {
		b.WriteString(" AND e.starred = 1")
	}
	if opts.Tag != "" {
		b.WriteString(" AND e.tags LIKE ? ESCAPE '\\'")
		args = append(args, "%,"+escapeLike(opts.Tag)+",%")
	}
	b.WriteString(" ORDER BY score ASC, e.updated_at DESC, e.id DESC LIMIT ? OFFSET ?")
	args = append(args, limit, opts.Offset)

	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("saved: fts search: %w", err)
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
		SELECT e.id, e.connection_id, e.owner_id, e.name, e.statement, e.description, e.tags, e.starred,
		       e.created_at, e.updated_at, 1.0 AS score
		FROM saved_queries e
		WHERE (e.name LIKE ? ESCAPE '\' OR e.statement LIKE ? ESCAPE '\' OR e.description LIKE ? ESCAPE '\' OR e.tags LIKE ? ESCAPE '\')
		  AND e.connection_id = ? AND e.owner_id = ?`)
	pat := "%" + escapeLike(query) + "%"
	args := []any{pat, pat, pat, pat, string(opts.ConnectionID), opts.OwnerID}
	if opts.StarOnly {
		b.WriteString(" AND e.starred = 1")
	}
	if opts.Tag != "" {
		b.WriteString(" AND e.tags LIKE ? ESCAPE '\\'")
		args = append(args, "%,"+escapeLike(opts.Tag)+",%")
	}
	b.WriteString(" ORDER BY e.updated_at DESC, e.id DESC LIMIT ? OFFSET ?")
	args = append(args, limit, opts.Offset)

	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("saved: like search: %w", err)
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

// Star pins / unpins a saved query.
func (s *sqliteStore) Star(ctx context.Context, connID dbadmin.ConnectionID, ownerID, id string, starred bool) error {
	if s.closed.Load() {
		return ErrClosed
	}
	if connID == "" {
		return fmt.Errorf("%w: empty ConnectionID", ErrInvalidInput)
	}
	if ownerID == "" {
		return fmt.Errorf("%w: empty OwnerID", ErrInvalidInput)
	}
	release, err := s.acquireWriter(ctx)
	if err != nil {
		return err
	}
	defer release()
	v := 0
	if starred {
		v = 1
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE saved_queries SET starred = ?, updated_at = ?
		 WHERE id = ? AND connection_id = ? AND owner_id = ?`,
		v, time.Now().UTC().UnixNano(), id, string(connID), ownerID)
	if err != nil {
		return fmt.Errorf("saved: star: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Tag replaces the tag set.
func (s *sqliteStore) Tag(ctx context.Context, connID dbadmin.ConnectionID, ownerID, id string, tags []string) error {
	if s.closed.Load() {
		return ErrClosed
	}
	if connID == "" {
		return fmt.Errorf("%w: empty ConnectionID", ErrInvalidInput)
	}
	if ownerID == "" {
		return fmt.Errorf("%w: empty OwnerID", ErrInvalidInput)
	}
	if err := validateTags(tags); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidInput, err.Error())
	}
	release, err := s.acquireWriter(ctx)
	if err != nil {
		return err
	}
	defer release()
	res, err := s.db.ExecContext(ctx,
		`UPDATE saved_queries SET tags = ?, updated_at = ?
		 WHERE id = ? AND connection_id = ? AND owner_id = ?`,
		serializeTags(tags), time.Now().UTC().UnixNano(),
		id, string(connID), ownerID)
	if err != nil {
		return fmt.Errorf("saved: tag: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Update mutates the per-field opt-ins. Empty UpdateFields is a no-op.
func (s *sqliteStore) Update(ctx context.Context, connID dbadmin.ConnectionID, ownerID, id string, fields UpdateFields) error {
	if s.closed.Load() {
		return ErrClosed
	}
	if connID == "" {
		return fmt.Errorf("%w: empty ConnectionID", ErrInvalidInput)
	}
	if ownerID == "" {
		return fmt.Errorf("%w: empty OwnerID", ErrInvalidInput)
	}
	if fields.Tags != nil {
		if err := validateTags(*fields.Tags); err != nil {
			return fmt.Errorf("%w: %s", ErrInvalidInput, err.Error())
		}
	}
	// Build SET clause incrementally. Nothing to do when every field
	// is nil.
	sets := []string{}
	args := []any{}
	if fields.Name != nil {
		sets = append(sets, "name = ?")
		args = append(args, *fields.Name)
	}
	if fields.Statement != nil {
		sets = append(sets, "statement = ?")
		args = append(args, *fields.Statement)
	}
	if fields.Description != nil {
		sets = append(sets, "description = ?")
		args = append(args, *fields.Description)
	}
	if fields.Tags != nil {
		sets = append(sets, "tags = ?")
		args = append(args, serializeTags(*fields.Tags))
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, time.Now().UTC().UnixNano())

	release, err := s.acquireWriter(ctx)
	if err != nil {
		return err
	}
	defer release()

	q := "UPDATE saved_queries SET " + strings.Join(sets, ", ") +
		" WHERE id = ? AND connection_id = ? AND owner_id = ?"
	args = append(args, id, string(connID), ownerID)
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		if isUniqueConstraint(err) {
			return ErrConflict
		}
		return fmt.Errorf("saved: update: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes one row. Returns (found, owned, err) so the httpapi
// caller can SEC-1-collapse not-found and not-owned into the same 404.
func (s *sqliteStore) Delete(ctx context.Context, connID dbadmin.ConnectionID, ownerID, id string) (found, owned bool, err error) {
	if s.closed.Load() {
		return false, false, ErrClosed
	}
	if connID == "" {
		return false, false, fmt.Errorf("%w: empty ConnectionID", ErrInvalidInput)
	}
	if ownerID == "" {
		return false, false, fmt.Errorf("%w: empty OwnerID", ErrInvalidInput)
	}
	release, err := s.acquireWriter(ctx)
	if err != nil {
		return false, false, err
	}
	defer release()
	// First check existence scoped only by (id, connection_id). Then
	// the owner check separately so the handler gets both signals.
	var rowOwner string
	row := s.db.QueryRowContext(ctx,
		`SELECT owner_id FROM saved_queries WHERE id = ? AND connection_id = ?`,
		id, string(connID))
	if err := row.Scan(&rowOwner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, false, nil
		}
		return false, false, fmt.Errorf("saved: delete probe: %w", err)
	}
	if rowOwner != ownerID {
		return true, false, nil
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM saved_queries WHERE id = ? AND connection_id = ? AND owner_id = ?`,
		id, string(connID), ownerID)
	if err != nil {
		return true, true, fmt.Errorf("saved: delete: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Lost a race; treat as not-found.
		return false, false, nil
	}
	return true, true, nil
}

// HasFTS reports the FTS5/LIKE mode.
func (s *sqliteStore) HasFTS() bool {
	return s.hasFTS
}

// Close releases the SQLite handle. Idempotent.
func (s *sqliteStore) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stmtAppend != nil {
		_ = s.stmtAppend.Close()
	}
	return s.db.Close()
}

// ─── Helpers ─────────────────────────────────────────────────────────

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRecord(row rowScanner) (*Record, error) {
	var (
		r          Record
		connID     string
		tagsStr    string
		starInt    int
		createdAt  int64
		updatedAt  int64
	)
	if err := row.Scan(
		&r.ID, &connID, &r.OwnerID,
		&r.Name, &r.Statement, &r.Description,
		&tagsStr, &starInt,
		&createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}
	r.ConnectionID = dbadmin.ConnectionID(connID)
	r.Tags = deserializeTags(tagsStr)
	r.Starred = starInt == 1
	r.CreatedAt = time.Unix(0, createdAt).UTC()
	r.UpdatedAt = time.Unix(0, updatedAt).UTC()
	return &r, nil
}

func scanSearchResult(row rowScanner) (*SearchResult, error) {
	var (
		r          SearchResult
		connID     string
		tagsStr    string
		starInt    int
		createdAt  int64
		updatedAt  int64
	)
	if err := row.Scan(
		&r.ID, &connID, &r.OwnerID,
		&r.Name, &r.Statement, &r.Description,
		&tagsStr, &starInt,
		&createdAt, &updatedAt, &r.Score,
	); err != nil {
		return nil, err
	}
	r.ConnectionID = dbadmin.ConnectionID(connID)
	r.Tags = deserializeTags(tagsStr)
	r.Starred = starInt == 1
	r.CreatedAt = time.Unix(0, createdAt).UTC()
	r.UpdatedAt = time.Unix(0, updatedAt).UTC()
	return &r, nil
}

// serializeTags joins tags as ",tag1,tag2," (fenced form). Empty list
// returns "". Callers must pre-validate that no tag contains a comma.
func serializeTags(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
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

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "_", `\_`)
	return s
}

func isMemoryDSN(dsn string) bool {
	if dsn == ":memory:" {
		return true
	}
	if strings.HasPrefix(dsn, "file::memory:") {
		return true
	}
	if strings.HasPrefix(dsn, "file:") {
		if i := strings.IndexByte(dsn, '?'); i > 0 && strings.Contains(dsn[i:], "mode=memory") {
			return true
		}
	}
	return false
}

func buildDSN(dsn string, pragmas []string) string {
	if len(pragmas) == 0 {
		return dsn
	}
	if strings.Contains(dsn, "_pragma=") {
		return dsn
	}
	var sep string
	if strings.IndexByte(dsn, '?') >= 0 {
		sep = "&"
	} else {
		sep = "?"
	}
	var b strings.Builder
	b.WriteString(dsn)
	for i, p := range pragmas {
		if i == 0 {
			b.WriteString(sep)
		} else {
			b.WriteString("&")
		}
		b.WriteString("_pragma=")
		b.WriteString(p)
	}
	return b.String()
}

func isValidTimestamp(t time.Time) bool {
	if t.IsZero() {
		return false
	}
	if t.UnixNano() <= 0 {
		return false
	}
	return true
}

// isUniqueConstraint sniffs the SQLite error text for the standard
// "UNIQUE constraint failed" marker. modernc.org/sqlite exposes the
// SQLite error code via the wrapped *sqlite.Error but the message is
// the most stable signal across driver versions.
func isUniqueConstraint(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}

// Compile-time assertion that *sqliteStore satisfies Store.
var _ Store = (*sqliteStore)(nil)

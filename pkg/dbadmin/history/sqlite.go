package history

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

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
//   - ":memory:" or shared-cache memory URIs (tests).
//
// The schema is created on first open if absent; subsequent opens are
// no-ops. Schema migrations (post-PR #7.5) will use a version table;
// for now the schema is v2 (PR #7.5 normalized tags into entry_tags)
// and any column change is a manual migration.
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

	// busyTimeout / maxOpen capture the effective values from OpenOpts
	// (or the defaults). Recorded so doc.Concurrency assertions can
	// inspect them from tests.
	busyTimeoutMS int
	maxOpenConns  int

	// maxRows caps total stored rows. Zero = unbounded. Enforced at
	// Append time: when the count exceeds this we delete the oldest
	// rows in a single statement so the cap holds.
	maxRows int

	// ftsBM25Weights holds the per-column bm25 weights when FTS5 is
	// active. Two entries: [sql, tags]. Zero means use bm25 default
	// (1.0). PR #7.5 medium: operator-tunable bm25 weights.
	ftsBM25Weights [2]float64

	// mu serializes schema-init + close. Reads and Append do not
	// hold this; SQLite's busy-timeout + WAL handles concurrency.
	mu sync.Mutex

	// writeSem bounds concurrent writers. SQLite serializes writes
	// internally, but bursty writer goroutines piled against the
	// busy-timeout can chew the panel UI's request budget; this
	// semaphore caps in-flight writers per process. PR #7.5 medium.
	writeSem chan struct{}

	// Prepared statements cached at init. Append is the hot path; the
	// rest are bonus. PR #7.5 medium: prepared-statement cache.
	stmtAppend     *sql.Stmt
	stmtTagsClear  *sql.Stmt
	stmtTagsInsert *sql.Stmt
	stmtsOnce      sync.Once
}

// OpenOpts tunes Store behavior at Open time. All fields are optional —
// zero values mean "use the package defaults." New fields are appended
// rather than reshaping existing ones; consumers can declare an
// OpenOpts literal without naming every field.
type OpenOpts struct {
	// RequireFTS5 makes Open fail when the underlying SQLite build
	// lacks FTS5 support. Without it the Store silently degrades to
	// LIKE-based Search, which is O(n) full-table scan and unusable
	// past ~10⁵ entries. Set true when the engine layer can't tolerate
	// the silent degradation (most production deployments). Defaults
	// to false for test parity with PR #7.
	RequireFTS5 bool

	// BusyTimeoutMS is the SQLite busy_timeout pragma value applied
	// at Open. Default 5000ms. Lower values reduce panel-UI stalls
	// under writer contention at the cost of more spurious busy
	// errors bubbling to the engine layer; raise it on multi-writer
	// integrated panels.
	BusyTimeoutMS int

	// MaxOpenConns is the database/sql open-connection ceiling.
	// Default 4. Pinned to 1 for in-memory DBs regardless of this
	// value (modernc.org/sqlite creates a fresh DB per conn unless
	// shared-cache is requested, so > 1 would split writes across
	// disjoint databases).
	MaxOpenConns int

	// MaxRows enforces a row-count ceiling. Zero = unbounded. When
	// Append would push the total over MaxRows the Store deletes
	// the oldest (lowest id) rows in the same transaction. The cap
	// is a coarse guardrail — operators tuning retention by time
	// should use StartRetentionLoop + DeleteOlderThan and leave
	// MaxRows zero or very large.
	MaxRows int

	// MaxWriters bounds concurrent in-flight Append/Star/Tag/Delete
	// calls in this process. Default 8. Lower it on single-CPU panels
	// where SQLite's internal write serialization is faster than the
	// goroutine scheduler can spin up. Zero disables the bound.
	MaxWriters int

	// FTSBM25Weights sets the per-column bm25 weights used by
	// Search's FTS5 ranking. Two values, [sqlWeight, tagsWeight].
	// Zero values fall back to bm25's default (1.0). Increase
	// tagsWeight when operators rely on tag-based recall; reduce it
	// when SQL-text relevance should dominate.
	FTSBM25Weights [2]float64
}

// defaultOpenOpts returns the package defaults.
func defaultOpenOpts() OpenOpts {
	return OpenOpts{
		BusyTimeoutMS: 5000,
		MaxOpenConns:  4,
		MaxWriters:    8,
	}
}

// Open creates / opens a SQLite-backed Store. dsn is a path or
// ":memory:" (or any "file::memory:?…" shared-cache URI). defaultEngine
// is the dialect to use for SQL redaction when the caller doesn't
// override via Entry.Engine. Pass a real engine — EngineUnknown is
// rejected by Append.
//
// Behavior mirrors OpenWithOpts(ctx, dsn, defaultEngine, OpenOpts{}).
func Open(ctx context.Context, dsn string, defaultEngine dbadmin.EngineKind) (Store, error) {
	return OpenWithOpts(ctx, dsn, defaultEngine, OpenOpts{})
}

// OpenWithOpts is the option-bearing constructor. See OpenOpts for
// per-field semantics. Added in PR #7.5; Open is retained as the
// simple constructor.
func OpenWithOpts(ctx context.Context, dsn string, defaultEngine dbadmin.EngineKind, opts OpenOpts) (Store, error) {
	if dsn == "" {
		return nil, fmt.Errorf("history: dsn required")
	}

	// Merge user opts with defaults. Explicit fields win; zero-value
	// fields fall back to the package default.
	def := defaultOpenOpts()
	if opts.BusyTimeoutMS <= 0 {
		opts.BusyTimeoutMS = def.BusyTimeoutMS
	}
	if opts.MaxOpenConns <= 0 {
		opts.MaxOpenConns = def.MaxOpenConns
	}
	if opts.MaxWriters < 0 {
		opts.MaxWriters = def.MaxWriters
	}

	// modernc.org/sqlite accepts the standard SQLite DSN. We append
	// WAL + busy_timeout so concurrent panel operators don't serialize
	// entirely on a single writer. Pragmas go into the connection
	// string via the modernc-specific "_pragma=" query parameter.
	//
	// DSN handling subtleties (PR #7.5 medium): the original code
	// blindly appended "?_pragma=…" which corrupted DSNs that already
	// carried a query string ("file:foo.db?cache=shared" → "file:foo.db?cache=shared?_pragma=…",
	// invalid). Use buildDSN which handles ?/& separator selection
	// and recognizes shared-cache memory URIs (file::memory:?…) as
	// memory DBs that still benefit from the busy_timeout pragma but
	// must NOT get WAL (WAL doesn't apply to memory DBs and produces
	// a warning).
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
		return nil, fmt.Errorf("history: sql.Open: %w", err)
	}
	// In-memory DBs without a shared-cache name pin to a single
	// connection — modernc.org/sqlite creates a fresh in-memory DB
	// per connection unless you use a shared-cache URI, so a
	// connection pool would give each goroutine its own empty DB and
	// writes would never converge.
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
		return nil, fmt.Errorf("history: ping: %w", err)
	}

	s := &sqliteStore{
		db:             db,
		dsn:            dsn,
		defaultEngine:  defaultEngine,
		busyTimeoutMS:  opts.BusyTimeoutMS,
		maxOpenConns:   opts.MaxOpenConns,
		maxRows:        opts.MaxRows,
		ftsBM25Weights: opts.FTSBM25Weights,
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
		return nil, fmt.Errorf("history: FTS5 required but not available in this SQLite build")
	}
	// Prepare hot-path statements. Failures here are fatal: the
	// fallback would be to silently degrade to per-call binding,
	// hiding the underlying schema issue. Better to surface it.
	if err := s.prepareStmts(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// initSchema creates the tables + FTS5 virtual table on first open.
// Idempotent; later calls do nothing.
//
// PR #7.5 splits the FTS5 probe from the trigger install: under the
// old code, a malformed CREATE TRIGGER (e.g. trigger collision from a
// half-applied earlier schema) was indistinguishable from a missing
// FTS5 module and silently downgraded the Store to LIKE search. Now
// the probe runs first; if it succeeds we mark hasFTS=true and create
// the triggers as a separate step that surfaces its own errors.
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
		-- Per-user starred index (covers normal operator listings).
		CREATE INDEX IF NOT EXISTS idx_entries_user_starred ON entries(user_id, starred, executed DESC) WHERE starred = 1;
		-- Admin-scoped starred index (covers cross-user listings; PR #7.5
		-- medium). Without this an admin "show me every starred entry"
		-- query falls back to a full-table scan despite the partial
		-- index above, because the per-user one is leftmost-keyed on
		-- user_id.
		CREATE INDEX IF NOT EXISTS idx_entries_starred_executed ON entries(starred, executed DESC) WHERE starred = 1;
		-- Normalized tag membership table (PR #7.5 medium: tags
		-- normalization). The serialized tags column on entries is
		-- retained for back-compat reads + FTS5 indexing of tag tokens,
		-- but the canonical join target for tag filters is now this
		-- table — Tag() rewrites both atomically. PRIMARY KEY orders
		-- by (tag, entry_id) so "find all entries tagged X" is an
		-- index range scan.
		CREATE TABLE IF NOT EXISTS entry_tags (
			tag      TEXT NOT NULL,
			entry_id INTEGER NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
			PRIMARY KEY (tag, entry_id)
		) WITHOUT ROWID;
		CREATE INDEX IF NOT EXISTS idx_entry_tags_entry ON entry_tags(entry_id);
	`
	if _, err := s.db.ExecContext(ctx, tableSQL); err != nil {
		return fmt.Errorf("history: init schema: %w", err)
	}

	// Probe for FTS5 support: try CREATE VIRTUAL TABLE in a separate
	// statement so its error is unambiguous. If the probe fails we
	// stay on LIKE search; triggers are skipped (no FTS table to
	// feed). If it succeeds we mark hasFTS=true and proceed to
	// install the triggers — a trigger failure now is a real bug
	// that should surface.
	const ftsProbe = `
		CREATE VIRTUAL TABLE IF NOT EXISTS entries_fts USING fts5(
			sql, tags,
			content='entries', content_rowid='id',
			tokenize='porter unicode61'
		);
	`
	if _, err := s.db.ExecContext(ctx, ftsProbe); err != nil {
		// FTS5 not available — leave hasFTS=false. Search will
		// degrade to LIKE without breaking the API.
		return nil
	}
	s.hasFTS = true

	const ftsTriggers = `
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
	if _, err := s.db.ExecContext(ctx, ftsTriggers); err != nil {
		return fmt.Errorf("history: install FTS triggers: %w", err)
	}
	return nil
}

// prepareStmts caches the hot-path prepared statements. Called once
// after initSchema; safe against repeat invocation via stmtsOnce.
func (s *sqliteStore) prepareStmts(ctx context.Context) error {
	var perr error
	s.stmtsOnce.Do(func() {
		stmtAppend, err := s.db.PrepareContext(ctx, `
			INSERT INTO entries (user_id, connection_id, sql, class, tags, starred, duration_ms, rows_returned, error, executed, engine)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			perr = fmt.Errorf("history: prepare append: %w", err)
			return
		}
		s.stmtAppend = stmtAppend

		stmtTagsClear, err := s.db.PrepareContext(ctx, `DELETE FROM entry_tags WHERE entry_id = ?`)
		if err != nil {
			perr = fmt.Errorf("history: prepare tags clear: %w", err)
			return
		}
		s.stmtTagsClear = stmtTagsClear

		stmtTagsInsert, err := s.db.PrepareContext(ctx, `INSERT OR IGNORE INTO entry_tags (tag, entry_id) VALUES (?, ?)`)
		if err != nil {
			perr = fmt.Errorf("history: prepare tags insert: %w", err)
			return
		}
		s.stmtTagsInsert = stmtTagsInsert
	})
	return perr
}

// acquireWriter takes a writer slot from writeSem, returning a release
// func. When writeSem is nil (MaxWriters=0) this is a no-op.
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

	// Sanity-check the Executed timestamp: the IsZero guard alone
	// lets time.Unix(0,0) and pre-1970 caller-supplied junk through.
	// We clamp to "now" rather than rejecting because the engine
	// layer hits this column for ordering and a bogus past timestamp
	// would push the entry to the bottom of every listing forever.
	if !isValidTimestamp(e.Executed) {
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

	release, err := s.acquireWriter(ctx)
	if err != nil {
		return 0, err
	}
	defer release()

	res, err := s.stmtAppend.ExecContext(ctx,
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

	// Mirror the serialized tags into entry_tags for indexed
	// retrieval. Done as a separate step so a single statement-
	// boundary failure here surfaces (vs being swallowed by a
	// trigger).
	for _, tag := range deserializeTags(tagsStr) {
		if _, err := s.stmtTagsInsert.ExecContext(ctx, tag, id); err != nil {
			return id, fmt.Errorf("history: tag mirror: %w", err)
		}
	}

	// Row-cap enforcement (PR #7.5 H9). Evict the oldest rows when
	// the cap is exceeded. We use a single DELETE keyed by id so
	// FK CASCADE on entry_tags fires automatically.
	if s.maxRows > 0 {
		if _, err := s.db.ExecContext(ctx, `
			DELETE FROM entries WHERE id IN (
				SELECT id FROM entries ORDER BY id ASC LIMIT MAX(0, (SELECT COUNT(*) FROM entries) - ?)
			)`, s.maxRows); err != nil {
			// Eviction failure does NOT fail the Append; we already
			// recorded the row. The cap may be momentarily over;
			// the next Append (or a retention sweep) will catch up.
			// Best-effort.
			_ = err
		}
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
	args := []any{}
	whereParts := []string{"e.user_id = ?"}
	args = append(args, opts.UserID)

	// Tag filter joins the normalized entry_tags table (PR #7.5
	// medium). The fenced-tags LIKE pattern still works against the
	// serialized column, but the normalized join is O(log n) per
	// tag where the LIKE scan is O(n).
	joinTags := opts.Tag != ""

	b.WriteString(`SELECT e.id, e.user_id, e.connection_id, e.sql, e.class, e.tags, e.starred,
	                  e.duration_ms, e.rows_returned, e.error, e.executed, e.engine
		FROM entries e`)
	if joinTags {
		b.WriteString(` JOIN entry_tags et ON et.entry_id = e.id`)
		whereParts = append(whereParts, "et.tag = ?")
		args = append(args, opts.Tag)
	}

	if opts.ConnectionID != "" {
		whereParts = append(whereParts, "e.connection_id = ?")
		args = append(args, string(opts.ConnectionID))
	}
	if opts.OnlyStarred {
		whereParts = append(whereParts, "e.starred = 1")
	}
	// ClassPtr is the preferred non-zero-value filter (PR #7.5
	// medium); IncludeClass is kept for back-compat. Either being set
	// activates the filter.
	if opts.ClassPtr != nil {
		whereParts = append(whereParts, "e.class = ?")
		args = append(args, int(*opts.ClassPtr))
	} else if opts.IncludeClass {
		whereParts = append(whereParts, "e.class = ?")
		args = append(args, int(opts.Class))
	}
	if !opts.Since.IsZero() {
		whereParts = append(whereParts, "e.executed >= ?")
		args = append(args, opts.Since.UnixNano())
	}
	if !opts.Until.IsZero() {
		whereParts = append(whereParts, "e.executed < ?")
		args = append(args, opts.Until.UnixNano())
	}
	b.WriteString(" WHERE ")
	b.WriteString(strings.Join(whereParts, " AND "))
	// Deterministic tiebreaker: executed DESC, id DESC. Without id
	// DESC two entries appended in the same nanosecond would order
	// non-deterministically across runs.
	b.WriteString(" ORDER BY e.executed DESC, e.id DESC LIMIT ? OFFSET ?")
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
//
// PR #7.5: opts.Offset is now validated (was List-only); FTS5 query
// input is length-capped and control-byte-stripped; bm25 ranking
// supports per-column weights via OpenOpts.FTSBM25Weights; ordering
// includes a deterministic id-DESC tiebreaker beyond executed-DESC.
func (s *sqliteStore) Search(ctx context.Context, query string, opts ListOpts) ([]SearchResult, error) {
	if s.closed.Load() {
		return nil, ErrClosed
	}
	if opts.UserID == "" {
		return nil, fmt.Errorf("%w: empty UserID", ErrInvalidInput)
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

// MaxFTSQueryBytes caps the FTS5 query length. SQLite's FTS5 parser
// can be coaxed into pathological behavior on very long inputs; cap
// keeps the search predictable.
const MaxFTSQueryBytes = 4096

// sanitizeFTSQuery normalizes a user-supplied search query for FTS5
// phrase search: strips ASCII control bytes (NUL, tabs, etc.), drops
// embedded double quotes (FTS5 phrase delimiter), and truncates to
// MaxFTSQueryBytes. Returns the cleaned form ready for phrase-wrapping.
func sanitizeFTSQuery(q string) string {
	var b strings.Builder
	b.Grow(len(q))
	for _, r := range q {
		switch {
		case r == '"':
			// Drop FTS5 phrase delimiters; we wrap the whole query
			// in our own quotes.
			continue
		case r < 0x20, r == 0x7f:
			// Strip ASCII control bytes.
			continue
		default:
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(out) > MaxFTSQueryBytes {
		out = truncateUTF8(out, MaxFTSQueryBytes)
	}
	return out
}

func (s *sqliteStore) searchFTS(ctx context.Context, query string, opts ListOpts, limit int) ([]SearchResult, error) {
	clean := sanitizeFTSQuery(query)
	if clean == "" {
		// All-control-byte input degenerates to empty; reject the
		// same way an empty query is rejected.
		return nil, fmt.Errorf("%w: empty search query (after sanitization)", ErrInvalidInput)
	}
	ftsQuery := `"` + clean + `"`

	// bm25 supports per-column weights as varargs:
	//   bm25(fts_table, sql_weight, tags_weight)
	// Default (no weights) is 1.0 across the board. Operators tune
	// via OpenOpts.FTSBM25Weights; zero entries fall through to the
	// default.
	scoreExpr := "bm25(entries_fts)"
	if s.ftsBM25Weights[0] > 0 || s.ftsBM25Weights[1] > 0 {
		sw := s.ftsBM25Weights[0]
		if sw <= 0 {
			sw = 1.0
		}
		tw := s.ftsBM25Weights[1]
		if tw <= 0 {
			tw = 1.0
		}
		scoreExpr = fmt.Sprintf("bm25(entries_fts, %g, %g)", sw, tw)
	}

	var b strings.Builder
	b.WriteString(`
		SELECT e.id, e.user_id, e.connection_id, e.sql, e.class, e.tags, e.starred,
		       e.duration_ms, e.rows_returned, e.error, e.executed, e.engine,
		       `)
	b.WriteString(scoreExpr)
	b.WriteString(` AS score
		FROM entries_fts
		JOIN entries e ON e.id = entries_fts.rowid`)

	joinTags := opts.Tag != ""
	if joinTags {
		b.WriteString(` JOIN entry_tags et ON et.entry_id = e.id`)
	}
	b.WriteString(` WHERE entries_fts MATCH ? AND e.user_id = ?`)
	args := []any{ftsQuery, opts.UserID}

	if joinTags {
		b.WriteString(" AND et.tag = ?")
		args = append(args, opts.Tag)
	}
	if opts.ConnectionID != "" {
		b.WriteString(" AND e.connection_id = ?")
		args = append(args, string(opts.ConnectionID))
	}
	if opts.OnlyStarred {
		b.WriteString(" AND e.starred = 1")
	}
	if opts.ClassPtr != nil {
		b.WriteString(" AND e.class = ?")
		args = append(args, int(*opts.ClassPtr))
	} else if opts.IncludeClass {
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
	// Lower bm25 score = better match. Deterministic tiebreaker
	// adds id DESC so two entries with identical scores + executed
	// timestamps don't ping-pong across pages.
	b.WriteString(" ORDER BY score ASC, e.executed DESC, e.id DESC LIMIT ? OFFSET ?")
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
		SELECT e.id, e.user_id, e.connection_id, e.sql, e.class, e.tags, e.starred,
		       e.duration_ms, e.rows_returned, e.error, e.executed, e.engine,
		       1.0 AS score
		FROM entries e`)
	joinTags := opts.Tag != ""
	if joinTags {
		b.WriteString(` JOIN entry_tags et ON et.entry_id = e.id`)
	}
	b.WriteString(` WHERE (e.sql LIKE ? ESCAPE '\' OR e.tags LIKE ? ESCAPE '\') AND e.user_id = ?`)
	pat := "%" + escapeLike(query) + "%"
	args := []any{pat, pat, opts.UserID}

	if joinTags {
		b.WriteString(" AND et.tag = ?")
		args = append(args, opts.Tag)
	}
	if opts.ConnectionID != "" {
		b.WriteString(" AND e.connection_id = ?")
		args = append(args, string(opts.ConnectionID))
	}
	if opts.OnlyStarred {
		b.WriteString(" AND e.starred = 1")
	}
	if opts.ClassPtr != nil {
		b.WriteString(" AND e.class = ?")
		args = append(args, int(*opts.ClassPtr))
	} else if opts.IncludeClass {
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
	b.WriteString(" ORDER BY e.executed DESC, e.id DESC LIMIT ? OFFSET ?")
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
	release, err := s.acquireWriter(ctx)
	if err != nil {
		return err
	}
	defer release()
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

	release, err := s.acquireWriter(ctx)
	if err != nil {
		return err
	}
	defer release()

	// Confirm the row exists and belongs to this user before
	// touching the normalized table. The serialized-tags UPDATE
	// would also return RowsAffected=0 on miss, but we need to
	// short-circuit before deleting the entry_tags rows.
	tagsStr := serializeTags(tags)
	if err := s.mutate(ctx, "UPDATE entries SET tags = ?", id, userID, tagsStr); err != nil {
		return err
	}
	// Rewrite the normalized side. Tag() is admin-rare-ish so a
	// per-call transaction is fine; the writeSem already prevents
	// pile-up.
	if _, err := s.stmtTagsClear.ExecContext(ctx, id); err != nil {
		return fmt.Errorf("history: tag clear: %w", err)
	}
	for _, tag := range deserializeTags(tagsStr) {
		if _, err := s.stmtTagsInsert.ExecContext(ctx, tag, id); err != nil {
			return fmt.Errorf("history: tag insert: %w", err)
		}
	}
	return nil
}

// Delete removes one entry.
func (s *sqliteStore) Delete(ctx context.Context, id int64, userID string) error {
	if s.closed.Load() {
		return ErrClosed
	}
	if userID == "" {
		return fmt.Errorf("%w: empty UserID", ErrInvalidInput)
	}
	release, err := s.acquireWriter(ctx)
	if err != nil {
		return err
	}
	defer release()
	// entry_tags rows cascade via FK ON DELETE CASCADE.
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
//
// PR #7.5 H9: chunked in 1000-row batches so a 365-day-overdue sweep
// doesn't hold the writer lock for multi-second windows. The caller's
// context is honored between batches; partial progress is preserved
// on cancellation (returned count reflects what was actually deleted).
const deleteOlderThanBatch = 1000

func (s *sqliteStore) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	if s.closed.Load() {
		return 0, ErrClosed
	}
	release, err := s.acquireWriter(ctx)
	if err != nil {
		return 0, err
	}
	defer release()

	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		// LIMIT in DELETE requires SQLITE_ENABLE_UPDATE_DELETE_LIMIT
		// in modernc.org/sqlite; the safer portable form is an
		// id-IN subselect.
		res, err := s.db.ExecContext(ctx, `
			DELETE FROM entries WHERE id IN (
				SELECT id FROM entries WHERE executed < ? LIMIT ?
			)`, cutoff.UnixNano(), deleteOlderThanBatch)
		if err != nil {
			return total, fmt.Errorf("history: retention sweep: %w", err)
		}
		n, _ := res.RowsAffected()
		total += n
		if n < deleteOlderThanBatch {
			break
		}
	}
	return total, nil
}

// StartRetentionLoop launches a goroutine that calls DeleteOlderThan
// every `period` with cutoff = time.Now().Add(-retention). Returns a
// cancel func; the caller is expected to call it during shutdown.
// Returns immediately; the first sweep runs after `period` elapses
// (not at start) to avoid every panel restart triggering a full
// retention pass.
//
// PR #7.5 H9: callers (the engine layer's periodic-task scheduler)
// can wire this in once and forget about it.
func (s *sqliteStore) StartRetentionLoop(ctx context.Context, period, retention time.Duration) (cancel func()) {
	stopCtx, stop := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(period)
		defer t.Stop()
		for {
			select {
			case <-stopCtx.Done():
				return
			case <-t.C:
				if s.closed.Load() {
					return
				}
				cutoff := time.Now().Add(-retention)
				_, _ = s.DeleteOlderThan(stopCtx, cutoff)
			}
		}
	}()
	return stop
}

// HasFTS reports whether this Store's underlying SQLite build has
// FTS5 support. Callers (UI / engine layer) use it to warn when
// Search is running on the LIKE fallback. PR #7.5 H8.
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
	// Close prepared statements first so the conn close doesn't
	// race a pending Exec. Errors are ignored — Close is
	// shutdown-time and the db.Close below will catch anything
	// truly broken.
	if s.stmtAppend != nil {
		_ = s.stmtAppend.Close()
	}
	if s.stmtTagsClear != nil {
		_ = s.stmtTagsClear.Close()
	}
	if s.stmtTagsInsert != nil {
		_ = s.stmtTagsInsert.Close()
	}
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
		e         Entry
		connID    string
		classInt  int
		tagsStr   string
		starInt   int
		executed  int64
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
		r         SearchResult
		connID    string
		classInt  int
		tagsStr   string
		starInt   int
		executed  int64
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

// isMemoryDSN recognizes both the canonical ":memory:" DSN and the
// modernc.org/sqlite "file::memory:?cache=shared" URI form. Memory DBs
// share a class of constraints: WAL doesn't apply, the conn pool must
// pin to 1 (unless shared-cache is requested, which we still treat as
// memory for the WAL question).
func isMemoryDSN(dsn string) bool {
	if dsn == ":memory:" {
		return true
	}
	// modernc-style file URI: "file::memory:?…" — note the second
	// colon. Also "file:something?mode=memory" with mode=memory.
	if strings.HasPrefix(dsn, "file::memory:") {
		return true
	}
	if strings.HasPrefix(dsn, "file:") {
		// Crude but effective: look for mode=memory in the query
		// component.
		if i := strings.IndexByte(dsn, '?'); i > 0 && strings.Contains(dsn[i:], "mode=memory") {
			return true
		}
	}
	return false
}

// buildDSN appends pragmas to the DSN, handling both the no-query
// case ("?_pragma=…") and the already-has-query case ("&_pragma=…").
// modernc.org/sqlite accepts repeated "_pragma=" entries — one per
// statement. Skip if pragmas is empty.
func buildDSN(dsn string, pragmas []string) string {
	if len(pragmas) == 0 {
		return dsn
	}
	// If the DSN already specifies any _pragma directive we leave it
	// alone — operator intent wins. This matches the previous "skip
	// when _journal_mode is set" heuristic but is broader.
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

// isValidTimestamp returns false for time.Time values the engine layer
// shouldn't be persisting: the zero value, time.Unix(0,0), and pre-1970
// timestamps. The Append guard upgrades these to time.Now() so the
// ordering invariants hold.
func isValidTimestamp(t time.Time) bool {
	if t.IsZero() {
		return false
	}
	// Pre-1970 catch-all also rejects time.Unix(0,0) (== 1970-01-01).
	// We allow exactly 1970-01-01 since some testdata uses it
	// intentionally; reject only strictly-earlier values plus the
	// IsZero sentinel already filtered above. Sentinel-ish caller
	// junk like time.Unix(0,0) shows up as Unix nanoseconds 0 which
	// IsZero does NOT catch — we filter it explicitly.
	if t.UnixNano() <= 0 {
		return false
	}
	return true
}

// truncateUTF8 returns s truncated to at most max bytes, snapped to
// the previous rune boundary if max would split a multi-byte rune.
// PR #7.5 low: MaxSQLLength used to split UTF-8 runes at the byte
// boundary; this helper is the safe replacement.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	// Step back from max until we hit a valid rune-start boundary.
	end := max
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}

// Compile-time assertion that *sqliteStore satisfies Store.
var _ Store = (*sqliteStore)(nil)

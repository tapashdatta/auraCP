package driver

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// SlowLogProbe checks for the pg_stat_statements extension. When
// installed, the connection can read the aggregated digest view; when
// missing, the operator must run CREATE EXTENSION pg_stat_statements
// (typically as a superuser in shared_preload_libraries-loaded form).
//
// We do not surface log_min_duration_statement here — pg_stat_statements
// itself has no per-row duration filter (it aggregates every statement).
// The TailSlowLog filter applies MinDuration against mean_exec_time.
func (c *postgresConn) SlowLogProbe(ctx context.Context) (SlowLogMode, string, error) {
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var present bool
	if err := c.pool.QueryRow(pingCtx,
		"SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname = 'pg_stat_statements')").
		Scan(&present); err != nil {
		return SlowLogModeUnavailable, "", classifyPostgresErr(wrapCtxErr(pingCtx, err))
	}
	if !present {
		return SlowLogModeUnavailable,
			"install the pg_stat_statements extension: " +
				"add 'pg_stat_statements' to shared_preload_libraries, " +
				"restart, then CREATE EXTENSION pg_stat_statements",
			ErrSlowLogUnavailable
	}
	// Verify SELECT works against the view. A 42501 (insufficient
	// privilege) maps to ErrPermission via classifyPostgresErr — a
	// distinct surface from the "extension missing" case.
	if _, err := c.pool.Exec(pingCtx,
		"SELECT 1 FROM pg_stat_statements WHERE false"); err != nil {
		return SlowLogModeUnavailable, "", classifyPostgresErr(wrapCtxErr(pingCtx, err))
	}
	return SlowLogModeSnapshot, "", nil
}

// TailSlowLog reads pg_stat_statements joined to pg_roles + pg_database
// and emits one SlowQueryRow per digest whose mean_exec_time exceeds
// MinDuration. Since is interpreted as "skip digests last seen before
// this point" but pg_stat_statements does not record a last-seen
// timestamp — every snapshot is "as of now", so subsequent polls return
// the same digest with updated counters. The httpapi layer is
// responsible for de-duplicating digests across polls if it wants
// strict "new entries only" semantics.
//
// The Postgres path therefore has SlowLogModeSnapshot, not Tail. The
// httpapi meta frame surfaces this so the UI can render an appropriate
// "snapshot @ HH:MM:SS" badge rather than a misleading scrolling tail.
func (c *postgresConn) TailSlowLog(ctx context.Context, limits Limits, opts SlowLogOptions) (SlowLogIter, error) {
	mode, hint, err := c.SlowLogProbe(ctx)
	if err != nil {
		if hint != "" {
			return nil, fmt.Errorf("%w: %s", ErrSlowLogUnavailable, hint)
		}
		return nil, err
	}
	if mode != SlowLogModeSnapshot {
		return nil, ErrSlowLogUnavailable
	}

	ctx, cancel := limits.ApplyTimeout(ctx)

	rowCap := opts.MaxRows
	if rowCap <= 0 || (limits.MaxRows > 0 && limits.MaxRows < rowCap) {
		rowCap = limits.MaxRows
	}
	if rowCap <= 0 {
		rowCap = 1000
	}

	// mean_exec_time is in milliseconds. Convert MinDuration to ms.
	minMS := float64(opts.MinDuration.Microseconds()) / 1000.0

	q := `SELECT
	        pg_roles.rolname AS user_name,
	        pg_database.datname AS db_name,
	        pss.calls,
	        pss.total_exec_time,
	        pss.mean_exec_time,
	        pss.rows,
	        pss.query
	      FROM pg_stat_statements pss
	      LEFT JOIN pg_roles    ON pg_roles.oid    = pss.userid
	      LEFT JOIN pg_database ON pg_database.oid = pss.dbid
	      WHERE pss.mean_exec_time >= $1
	      ORDER BY pss.mean_exec_time DESC
	      LIMIT $2`

	rows, err := c.pool.Query(ctx, q, minMS, rowCap)
	if err != nil {
		cancel()
		return nil, classifyPostgresErr(wrapCtxErr(ctx, err))
	}
	return &postgresSlowLogIter{
		rows:   rows,
		cancel: cancel,
		cap:    rowCap,
		now:    time.Now().UTC(),
	}, nil
}

// postgresSlowLogIter implements SlowLogIter against pg_stat_statements.
type postgresSlowLogIter struct {
	rows   pgx.Rows
	cancel context.CancelFunc
	emit   int
	cap    int
	now    time.Time
	closed bool
}

func (it *postgresSlowLogIter) Next(ctx context.Context) (SlowQueryRow, error) {
	if it.closed {
		return SlowQueryRow{}, ErrClosed
	}
	if it.emit >= it.cap {
		return SlowQueryRow{}, ErrCapped
	}
	if !it.rows.Next() {
		if err := it.rows.Err(); err != nil {
			return SlowQueryRow{}, classifyPostgresErr(wrapCtxErr(ctx, err))
		}
		return SlowQueryRow{}, ErrEOF
	}

	var (
		userName, dbName, query *string
		calls                   int64
		totalMS, meanMS         float64
		rowsTouched             int64
	)
	if err := it.rows.Scan(
		&userName, &dbName, &calls, &totalMS, &meanMS, &rowsTouched, &query,
	); err != nil {
		return SlowQueryRow{}, classifyPostgresErr(wrapCtxErr(ctx, err))
	}
	it.emit++

	user := ""
	if userName != nil {
		user = *userName
	}
	db := ""
	if dbName != nil {
		db = *dbName
	}
	digest := ""
	if query != nil {
		digest = *query
	}

	return SlowQueryRow{
		Time:         it.now,
		UserHost:     user,
		QueryTime:    time.Duration(totalMS * float64(time.Millisecond)),
		LockTime:     0,
		MeanTime:     time.Duration(meanMS * float64(time.Millisecond)),
		RowsExamined: rowsTouched,
		RowsSent:     0,
		Calls:        calls,
		Database:     db,
		SQLDigest:    digest,
	}, nil
}

func (it *postgresSlowLogIter) Close() error {
	if it.closed {
		return nil
	}
	it.closed = true
	it.rows.Close()
	if it.cancel != nil {
		it.cancel()
	}
	return nil
}

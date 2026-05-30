package driver

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// SlowLogProbe checks @@slow_query_log + @@log_output and verifies the
// mysql.slow_log table is readable from this connection. Returns the
// mode + an empty hint when usable; SlowLogModeUnavailable + a hint
// listing the enabling SQL otherwise.
//
// We do not require SUPER; SELECT on mysql.slow_log is sufficient. If
// the connection lacks the SELECT grant, the driver maps the resulting
// 1142 to ErrPermission via classifyMySQLErr and the caller forwards
// the typical "backend permission denied" envelope — distinct from
// "slow-log not enabled" so the operator can fix the right thing.
func (c *mysqlConn) SlowLogProbe(ctx context.Context) (SlowLogMode, string, error) {
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var sqlLog, logOutput string
	if err := c.db.QueryRowContext(pingCtx,
		"SELECT @@slow_query_log, @@log_output").
		Scan(&sqlLog, &logOutput); err != nil {
		return SlowLogModeUnavailable, "", classifyMySQLErr(wrapCtxErr(pingCtx, err))
	}

	enabled := strings.EqualFold(sqlLog, "ON") || sqlLog == "1"
	tableOutput := strings.Contains(strings.ToUpper(logOutput), "TABLE")

	if !enabled || !tableOutput {
		return SlowLogModeUnavailable,
			"enable slow-log capture to TABLE: " +
				"SET GLOBAL slow_query_log=ON, log_output='TABLE'",
			ErrSlowLogUnavailable
	}

	// Verify SELECT works against mysql.slow_log. We don't read any
	// rows; the probe checks the privilege without dragging row data
	// over the wire.
	if _, err := c.db.ExecContext(pingCtx,
		"SELECT 1 FROM mysql.slow_log WHERE 1=0"); err != nil {
		return SlowLogModeUnavailable, "", classifyMySQLErr(wrapCtxErr(pingCtx, err))
	}
	return SlowLogModeTable, "", nil
}

// TailSlowLog opens a streaming iterator over mysql.slow_log filtered
// to rows whose start_time > opts.Since AND query_time >= opts.MinDuration.
//
// The query uses parameterised placeholders so the driver layer never
// interpolates user input. Ordering is by start_time ASC so the caller
// can advance Since by the last emitted row to follow the log without
// gaps.
func (c *mysqlConn) TailSlowLog(ctx context.Context, limits Limits, opts SlowLogOptions) (SlowLogIter, error) {
	mode, hint, err := c.SlowLogProbe(ctx)
	if err != nil {
		if hint != "" {
			return nil, fmt.Errorf("%w: %s", ErrSlowLogUnavailable, hint)
		}
		return nil, err
	}
	if mode != SlowLogModeTable {
		return nil, ErrSlowLogUnavailable
	}

	ctx, cancel := limits.ApplyTimeout(ctx)

	rowCap := opts.MaxRows
	if rowCap <= 0 || (limits.MaxRows > 0 && limits.MaxRows < rowCap) {
		rowCap = limits.MaxRows
	}
	if rowCap <= 0 {
		rowCap = 10000
	}

	// MariaDB stores query_time / lock_time as TIME(6). Convert to
	// microseconds-since-zero for comparison against MinDuration in µs.
	// We pass MinDuration in µs to avoid TIME literal pitfalls.
	minUS := opts.MinDuration.Microseconds()
	since := opts.Since
	if since.IsZero() {
		since = time.Unix(0, 0)
	}

	q := `SELECT start_time, user_host, query_time, lock_time,
	             rows_sent, rows_examined, db, sql_text
	      FROM mysql.slow_log
	      WHERE start_time > ?
	        AND TIME_TO_SEC(query_time) * 1000000 +
	            MICROSECOND(query_time) >= ?
	      ORDER BY start_time ASC
	      LIMIT ?`

	rows, err := c.db.QueryContext(ctx, q, since, minUS, rowCap)
	if err != nil {
		cancel()
		return nil, classifyMySQLErr(wrapCtxErr(ctx, err))
	}
	return &mysqlSlowLogIter{rows: rows, cancel: cancel, cap: rowCap}, nil
}

// mysqlSlowLogIter implements SlowLogIter against mysql.slow_log.
type mysqlSlowLogIter struct {
	rows   *sql.Rows
	cancel context.CancelFunc
	emit   int
	cap    int
	closed bool
}

func (it *mysqlSlowLogIter) Next(ctx context.Context) (SlowQueryRow, error) {
	if it.closed {
		return SlowQueryRow{}, ErrClosed
	}
	if it.emit >= it.cap {
		return SlowQueryRow{}, ErrCapped
	}
	if !it.rows.Next() {
		if err := it.rows.Err(); err != nil {
			return SlowQueryRow{}, classifyMySQLErr(wrapCtxErr(ctx, err))
		}
		return SlowQueryRow{}, ErrEOF
	}

	var (
		startTime               time.Time
		userHost, db, sqlText   sql.NullString
		queryTimeStr, lockTimeS sql.NullString
		rowsSent, rowsExamined  sql.NullInt64
	)
	if err := it.rows.Scan(
		&startTime, &userHost, &queryTimeStr, &lockTimeS,
		&rowsSent, &rowsExamined, &db, &sqlText,
	); err != nil {
		return SlowQueryRow{}, classifyMySQLErr(wrapCtxErr(ctx, err))
	}
	it.emit++

	qt := parseMySQLTime(queryTimeStr.String)
	lt := parseMySQLTime(lockTimeS.String)
	return SlowQueryRow{
		Time:         startTime.UTC(),
		UserHost:     userHost.String,
		QueryTime:    qt,
		LockTime:     lt,
		MeanTime:     qt,
		RowsExamined: rowsExamined.Int64,
		RowsSent:     rowsSent.Int64,
		Calls:        1,
		Database:     db.String,
		SQLDigest:    sqlText.String,
	}, nil
}

func (it *mysqlSlowLogIter) Close() error {
	if it.closed {
		return nil
	}
	it.closed = true
	if it.cancel != nil {
		it.cancel()
	}
	return it.rows.Close()
}

// parseMySQLTime converts a TIME(6) string like "00:00:02.123456" into
// a time.Duration. Returns 0 on parse failure.
func parseMySQLTime(s string) time.Duration {
	if s == "" {
		return 0
	}
	// Split HH:MM:SS[.frac]
	parts := strings.SplitN(s, ":", 3)
	if len(parts) != 3 {
		return 0
	}
	h, _ := atoi(parts[0])
	m, _ := atoi(parts[1])
	secStr := parts[2]
	var sec int
	var frac float64
	if dot := strings.IndexByte(secStr, '.'); dot >= 0 {
		sec, _ = atoi(secStr[:dot])
		// Right-pad fractional to 6 digits.
		f := secStr[dot+1:]
		if len(f) > 6 {
			f = f[:6]
		}
		for len(f) < 6 {
			f += "0"
		}
		us, _ := atoi(f)
		frac = float64(us) / 1_000_000
	} else {
		sec, _ = atoi(secStr)
	}
	total := float64(h*3600+m*60+sec) + frac
	return time.Duration(total * float64(time.Second))
}

// atoi is a tiny strconv.Atoi shim that swallows the error — callers
// already default zero on parse failure.
func atoi(s string) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not a digit: %q", r)
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

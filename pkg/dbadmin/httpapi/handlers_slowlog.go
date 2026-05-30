package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/driver"
	"github.com/gorilla/websocket"
)

// handleSlowLogStream upgrades to a WebSocket and serves the slow-log
// streaming protocol (v0.3.2-C). It mirrors handleSQLStream's CSWSH
// + CSRF + rate-limit + semaphore + ping/pong + DEF-{6,13,14,19,22,23,24}
// behaviour exactly — see handlers_stream.go for the canonical
// numbered-comment reference. This handler differs only in:
//
//   - RBAC: a single authorize() against ActionSlowLogRead (no
//     classifier, no per-table grant — there is no user statement).
//   - Driver dispatch: type-asserts the opened Conn to SlowLogReader
//     and refuses with CodeSlowLogUnavailable when the engine does
//     not implement the capability or the backend prerequisites are
//     missing.
//   - Frame protocol: the meta frame is slowLogMetaFrame (carries
//     Mode + Hint) and the row frame is slowLogRow.
//   - Follow loop (MariaDB only): when Follow=true the handler polls
//     the slow-log table every wsSlowLogPollInterval, advancing Since
//     to the last emitted row, until the client cancels or the
//     stream timeout fires. Postgres pg_stat_statements is snapshot-
//     only — the handler emits one snapshot then done, regardless of
//     Follow.
//
// SQL text emitted to the client is excerpted to wsSlowLogSQLExcerptCap
// bytes. The audit log captures the action open + error codes (never
// the SQL text on the wire), exactly as handleSQLStream.
func handleSlowLogStream(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// (1) Auth was already run by the authn middleware.
		user, _ := userFrom(r.Context())
		if user.ID == "" {
			writeError(w, r, http.StatusUnauthorized, CodeUnauthenticated, "authentication required")
			return
		}

		connID := dbadmin.ConnectionID(r.PathValue("id"))
		if connID == "" {
			writeError(w, r, http.StatusNotFound, CodeNotFound, "connection id required")
			return
		}

		// (2) CSWSH defense — Origin allow-list / true same-origin.
		if !originAllowed(s, r) {
			wsAuditDenial(s, r.Context(), user, connID, "origin-rejected", r.Header.Get("Origin"))
			writeError(w, r, http.StatusForbidden, CodeOriginRejected, "origin not allowed")
			return
		}

		// (3) DEF-13: per-user concurrent-stream cap. Slow-log streams
		// share the same gate as SQL streams — operators don't get
		// double-budget by opening one of each. Token-bucket on the
		// upgrade rate sits on the mutating bucket because the stream
		// is long-lived.
		if s.limiter != nil && !s.limiter.allow(user.ID, rateClassMutating) {
			emitDenialAudit(s, r, dbadmin.Action("ratelimit.denied"), "ws-upgrade")
			w.Header().Set("Retry-After", "1")
			writeError(w, r, http.StatusTooManyRequests, CodeRateLimited, "rate limit exceeded")
			return
		}
		if !s.queryGate.acquire(user.ID) {
			w.Header().Set("Retry-After", "5")
			writeError(w, r, http.StatusTooManyRequests, CodeRateLimited, "concurrent query cap reached")
			return
		}
		defer s.queryGate.release(user.ID)

		// (4) CSRF on the upgrade — header OR aura.csrf.<token>
		// subprotocol. Browsers can't set custom headers on the WS
		// constructor.
		if !s.csrfDisabled && !wsCSRFValid(s, r) {
			wsAuditDenial(s, r.Context(), user, connID, "csrf-rejected", "")
			writeError(w, r, http.StatusForbidden, CodeCSRFRejected, "CSRF check failed")
			return
		}

		// (5) Capture cookie value BEFORE upgrade for DEF-6 open-frame
		// revalidation downstream.
		expectedCSRF := wsExpectedCSRFCookie(s, r)

		// (6) Upgrade.
		conn, err := s.upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetReadLimit(wsReadLimit)
		_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
		conn.SetPongHandler(func(string) error {
			_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
			return nil
		})

		streamCtx, cancel := context.WithCancel(r.Context())
		defer cancel()

		// (7) DEF-14: ALL websocket writes go through these mu-guarded
		// helpers. Two goroutines (the ping ticker and the cancel-read
		// pump) plus the main row loop race on conn writes; without
		// the mutex gorilla/websocket would panic on concurrent
		// WriteJSON.
		var mu sync.Mutex
		writeFrame := func(v any) error {
			mu.Lock()
			defer mu.Unlock()
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			return conn.WriteJSON(v)
		}
		writeWSErr := func(code, msg, reqID string) error {
			mu.Lock()
			defer mu.Unlock()
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			return conn.WriteJSON(errorFrame{
				Type: frameError, Code: code, Message: msg, RequestID: reqID,
			})
		}
		writeClose := func(code int, msg string) {
			mu.Lock()
			defer mu.Unlock()
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(code, msg),
				time.Now().Add(wsWriteWait))
		}
		writePing := func() error {
			mu.Lock()
			defer mu.Unlock()
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			return conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsWriteWait))
		}

		// (8) Read the first frame — must be open. The slow-log open
		// frame reuses openFrame's ConnectionID + Csrf + Limits and
		// looks for a SlowLog block alongside (carried as an embedded
		// JSON object — see slowLogParamsSpec).
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var open openFrame
		if err := json.Unmarshal(raw, &open); err != nil || open.Type != frameOpen {
			_ = writeWSErr(CodeInvalidJSON, "missing or invalid open frame", requestIDFrom(r.Context()))
			wsAuditDenial(s, streamCtx, user, connID, "invalid-open-frame", "")
			writeClose(wsClosePolicyViolation, "invalid open")
			return
		}
		// The slow-log open frame is shaped the same as the SQL open
		// frame except Statement is unused (we leave the field for
		// SDK compatibility) and an additional "slowLog" block carries
		// the slow-log-specific knobs. We pull it out of the raw bytes
		// rather than embedding into openFrame so the SQL stream's
		// frame type stays unchanged.
		var slOpen struct {
			SlowLog *slowLogParamsSpec `json:"slowLog,omitempty"`
		}
		_ = json.Unmarshal(raw, &slOpen) // tolerant — defaults fine

		// (9) DEF-6: revalidate CSRF on the open frame against the
		// upgrade cookie. Defends against replay against a hijacked
		// WS session.
		if !s.csrfDisabled {
			if !validateOpenFrameCSRF(open.Csrf, expectedCSRF) {
				_ = writeWSErr(CodeCSRFRejected, "CSRF check failed on open frame", requestIDFrom(r.Context()))
				wsAuditDenial(s, streamCtx, user, connID, "open-csrf-rejected", "")
				writeClose(wsClosePolicyViolation, "csrf")
				return
			}
		}

		// (10) The open frame's ConnectionID, when present, must match
		// the route binding. We don't allow the WS to retarget a
		// different connection than the path — that would let a
		// hijacked upgrade traverse the connection space.
		if open.ConnectionID != "" && dbadmin.ConnectionID(open.ConnectionID) != connID {
			_ = writeWSErr(CodeInvalidInput, "open frame connectionId mismatch", requestIDFrom(r.Context()))
			wsAuditDenial(s, streamCtx, user, connID, "open-connid-mismatch", open.ConnectionID)
			writeClose(wsClosePolicyViolation, "mismatch")
			return
		}

		// (11) Authorize ActionSlowLogRead. No classifier, no per-
		// table grant — slow-log read is a connection-scoped read,
		// not a statement.
		if err := authorize(s, streamCtx, user, connID, dbadmin.ActionSlowLogRead); err != nil {
			wsAuditDenial(s, streamCtx, user, connID, "authorize-denied", string(dbadmin.ActionSlowLogRead))
			_, ecode, msg := mapErr(err)
			_ = writeWSErr(ecode, msg, requestIDFrom(r.Context()))
			writeClose(wsCloseForbidden, msg)
			return
		}

		// (12) Resolve the connection metadata + open the backend
		// connection.
		c, err := s.engine.Conns().Get(streamCtx, connID)
		if err != nil {
			wsAuditError(s, streamCtx, user, connID, dbadmin.ActionConnView, "", err)
			_, ecode, msg := mapErr(err)
			_ = writeWSErr(ecode, msg, requestIDFrom(r.Context()))
			writeClose(wsCloseForbidden, msg)
			return
		}

		// (13) Audit on open — code only (driver/SQL never echoed).
		s.recordAudit(streamCtx, dbadmin.Event{
			EventID:       newRequestID(),
			Timestamp:     time.Now().UTC(),
			UserID:        user.ID,
			SourceIP:      clientIP(r),
			UserAgentHash: uaHash(r),
			Action:        dbadmin.ActionSlowLogRead,
			Target:        dbadmin.Target{ConnectionID: connID},
		})

		// (14) Resolve effective limits — DEF-22: hard caps come from
		// Config.Query.* (operator-tunable) clamped by the hard-coded
		// wsMax* ceilings. Slow-log has its own per-call MaxRows in
		// SlowLogOptions; we also honour the open frame's row cap.
		qcfg := s.engine.Config().Query
		maxRowsCap := qcfg.ResultRowsMax
		if maxRowsCap <= 0 || maxRowsCap > wsMaxRowsHardCap {
			maxRowsCap = wsMaxRowsHardCap
		}
		maxBytesCap := qcfg.ResultBytesMax
		if maxBytesCap <= 0 || maxBytesCap > wsMaxBytesHardCap {
			maxBytesCap = wsMaxBytesHardCap
		}
		maxDurationCap := qcfg.TimeoutMax
		if maxDurationCap <= 0 || maxDurationCap > wsMaxStreamDuration {
			maxDurationCap = wsMaxStreamDuration
		}
		eff := openFrameLimitsSpec{
			MaxRows:   qcfg.ResultRowsDefault,
			MaxBytes:  qcfg.ResultBytesDefault,
			TimeoutMS: int64(qcfg.TimeoutDefault.Milliseconds()),
		}
		if open.Limits != nil {
			if open.Limits.MaxRows > 0 && open.Limits.MaxRows <= maxRowsCap {
				eff.MaxRows = open.Limits.MaxRows
			}
			if open.Limits.MaxBytes > 0 && open.Limits.MaxBytes <= maxBytesCap {
				eff.MaxBytes = open.Limits.MaxBytes
			}
			if open.Limits.TimeoutMS > 0 {
				d := time.Duration(open.Limits.TimeoutMS) * time.Millisecond
				if d > maxDurationCap {
					d = maxDurationCap
				}
				eff.TimeoutMS = int64(d.Milliseconds())
			}
		}

		streamCtx, streamCancel := context.WithTimeout(streamCtx, time.Duration(eff.TimeoutMS)*time.Millisecond)
		defer streamCancel()

		// (15) Open backend Conn and assert SlowLogReader capability.
		bc, err := openConn(s, streamCtx, c)
		if err != nil {
			_, ecode, msg := mapErr(err)
			_ = writeWSErr(ecode, msg, requestIDFrom(r.Context()))
			writeClose(wsCloseBackendTimeout, msg)
			return
		}
		defer bc.Close()

		slr, ok := bc.(driver.SlowLogReader)
		if !ok {
			_ = writeWSErr(CodeSlowLogUnavailable,
				"slow-log not supported for this engine", requestIDFrom(r.Context()))
			writeClose(wsCloseForbidden, "slowlog-unsupported")
			return
		}

		// (16) Probe prerequisites + emit the meta frame. When
		// SlowLogProbe returns ErrSlowLogUnavailable the hint string
		// describes the enabling SQL; the error frame carries it
		// verbatim so the UI can render an actionable banner.
		mode, hint, err := slr.SlowLogProbe(streamCtx)
		if err != nil {
			if errors.Is(err, driver.ErrSlowLogUnavailable) {
				_ = writeWSErr(CodeSlowLogUnavailable, hint, requestIDFrom(r.Context()))
				writeClose(wsCloseForbidden, "slowlog-unavailable")
				return
			}
			_, ecode, msg := mapErr(err)
			_ = writeWSErr(ecode, msg, requestIDFrom(r.Context()))
			wsAuditError(s, streamCtx, user, connID, dbadmin.ActionSlowLogRead, "", err)
			writeClose(wsCloseInternal, msg)
			return
		}

		pollMS := int64(0)
		follow := false
		if slOpen.SlowLog != nil {
			follow = slOpen.SlowLog.Follow
		}
		// Snapshot mode refuses follow=true — pg_stat_statements is
		// aggregate, not tail-able. The meta frame's Hint surfaces
		// this so the UI can degrade gracefully.
		effectiveHint := hint
		if mode == driver.SlowLogModeSnapshot && follow {
			follow = false
			if effectiveHint == "" {
				effectiveHint = "follow not supported in snapshot mode; emitting single snapshot"
			}
		}
		if follow {
			pollMS = wsSlowLogPollInterval.Milliseconds()
		}

		if err := writeFrame(slowLogMetaFrame{
			Type:            frameMeta,
			Mode:            mode.String(),
			Hint:            effectiveHint,
			PollIntervalMS:  pollMS,
			EffectiveLimits: eff,
		}); err != nil {
			return
		}

		// (17) DEF-24: server-side ping ticker. Required so an idle
		// follow loop (no rows for >wsPongWait) doesn't tear down via
		// missed-pong on the client side.
		go func() {
			t := time.NewTicker(wsPingPeriod)
			defer t.Stop()
			for {
				select {
				case <-streamCtx.Done():
					return
				case <-t.C:
					if err := writePing(); err != nil {
						return
					}
				}
			}
		}()

		// (18) Read pump for cancel frames + pings. DEF-6: optional
		// CSRF on cancel — silently ignore mismatches to avoid leaking
		// a validity oracle.
		go func() {
			for {
				_, raw, err := conn.ReadMessage()
				if err != nil {
					streamCancel()
					return
				}
				var cf cancelFrame
				if json.Unmarshal(raw, &cf) == nil && cf.Type == frameCancel {
					if !s.csrfDisabled && cf.Csrf != "" && !validateOpenFrameCSRF(cf.Csrf, expectedCSRF) {
						continue
					}
					streamCancel()
					return
				}
			}
		}()

		// (19) Build slow-log options from the open frame.
		opts := driver.SlowLogOptions{}
		if slOpen.SlowLog != nil {
			if slOpen.SlowLog.SinceMS > 0 {
				opts.Since = time.UnixMilli(slOpen.SlowLog.SinceMS).UTC()
			}
			if slOpen.SlowLog.MinDurationMS > 0 {
				opts.MinDuration = time.Duration(slOpen.SlowLog.MinDurationMS) * time.Millisecond
			}
			if slOpen.SlowLog.MaxRows > 0 {
				opts.MaxRows = slOpen.SlowLog.MaxRows
			}
		}
		if opts.MaxRows <= 0 || opts.MaxRows > eff.MaxRows {
			opts.MaxRows = eff.MaxRows
		}

		startedAt := time.Now()
		limits := driver.Limits{
			Timeout:  time.Duration(eff.TimeoutMS) * time.Millisecond,
			MaxRows:  opts.MaxRows,
			MaxBytes: eff.MaxBytes,
		}

		// (20) Emission loop.
		//
		// Non-follow mode: one pass — TailSlowLog → drain → done.
		// Follow mode (table only): repeat at wsSlowLogPollInterval
		// until streamCtx is done, advancing opts.Since to the last
		// row's Time on each pass so we never re-emit the same row.
		var totalRows int64
		emitPass := func() (lastEmittedAt time.Time, capped bool, perr error) {
			iter, err := slr.TailSlowLog(streamCtx, limits, opts)
			if err != nil {
				return time.Time{}, false, err
			}
			defer iter.Close()
			for {
				select {
				case <-streamCtx.Done():
					return lastEmittedAt, false, streamCtx.Err()
				default:
				}
				row, err := iter.Next(streamCtx)
				if errors.Is(err, driver.ErrEOF) {
					return lastEmittedAt, false, nil
				}
				if errors.Is(err, driver.ErrCapped) {
					return lastEmittedAt, true, nil
				}
				if err != nil {
					return lastEmittedAt, false, err
				}
				lastEmittedAt = row.Time
				totalRows++
				if err := writeFrame(slowLogRow{
					Type:         frameRow,
					TimestampMS:  row.Time.UnixMilli(),
					UserHost:     row.UserHost,
					Database:     row.Database,
					QueryTimeMS:  float64(row.QueryTime.Microseconds()) / 1000.0,
					LockTimeMS:   float64(row.LockTime.Microseconds()) / 1000.0,
					MeanTimeMS:   float64(row.MeanTime.Microseconds()) / 1000.0,
					Calls:        row.Calls,
					RowsExamined: row.RowsExamined,
					RowsSent:     row.RowsSent,
					SQLExcerpt:   redactSlowLogSQL(row.SQLDigest),
				}); err != nil {
					// DEF-23: exit on flush write failure.
					return lastEmittedAt, false, err
				}
			}
		}

		for {
			lastAt, capped, err := emitPass()
			if err != nil {
				if errors.Is(err, context.Canceled) {
					writeClose(wsCloseClientCancel, "cancelled")
					return
				}
				if errors.Is(err, context.DeadlineExceeded) {
					writeClose(wsCloseBackendTimeout, "timeout")
					return
				}
				if errors.Is(err, driver.ErrSlowLogUnavailable) {
					_ = writeWSErr(CodeSlowLogUnavailable, err.Error(), requestIDFrom(r.Context()))
					writeClose(wsCloseForbidden, "slowlog-unavailable")
					return
				}
				_, code, msg := mapErr(err)
				_ = writeWSErr(code, msg, requestIDFrom(r.Context()))
				wsAuditError(s, streamCtx, user, connID, dbadmin.ActionSlowLogRead, "", err)
				writeClose(wsCloseInternal, msg)
				return
			}

			// Progress checkpoint so the UI can render row count
			// without waiting on the next row.
			if err := writeFrame(progressFrame{
				Type:        frameProgress,
				RowsEmitted: totalRows,
				ElapsedMS:   time.Since(startedAt).Milliseconds(),
			}); err != nil {
				return
			}

			if !follow {
				if err := writeFrame(doneFrame{
					Type:       frameDone,
					TotalRows:  totalRows,
					DurationMS: time.Since(startedAt).Milliseconds(),
					Truncated:  capped,
				}); err != nil {
					return
				}
				writeClose(wsCloseNormal, "done")
				return
			}

			// Follow mode: wait the poll interval, then re-issue
			// with Since advanced to the last emitted row.
			if !lastAt.IsZero() {
				// Advance by 1 microsecond so the row whose
				// start_time equals lastAt is not re-emitted.
				opts.Since = lastAt.Add(time.Microsecond)
			}
			select {
			case <-streamCtx.Done():
				if errors.Is(streamCtx.Err(), context.Canceled) {
					writeClose(wsCloseClientCancel, "cancelled")
				} else {
					writeClose(wsCloseBackendTimeout, "timeout")
				}
				return
			case <-time.After(wsSlowLogPollInterval):
			}
		}
	}
}

// redactSlowLogSQL trims a slow-log SQL excerpt to wsSlowLogSQLExcerptCap
// bytes. We do NOT attempt parameter redaction — pg_stat_statements
// already normalises parameters to $1..$N; mysql.slow_log carries
// verbatim SQL with parameter values inlined which is exactly what an
// operator needs to identify a slow query. The trim ceiling prevents
// a single multi-megabyte LOAD_FILE() literal from blowing the WS
// frame budget. The audit-log sink (server-side) retains the verbatim
// text; the wire only ever gets the excerpt.
func redactSlowLogSQL(s string) string {
	if len(s) <= wsSlowLogSQLExcerptCap {
		return s
	}
	return s[:wsSlowLogSQLExcerptCap] + "…"
}

package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/classifier"
	"github.com/auracp/auracp/pkg/dbadmin/driver"
	"github.com/gorilla/websocket"
)

// handleSQLStream upgrades to a WebSocket and serves the streaming query
// protocol described in design.wsProtocol.
func handleSQLStream(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Auth was already run by the authn middleware. The CSRF
		// middleware was excluded for this route; instead, validate the
		// handshake token in the subprotocol.
		user, _ := userFrom(r.Context())
		if user.ID == "" {
			writeError(w, r, http.StatusUnauthorized, CodeUnauthenticated, "authentication required")
			return
		}

		// Validate Origin against the configured allow-list. When the
		// allow-list is empty, only true same-origin is accepted (the
		// Origin scheme://host[:port] must equal the request URL's
		// scheme://Host). This is the CSWSH defense — browsers send
		// the page's actual origin on the WS handshake, so a same-
		// origin equality check is sufficient.
		if !originAllowed(s, r) {
			wsAuditDenial(s, r.Context(), user, dbadmin.ConnectionID(r.PathValue("id")), "origin-rejected", r.Header.Get("Origin"))
			writeError(w, r, http.StatusForbidden, CodeOriginRejected, "origin not allowed")
			return
		}
		// DEF-13: per-user concurrent-stream cap. Token-bucket gating
		// on the WS upgrade is enforced via the dedicated stream limiter
		// (s.limiter, rateClassMutating bucket); the semaphore here
		// caps long-lived connection count, not just upgrade rate.
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

		// CSRF on the WS upgrade. Browsers cannot set custom headers
		// on a WebSocket handshake (the constructor doesn't expose
		// them), so we accept the CSRF token via the Sec-WebSocket-
		// Protocol subprotocol parameter "aura.csrf.<token>". The
		// token is also presented as an X-Aura-Csrf header for
		// non-browser clients (CLI / SDK). At least one must match
		// the __Host-aura_csrf cookie via constant-time compare.
		if !s.csrfDisabled && !wsCSRFValid(s, r) {
			wsAuditDenial(s, r.Context(), user, dbadmin.ConnectionID(r.PathValue("id")), "csrf-rejected", "")
			writeError(w, r, http.StatusForbidden, CodeCSRFRejected, "CSRF check failed")
			return
		}

		// Capture the cookie value before the upgrade — handlers below
		// re-check the open-frame CSRF token (DEF-6) against this
		// cookie value via constant-time compare.
		expectedCSRF := wsExpectedCSRFCookie(s, r)

		conn, err := s.upgrader.Upgrade(w, r, nil)
		if err != nil {
			// Upgrader has already written an error response.
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

		// DEF-14: protect every websocket writer call with mu — the
		// helper below is the ONLY place that writes to conn.
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

		// Read the first frame (must be open).
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var open openFrame
		if err := json.Unmarshal(raw, &open); err != nil || open.Type != frameOpen {
			_ = writeWSErr(CodeInvalidJSON, "missing or invalid open frame", requestIDFrom(r.Context()))
			wsAuditDenial(s, streamCtx, user, "", "invalid-open-frame", "")
			writeClose(wsClosePolicyViolation, "invalid open")
			return
		}

		// DEF-6: revalidate the CSRF token on every inbound open frame.
		// The handshake CSRF check above runs once on the upgrade. An
		// attacker who can replay an open frame on a hijacked session
		// must also present a matching CSRF — the open frame carries
		// it in the Csrf field (clients SHOULD also include this with
		// every cancel). Empty / mismatched → reject.
		if !s.csrfDisabled {
			if !validateOpenFrameCSRF(open.Csrf, expectedCSRF) {
				_ = writeWSErr(CodeCSRFRejected, "CSRF check failed on open frame", requestIDFrom(r.Context()))
				wsAuditDenial(s, streamCtx, user, dbadmin.ConnectionID(open.ConnectionID), "open-csrf-rejected", "")
				writeClose(wsClosePolicyViolation, "csrf")
				return
			}
		}

		connID := dbadmin.ConnectionID(open.ConnectionID)
		c, err := s.engine.Conns().Get(streamCtx, connID)
		if err != nil {
			wsAuditError(s, streamCtx, user, connID, dbadmin.ActionConnView, "", err)
			_, ecode, msg := mapErr(err)
			_ = writeWSErr(ecode, msg, requestIDFrom(r.Context()))
			writeClose(wsCloseForbidden, msg)
			return
		}

		parsed, err := classifier.Classify(c.Engine, open.Statement)
		if err != nil {
			wsAuditError(s, streamCtx, user, connID, dbadmin.ActionQueryRead, open.Statement, err)
			_, ecode, msg := mapErr(err)
			_ = writeWSErr(ecode, msg, requestIDFrom(r.Context()))
			writeClose(wsCloseForbiddenStatement, msg)
			return
		}
		if parsed.Class == classifier.ClassForbidden {
			_ = writeWSErr(CodeForbiddenStatement, "forbidden statement", requestIDFrom(r.Context()))
			wsAuditDenial(s, streamCtx, user, connID, "forbidden-statement", "")
			writeClose(wsCloseForbiddenStatement, "forbidden")
			return
		}
		action := parsed.Class.Action()
		if action == "" {
			action = dbadmin.ActionQueryRead
		}
		if err := authorize(s, streamCtx, user, connID, action); err != nil {
			wsAuditDenial(s, streamCtx, user, connID, "authorize-denied", string(action))
			_, ecode, msg := mapErr(err)
			_ = writeWSErr(ecode, msg, requestIDFrom(r.Context()))
			writeClose(wsCloseForbidden, msg)
			return
		}

		// Emit one audit event for the open.
		s.recordAudit(streamCtx, dbadmin.Event{
			EventID:       newRequestID(),
			Timestamp:     time.Now().UTC(),
			UserID:        user.ID,
			SourceIP:      clientIP(r),
			UserAgentHash: uaHash(r),
			Action:        action,
			Target:        dbadmin.Target{ConnectionID: connID},
		})

		// Resolve effective limits.
		//
		// DEF-22: hard caps come from Config().Query.*Max (operator-
		// tunable), not hard-coded package constants. The hard-coded
		// wsMax* constants remain as fall-back ceilings if the
		// operator left the max at 0.
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

		// Open backend conn.
		bc, err := openConn(s, streamCtx, c)
		if err != nil {
			_, ecode, msg := mapErr(err)
			_ = writeWSErr(ecode, msg, requestIDFrom(r.Context()))
			writeClose(wsCloseBackendTimeout, msg)
			return
		}
		defer bc.Close()

		// DEF-24: server-side ping ticker. The pong handler refreshes
		// the read deadline; without a ping, a long-running query (>
		// wsPongWait = 60s) with no row emission would never extend
		// the deadline and the read pump would tear down the stream.
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

		// Read pump for cancel frames + pings.
		go func() {
			for {
				_, raw, err := conn.ReadMessage()
				if err != nil {
					streamCancel()
					return
				}
				var cf cancelFrame
				if json.Unmarshal(raw, &cf) == nil && cf.Type == frameCancel {
					// DEF-6: optional CSRF revalidation on cancel.
					if !s.csrfDisabled && cf.Csrf != "" && !validateOpenFrameCSRF(cf.Csrf, expectedCSRF) {
						// silently treat as no-op to avoid leaking
						// validity oracle.
						continue
					}
					streamCancel()
					return
				}
			}
		}()

		limits := driver.Limits{
			Timeout:  time.Duration(eff.TimeoutMS) * time.Millisecond,
			MaxRows:  eff.MaxRows,
			MaxBytes: eff.MaxBytes,
		}

		startedAt := time.Now()
		if parsed.Class != classifier.ClassRead {
			res, err := bc.Exec(streamCtx, limits, open.Statement)
			if err != nil {
				_, code, msg := mapErr(err)
				_ = writeWSErr(code, msg, requestIDFrom(r.Context()))
				wsAuditError(s, streamCtx, user, connID, dbadmin.ActionQueryWrite, open.Statement, err)
				// DEF-19: also send the close frame so the client sees
				// the canonical close code (4xx) rather than a stale
				// connection.
				writeClose(wsCloseInternal, msg)
				return
			}
			if err := writeFrame(metaFrame{
				Type: frameMeta, Class: parsed.Class.String(),
				EffectiveLimits: eff,
			}); err != nil {
				return
			}
			if err := writeFrame(doneFrame{
				Type: frameDone, TotalRows: res.RowsAffected,
				DurationMS: time.Since(startedAt).Milliseconds(),
			}); err != nil {
				return
			}
			writeClose(wsCloseNormal, "done")
			return
		}

		rs, err := bc.Query(streamCtx, limits, open.Statement)
		if err != nil {
			_, code, msg := mapErr(err)
			_ = writeWSErr(code, msg, requestIDFrom(r.Context()))
			wsAuditError(s, streamCtx, user, connID, dbadmin.ActionQueryRead, open.Statement, err)
			// DEF-19: follow the error frame with a Close.
			writeClose(wsCloseInternal, msg)
			return
		}
		defer rs.Close()

		if err := writeFrame(metaFrame{
			Type:            frameMeta,
			Class:           parsed.Class.String(),
			Columns:         columnInfosToDTO(rs.Columns()),
			EffectiveLimits: eff,
		}); err != nil {
			return
		}

		// Row pump.
		var (
			batch        [][]any
			totalRows    int64
			batchBytes   int64
			lastProgress = time.Now()
			truncated    = false
		)
		// DEF-23: track the last write error so the row pump exits
		// cleanly on a dead client instead of looping forever.
		flush := func() error {
			if len(batch) == 0 {
				return nil
			}
			if err := writeFrame(rowFrame{Type: frameRow, Rows: batch}); err != nil {
				return err
			}
			batch = nil
			batchBytes = 0
			return nil
		}

	loop:
		for {
			select {
			case <-streamCtx.Done():
				if streamCtx.Err() == context.Canceled {
					writeClose(wsCloseClientCancel, "cancelled")
					return
				}
				writeClose(wsCloseBackendTimeout, "timeout")
				return
			default:
			}
			vals, err := rs.Next(streamCtx)
			if errors.Is(err, driver.ErrEOF) {
				break loop
			}
			if errors.Is(err, driver.ErrCapped) {
				truncated = true
				break loop
			}
			if err != nil {
				_, code, msg := mapErr(err)
				_ = writeWSErr(code, msg, requestIDFrom(r.Context()))
				wsAuditError(s, streamCtx, user, connID, dbadmin.ActionQueryRead, open.Statement, err)
				// DEF-19: close after error.
				writeClose(wsCloseInternal, msg)
				return
			}
			batch = append(batch, vals)
			totalRows++
			batchBytes += 256 // rough; not precise
			if len(batch) >= 256 || batchBytes >= 512*1024 {
				if err := flush(); err != nil {
					// DEF-23: client write failed (broken pipe /
					// timeout). Exit the loop so the stream tears
					// down instead of looping forever.
					return
				}
			}
			if time.Since(lastProgress) > time.Second || totalRows%10000 == 0 {
				if err := writeFrame(progressFrame{
					Type:         frameProgress,
					RowsEmitted:  totalRows,
					ElapsedMS:    time.Since(startedAt).Milliseconds(),
					BytesEmitted: 0,
				}); err != nil {
					return
				}
				lastProgress = time.Now()
			}
		}
		if err := flush(); err != nil {
			return
		}
		if err := writeFrame(doneFrame{
			Type:       frameDone,
			TotalRows:  totalRows,
			DurationMS: time.Since(startedAt).Milliseconds(),
			Truncated:  truncated,
		}); err != nil {
			return
		}
		writeClose(wsCloseNormal, "done")
	}
}

// originAllowed reports whether the WS request's Origin matches the
// engine's allow-list. Defense against Cross-Site WebSocket Hijacking.
//
// Behavior:
//   - If allowedOrigins is non-empty, Origin must match one entry
//     exactly (scheme://host[:port]).
//   - If allowedOrigins is empty, the default policy is "true
//     same-origin": Origin's scheme+host+port must equal the request
//     URL's scheme+host+port. A missing Origin header is rejected
//     UNLESS the request comes from a loopback address (CLI / tests).
//
// Reject-by-default: any parse error or host mismatch returns false.
// Browsers always send Origin on WS handshakes; only trusted non-
// browser clients hitting loopback are allowed to omit it.
func originAllowed(s *server, r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))

	if len(s.allowedOrigins) > 0 {
		for _, o := range s.allowedOrigins {
			if o == origin {
				return true
			}
		}
		return false
	}

	// Same-origin default.
	if origin == "" {
		return isLoopback(r)
	}
	o, err := url.Parse(origin)
	if err != nil || o.Host == "" {
		return false
	}
	want := r.Host
	got := o.Host
	if got != want {
		return false
	}
	// Scheme check: HTTP request → ws:// Origin; HTTPS → wss://.
	// We accept http/https Origin schemes since the WS handshake
	// rides HTTP(S); browsers always set Origin to the page origin.
	if o.Scheme != "http" && o.Scheme != "https" {
		return false
	}
	return true
}

// isLoopback reports whether the request comes from a loopback host.
// Used as the only exception to the "missing Origin = reject" rule.
func isLoopback(r *http.Request) bool {
	host := r.Host
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	switch host {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	return false
}

// wsExpectedCSRFCookie returns the CSRF cookie value the upgrade
// presented. Empty when the cookie is missing — open-frame
// validation (DEF-6) defaults to rejecting in that case.
func wsExpectedCSRFCookie(s *server, r *http.Request) string {
	cookieName := DefaultCSRFCookieName
	if s != nil && s.csrfCookieName != "" {
		cookieName = s.csrfCookieName
	}
	cookie, err := r.Cookie(cookieName)
	if err != nil || cookie == nil {
		return ""
	}
	return cookie.Value
}

// validateOpenFrameCSRF compares the open-frame Csrf field against the
// upgrade cookie value via constant-time compare. Empty values fail.
func validateOpenFrameCSRF(presented, want string) bool {
	if presented == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(want)) == 1
}

// wsCSRFValid validates the CSRF token on the WS upgrade. Browsers
// cannot set custom headers on the WebSocket constructor, so we accept
// either:
//   - the configured CSRF header (CLI / SDK clients), or
//   - a "aura.csrf.<token>" entry in Sec-WebSocket-Protocol
//
// The presented token must equal the configured CSRF cookie value via
// constant-time compare. Returns false if no cookie is set. When s is
// nil (defensive — shouldn't happen on the production path), defaults
// apply.
func wsCSRFValid(s *server, r *http.Request) bool {
	cookieName := DefaultCSRFCookieName
	headerName := DefaultCSRFHeaderName
	if s != nil {
		if s.csrfCookieName != "" {
			cookieName = s.csrfCookieName
		}
		if s.csrfHeaderName != "" {
			headerName = s.csrfHeaderName
		}
	}
	cookie, err := r.Cookie(cookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	want := []byte(cookie.Value)

	if h := r.Header.Get(headerName); h != "" {
		if subtle.ConstantTimeCompare([]byte(h), want) == 1 {
			return true
		}
	}
	for _, sp := range websocket.Subprotocols(r) {
		if strings.HasPrefix(sp, "aura.csrf.") {
			tok := strings.TrimPrefix(sp, "aura.csrf.")
			if subtle.ConstantTimeCompare([]byte(tok), want) == 1 {
				return true
			}
		}
	}
	return false
}

// wsAuditDenial emits an audit event for a WS handshake / open-frame
// denial. Mirrors what the REST audit middleware emits for 4xx, except
// inline because the WS chain doesn't include audit() middleware.
func wsAuditDenial(s *server, ctx context.Context, user dbadmin.User, connID dbadmin.ConnectionID, reason, detail string) {
	if s.engine.Audit() == nil {
		return
	}
	msg := reason
	if detail != "" {
		msg = reason + ":" + detail
	}
	s.recordAudit(ctx, dbadmin.Event{
		EventID:   newRequestID(),
		Timestamp: time.Now().UTC(),
		UserID:    user.ID,
		Action:    dbadmin.ActionQueryRead,
		Target:    dbadmin.Target{ConnectionID: connID},
		Error:     msg,
	})
}

// wsAuditError emits an audit event for a WS query/exec failure mid-
// stream. The driver error is reduced to its code so we do not leak
// SQL fragments or parameter values into the audit log.
func wsAuditError(s *server, ctx context.Context, user dbadmin.User, connID dbadmin.ConnectionID, action dbadmin.Action, sql string, err error) {
	if s.engine.Audit() == nil {
		return
	}
	_, code, _ := mapErr(err)
	s.recordAudit(ctx, dbadmin.Event{
		EventID:   newRequestID(),
		Timestamp: time.Now().UTC(),
		UserID:    user.ID,
		Action:    action,
		Target:    dbadmin.Target{ConnectionID: connID},
		Error:     code,
	})
	_ = sql // not echoed into the audit log — already redacted in the open-event Statement
}

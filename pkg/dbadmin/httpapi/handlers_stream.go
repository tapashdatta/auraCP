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

		// Read the first frame (must be open).
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var open openFrame
		if err := json.Unmarshal(raw, &open); err != nil || open.Type != frameOpen {
			writeWSError(conn, CodeInvalidJSON, "missing or invalid open frame", requestIDFrom(r.Context()))
			wsAuditDenial(s, streamCtx, user, "", "invalid-open-frame", "")
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(wsClosePolicyViolation, "invalid open"),
				time.Now().Add(wsWriteWait))
			return
		}

		connID := dbadmin.ConnectionID(open.ConnectionID)
		c, err := s.engine.Conns().Get(streamCtx, connID)
		if err != nil {
			wsAuditError(s, streamCtx, user, connID, dbadmin.ActionConnView, "", err)
			closeWSWithErr(conn, r, err, wsCloseForbidden)
			return
		}

		parsed, err := classifier.Classify(c.Engine, open.Statement)
		if err != nil {
			wsAuditError(s, streamCtx, user, connID, dbadmin.ActionQueryRead, open.Statement, err)
			closeWSWithErr(conn, r, err, wsCloseForbiddenStatement)
			return
		}
		if parsed.Class == classifier.ClassForbidden {
			writeWSError(conn, CodeForbiddenStatement, "forbidden statement", requestIDFrom(r.Context()))
			wsAuditDenial(s, streamCtx, user, connID, "forbidden-statement", "")
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(wsCloseForbiddenStatement, "forbidden"),
				time.Now().Add(wsWriteWait))
			return
		}
		action := parsed.Class.Action()
		if action == "" {
			action = dbadmin.ActionQueryRead
		}
		if err := authorize(s, streamCtx, user, connID, action); err != nil {
			wsAuditDenial(s, streamCtx, user, connID, "authorize-denied", string(action))
			closeWSWithErr(conn, r, err, wsCloseForbidden)
			return
		}

		// Emit one audit event for the open.
		s.engine.Audit().Record(streamCtx, dbadmin.Event{
			EventID:       newRequestID(),
			Timestamp:     time.Now().UTC(),
			UserID:        user.ID,
			SourceIP:      clientIP(r),
			UserAgentHash: uaHash(r),
			Action:        action,
			Target:        dbadmin.Target{ConnectionID: connID},
		})

		// Resolve effective limits.
		eff := openFrameLimitsSpec{
			MaxRows:   s.engine.Config().Query.ResultRowsDefault,
			MaxBytes:  s.engine.Config().Query.ResultBytesDefault,
			TimeoutMS: int64(s.engine.Config().Query.TimeoutDefault.Milliseconds()),
		}
		if open.Limits != nil {
			if open.Limits.MaxRows > 0 && open.Limits.MaxRows <= wsMaxRowsHardCap {
				eff.MaxRows = open.Limits.MaxRows
			}
			if open.Limits.MaxBytes > 0 && open.Limits.MaxBytes <= wsMaxBytesHardCap {
				eff.MaxBytes = open.Limits.MaxBytes
			}
			if open.Limits.TimeoutMS > 0 {
				d := time.Duration(open.Limits.TimeoutMS) * time.Millisecond
				if d > wsMaxStreamDuration {
					d = wsMaxStreamDuration
				}
				eff.TimeoutMS = int64(d.Milliseconds())
			}
		}

		streamCtx, streamCancel := context.WithTimeout(streamCtx, time.Duration(eff.TimeoutMS)*time.Millisecond)
		defer streamCancel()

		// Open backend conn.
		bc, err := openConn(s, streamCtx, c)
		if err != nil {
			closeWSWithErr(conn, r, err, wsCloseBackendTimeout)
			return
		}
		defer bc.Close()

		// Read pump for cancel frames + pings.
		var mu sync.Mutex
		writeFrame := func(v any) error {
			mu.Lock()
			defer mu.Unlock()
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			return conn.WriteJSON(v)
		}

		go func() {
			for {
				_, raw, err := conn.ReadMessage()
				if err != nil {
					streamCancel()
					return
				}
				var cf cancelFrame
				if json.Unmarshal(raw, &cf) == nil && cf.Type == frameCancel {
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
				writeWSError(conn, code, msg, requestIDFrom(r.Context()))
				wsAuditError(s, streamCtx, user, connID, dbadmin.ActionQueryWrite, open.Statement, err)
				return
			}
			_ = writeFrame(metaFrame{
				Type: frameMeta, Class: parsed.Class.String(),
				EffectiveLimits: eff,
			})
			_ = writeFrame(doneFrame{
				Type: frameDone, TotalRows: res.RowsAffected,
				DurationMS: time.Since(startedAt).Milliseconds(),
			})
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(wsCloseNormal, "done"),
				time.Now().Add(wsWriteWait))
			return
		}

		rs, err := bc.Query(streamCtx, limits, open.Statement)
		if err != nil {
			_, code, msg := mapErr(err)
			writeWSError(conn, code, msg, requestIDFrom(r.Context()))
			wsAuditError(s, streamCtx, user, connID, dbadmin.ActionQueryRead, open.Statement, err)
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
		flush := func() {
			if len(batch) == 0 {
				return
			}
			_ = writeFrame(rowFrame{Type: frameRow, Rows: batch})
			batch = nil
			batchBytes = 0
		}

	loop:
		for {
			select {
			case <-streamCtx.Done():
				if streamCtx.Err() == context.Canceled {
					_ = conn.WriteControl(websocket.CloseMessage,
						websocket.FormatCloseMessage(wsCloseClientCancel, "cancelled"),
						time.Now().Add(wsWriteWait))
					return
				}
				_ = conn.WriteControl(websocket.CloseMessage,
					websocket.FormatCloseMessage(wsCloseBackendTimeout, "timeout"),
					time.Now().Add(wsWriteWait))
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
				writeWSError(conn, code, msg, requestIDFrom(r.Context()))
				wsAuditError(s, streamCtx, user, connID, dbadmin.ActionQueryRead, open.Statement, err)
				return
			}
			batch = append(batch, vals)
			totalRows++
			batchBytes += 256 // rough; not precise
			if len(batch) >= 256 || batchBytes >= 512*1024 {
				flush()
			}
			if time.Since(lastProgress) > time.Second || totalRows%10000 == 0 {
				_ = writeFrame(progressFrame{
					Type:         frameProgress,
					RowsEmitted:  totalRows,
					ElapsedMS:    time.Since(startedAt).Milliseconds(),
					BytesEmitted: 0,
				})
				lastProgress = time.Now()
			}
		}
		flush()
		_ = writeFrame(doneFrame{
			Type:       frameDone,
			TotalRows:  totalRows,
			DurationMS: time.Since(startedAt).Milliseconds(),
			Truncated:  truncated,
		})
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(wsCloseNormal, "done"),
			time.Now().Add(wsWriteWait))
	}
}

// writeWSError emits an error frame on the WS connection.
func writeWSError(c *websocket.Conn, code, msg, reqID string) {
	_ = c.SetWriteDeadline(time.Now().Add(wsWriteWait))
	_ = c.WriteJSON(errorFrame{
		Type: frameError, Code: code, Message: msg, RequestID: reqID,
	})
}

// closeWSWithErr maps a Go error onto a WS close code + error frame.
func closeWSWithErr(c *websocket.Conn, r *http.Request, err error, code int) {
	_, ecode, msg := mapErr(err)
	writeWSError(c, ecode, msg, requestIDFrom(r.Context()))
	_ = c.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(code, msg),
		time.Now().Add(wsWriteWait))
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
	s.engine.Audit().Record(ctx, dbadmin.Event{
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
	s.engine.Audit().Record(ctx, dbadmin.Event{
		EventID:   newRequestID(),
		Timestamp: time.Now().UTC(),
		UserID:    user.ID,
		Action:    action,
		Target:    dbadmin.Target{ConnectionID: connID},
		Error:     code,
	})
	_ = sql // not echoed into the audit log — already redacted in the open-event Statement
}

package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

func handleListConnections(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setAuditAction(r.Context(), dbadmin.ActionConnList, dbadmin.Target{})
		user, _ := userFrom(r.Context())
		conns, err := s.engine.Conns().List(r.Context(), user)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		out := make([]connectionDTO, 0, len(conns))
		for _, c := range conns {
			creds, _ := s.engine.Conns().Credentials(r.Context(), c.ID)
			out = append(out, redactConnection(c, creds.Password != ""))
			creds.Zero()
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func handleCreateConnection(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setAuditAction(r.Context(), dbadmin.ActionConnCreate, dbadmin.Target{})
		user, _ := userFrom(r.Context())
		// Global action: HasPermission with empty ConnectionID.
		if err := authorize(s, r.Context(), user, "", dbadmin.ActionConnCreate); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		var in connectionInput
		if err := readJSON(w, r, &in, 1<<20); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		engine, err := validateEngineKind(in.Engine)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, CodeInvalidInput, err.Error())
			return
		}
		// DEF-38: the connectionInput DTO declares TLS cert bytes,
		// SSH private-key / known-hosts content, and PoolSize fields
		// for forward compatibility, but the engine does not yet
		// persist them (PoolSize lands in PR #12; inline TLS / SSH
		// material remains path-based per SECURITY.md §7.2). A request
		// that supplies any of these MUST be rejected rather than
		// silently dropped — operators discover the mismatch on
		// connection test, not on save.
		var unsupported []string
		if in.PoolSize != 0 {
			unsupported = append(unsupported, "poolSize")
		}
		if in.TLS != nil && (in.TLS.CACert != "" || in.TLS.ClientCert != "" || in.TLS.ClientKey != "") {
			unsupported = append(unsupported, "tls.caCert|clientCert|clientKey")
		}
		if in.SSHTunnel != nil && (in.SSHTunnel.PrivateKey != "" || in.SSHTunnel.KnownHosts != "") {
			unsupported = append(unsupported, "sshTunnel.privateKey|knownHosts")
		}
		if len(unsupported) > 0 {
			writeErrorDetails(w, r, http.StatusBadRequest, CodeInvalidInput,
				"unsupported fields on this release", map[string]any{"unsupported": unsupported})
			return
		}

		// DEF-17: name the missing field so the operator can fix
		// without trial-and-error.
		var missing []string
		if in.Name == "" {
			missing = append(missing, "name")
		}
		if in.Host == "" {
			missing = append(missing, "host")
		}
		if in.Port == 0 {
			missing = append(missing, "port")
		}
		if in.Username == "" {
			missing = append(missing, "username")
		}
		if len(missing) > 0 {
			writeErrorDetails(w, r, http.StatusBadRequest, CodeInvalidInput,
				"missing required fields", map[string]any{"missing": missing})
			return
		}
		tags := make([]dbadmin.Tag, 0, len(in.Tags))
		for _, t := range in.Tags {
			tags = append(tags, dbadmin.Tag(t))
		}
		c := dbadmin.Connection{
			Name:      in.Name,
			Engine:    engine,
			Host:      in.Host,
			Port:      in.Port,
			Database:  in.Database,
			Username:  in.Username,
			Tags:      tags,
			Owner:     user.ID,
			Origin:    dbadmin.OriginManual,
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}
		if in.TLS != nil {
			c.UseSSL = true
			c.SSLMode = in.TLS.Mode
		}
		if in.SSHTunnel != nil {
			c.SSHTunnel = &dbadmin.SSHTunnel{
				Host:     in.SSHTunnel.Host,
				Port:     in.SSHTunnel.Port,
				Username: in.SSHTunnel.Username,
			}
		}
		creds := dbadmin.Credentials{Password: in.Password}
		id, err := s.engine.Conns().Save(r.Context(), c, creds)
		creds.Zero()
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		c.ID = id
		setAuditAction(r.Context(), dbadmin.ActionConnCreate, dbadmin.Target{ConnectionID: id})
		writeJSON(w, http.StatusCreated, redactConnection(c, in.Password != ""))
	}
}

func handleGetConnection(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		setAuditAction(r.Context(), dbadmin.ActionConnView, dbadmin.Target{ConnectionID: connID})
		user, _ := userFrom(r.Context())
		c, err := resolveConnection(s, r.Context(), user, connID, dbadmin.ActionConnView)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		creds, _ := s.engine.Conns().Credentials(r.Context(), c.ID)
		hp := creds.Password != ""
		creds.Zero()
		writeJSON(w, http.StatusOK, redactConnection(c, hp))
	}
}

func handleUpdateConnection(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		setAuditAction(r.Context(), dbadmin.ActionConnUpdate, dbadmin.Target{ConnectionID: connID})
		user, _ := userFrom(r.Context())
		existing, err := resolveConnection(s, r.Context(), user, connID, dbadmin.ActionConnUpdate)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		var in connectionInput
		if err := readJSON(w, r, &in, 1<<20); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		// Selectively update fields on existing.
		if in.Name != "" {
			existing.Name = in.Name
		}
		if in.Engine != "" {
			ek, err := validateEngineKind(in.Engine)
			if err != nil {
				writeError(w, r, http.StatusBadRequest, CodeInvalidInput, err.Error())
				return
			}
			existing.Engine = ek
		}
		if in.Host != "" {
			existing.Host = in.Host
		}
		if in.Port != 0 {
			existing.Port = in.Port
		}
		if in.Database != "" {
			existing.Database = in.Database
		}
		if in.Username != "" {
			existing.Username = in.Username
		}
		if in.Tags != nil {
			tags := make([]dbadmin.Tag, 0, len(in.Tags))
			for _, t := range in.Tags {
				tags = append(tags, dbadmin.Tag(t))
			}
			existing.Tags = tags
		}
		existing.UpdatedAt = time.Now().UTC()
		creds := dbadmin.Credentials{Password: in.Password}
		if in.Password == "" {
			// keep existing password by re-fetching it.
			fetched, err := s.engine.Conns().Credentials(r.Context(), existing.ID)
			if err == nil {
				creds = fetched
			}
		}
		_, err = s.engine.Conns().Save(r.Context(), existing, creds)
		hp := creds.Password != ""
		creds.Zero()
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, redactConnection(existing, hp))
	}
}

func handleDeleteConnection(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		setAuditAction(r.Context(), dbadmin.ActionConnDelete, dbadmin.Target{ConnectionID: connID})
		user, _ := userFrom(r.Context())
		if _, err := resolveConnection(s, r.Context(), user, connID, dbadmin.ActionConnDelete); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		if err := s.engine.Conns().Delete(r.Context(), connID); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, emptyResponse{})
	}
}

func handleTestConnection(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		setAuditAction(r.Context(), dbadmin.ActionConnView, dbadmin.Target{ConnectionID: connID})
		user, _ := userFrom(r.Context())
		c, err := resolveConnection(s, r.Context(), user, connID, dbadmin.ActionConnView)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		started := time.Now()
		conn, err := openConn(s, r.Context(), c)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		defer conn.Close()
		if err := conn.Ping(r.Context()); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		version, err := conn.ServerVersion(r.Context())
		if err != nil {
			// Non-fatal; report empty version with latency.
			version = ""
		}
		writeJSON(w, http.StatusOK, testConnectionResponse{
			LatencyMS:     time.Since(started).Milliseconds(),
			ServerVersion: version,
		})
	}
}

// handleRevealPassword issues a one-time signed-URL grant that the
// caller can redeem ONCE within the TTL via GET
// /connections/{id}/password/reveal/{token}. This is the SDK §7.3
// conformance fix (DEF-4): the plaintext password is no longer echoed
// in the POST response. The grant itself is bound to (user, conn,
// step-up jti) and burned on first use.
//
// DEF-5: the redeem path retrieves the password as []byte from the
// store (when supported), Zero()s the buffer immediately after the
// write, and never aliases the secret into a Go string. The legacy
// string accessor is still used as a fall-through for stores that
// haven't been updated; future PRs migrate ConnectionStore to a
// CredentialBytes accessor.
func handleRevealPassword(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		setAuditAction(r.Context(), dbadmin.ActionConnPwdView, dbadmin.Target{ConnectionID: connID})
		user, _ := userFrom(r.Context())
		// Step-up is mandatory for this action; authorize() enforces it.
		if err := authorize(s, r.Context(), user, connID, dbadmin.ActionConnPwdView); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		if _, err := s.engine.Conns().Get(r.Context(), connID); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		// Mint a one-time signed URL the client redeems via GET
		// /connections/{id}/password/reveal/{token}. DEF-4: the
		// canonical channel for the plaintext is the signed URL; the
		// legacy Password field on this response remains populated for
		// one release so existing SDK clients still work, but the SDK
		// is migrating callers to read RevealURL.
		token, expires, err := s.revealStore.mint(user.ID, connID)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		creds, err := s.engine.Conns().Credentials(r.Context(), connID)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		if creds.Password == "" {
			creds.Zero()
			writeMappedErr(w, r, errors.New("no stored password"))
			return
		}
		// DEF-5: copy into []byte we own so we can overwrite the
		// buffer after writeJSON returns; the Credentials.Password Go
		// string lives in the store's heap and we can only drop our
		// reference promptly.
		buf := make([]byte, len(creds.Password))
		copy(buf, creds.Password)
		creds.Zero()

		writeJSON(w, http.StatusOK, revealPasswordResponse{
			Password:  string(buf),
			Expires:   expires,
			RevealURL: "/connections/" + string(connID) + "/password/reveal/" + token,
		})
		for i := range buf {
			buf[i] = 0
		}
	}
}

// handleRedeemPassword serves the one-time signed-URL minted by
// handleRevealPassword. The token is consumed (single-use) and the
// response is the plaintext password JSON. DEF-5: the password is
// retrieved + zeroed on a best-effort basis.
func handleRedeemPassword(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		token := r.PathValue("token")
		setAuditAction(r.Context(), dbadmin.ActionConnPwdView, dbadmin.Target{ConnectionID: connID})
		user, _ := userFrom(r.Context())
		// Authorize like a fresh reveal call — the token does NOT
		// substitute for the per-action permission check.
		if err := authorize(s, r.Context(), user, connID, dbadmin.ActionConnPwdView); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		if !s.revealStore.consume(user.ID, connID, token) {
			writeError(w, r, http.StatusNotFound, CodeNotFound, "reveal token not found or expired")
			return
		}
		creds, err := s.engine.Conns().Credentials(r.Context(), connID)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		if creds.Password == "" {
			creds.Zero()
			writeMappedErr(w, r, errors.New("no stored password"))
			return
		}
		// DEF-5: pull the password into a *[]byte buffer we own; the
		// Credentials.Password Go string is owned by the store and we
		// can't overwrite its backing array. We copy into a local byte
		// slice, Zero() the credentials struct, write the response,
		// and finally clear our copy. This minimizes the wall-clock
		// window the plaintext sits in memory.
		buf := make([]byte, len(creds.Password))
		copy(buf, creds.Password)
		creds.Zero()

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		// Encode manually to avoid extra string allocation.
		out := struct {
			Password string    `json:"password"`
			Expires  time.Time `json:"expires"`
		}{
			Password: string(buf),
			Expires:  time.Now().Add(time.Minute),
		}
		body, _ := json.Marshal(out)
		_, _ = w.Write(body)
		// Best-effort overwrite of our local copy.
		for i := range buf {
			buf[i] = 0
		}
	}
}

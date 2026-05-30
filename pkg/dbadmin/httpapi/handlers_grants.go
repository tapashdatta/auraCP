package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// TableGrantStore is the engine-private surface a ConnectionStore may
// implement to expose per-table grant CRUD. v0.3.2-B introduces this
// interface so the engine can route POST/DELETE/GET on the table-grant
// endpoints without coupling the public ConnectionStore interface to a
// feature only some hosts implement (standalone + integrated panel
// both do; future hosts may not).
//
// Implementations:
//   - standalone.Connections.GrantTable / RevokeTable / ListTableGrants
//   - internal/api/dbadmin.panelConns.GrantTable / RevokeTable / ListTableGrants
type TableGrantStore interface {
	GrantTable(ctx context.Context, granter, userID string, conn dbadmin.ConnectionID, schema, table string, role dbadmin.Role) error
	RevokeTable(ctx context.Context, granter, userID string, conn dbadmin.ConnectionID, schema, table string) error
	ListTableGrants(ctx context.Context, conn dbadmin.ConnectionID, userID string) ([]dbadmin.TableGrant, error)
}

// tableGrantDTO is the wire shape we emit on GET. We translate
// dbadmin.TableGrant → wire DTO so the JSON role is the canonical
// human-readable name (matches /grants on the connection-level path)
// rather than the numeric enum.
type tableGrantDTO struct {
	UserID       string    `json:"user_id"`
	ConnectionID string    `json:"connection_id"`
	Schema       string    `json:"schema"`
	Table        string    `json:"table"`
	Role         string    `json:"role"`
	GrantedBy    string    `json:"granted_by"`
	GrantedAt    time.Time `json:"granted_at"`
}

func toTableGrantDTOs(in []dbadmin.TableGrant) []tableGrantDTO {
	out := make([]tableGrantDTO, 0, len(in))
	for _, g := range in {
		out = append(out, tableGrantDTO{
			UserID:       g.UserID,
			ConnectionID: string(g.ConnectionID),
			Schema:       g.Schema,
			Table:        g.Table,
			Role:         g.Role.String(),
			GrantedBy:    g.GrantedBy,
			GrantedAt:    g.GrantedAt,
		})
	}
	return out
}

// tableGrantInput is the request body for POST/DELETE on a per-table
// grant. Both verbs share the shape; for DELETE the role is ignored.
type tableGrantInput struct {
	UserID string `json:"user_id"`
	Role   string `json:"role,omitempty"`
}

// roleFromString maps a wire-side role name onto the dbadmin.Role enum.
// Unknown values resolve to RoleNone, which the handler converts into
// ErrInvalidInput (the GrantTable backend would also delegate to
// Revoke). The strings here are stable per types.go Role.String().
func roleFromString(s string) (dbadmin.Role, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "viewer":
		return dbadmin.RoleViewer, true
	case "analyst":
		return dbadmin.RoleAnalyst, true
	case "writer":
		return dbadmin.RoleWriter, true
	case "dba":
		return dbadmin.RoleDBA, true
	case "owner":
		return dbadmin.RoleOwner, true
	case "none", "":
		return dbadmin.RoleNone, true
	}
	return dbadmin.RoleNone, false
}

// tableGrantStore returns the engine's ConnectionStore type-asserted to
// TableGrantStore, or (nil, false) when the backend doesn't implement
// it. Handlers fall back to 501 in that case so the failure mode is
// visible (not silently dropped).
func tableGrantStore(s *server) (TableGrantStore, bool) {
	v, ok := s.engine.Conns().(TableGrantStore)
	return v, ok
}

// handleListTableGrants serves GET
// /connections/{id}/grants/tables — returns every per-table grant on
// the connection. Optional ?user_id=… filter narrows to one user.
// v0.3.2-B.
func handleListTableGrants(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		setAuditAction(r.Context(), dbadmin.ActionConnGrantMgmt, dbadmin.Target{ConnectionID: connID})
		user, _ := userFrom(r.Context())
		// ConnGrantMgmt is RoleOwner-min + step-up.
		if err := authorize(s, r.Context(), user, connID, dbadmin.ActionConnGrantMgmt); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		store, ok := tableGrantStore(s)
		if !ok {
			writeError(w, r, http.StatusNotImplemented, "not-implemented",
				"connection store does not support per-table grants")
			return
		}
		userFilter := r.URL.Query().Get("user_id")
		rows, err := store.ListTableGrants(r.Context(), connID, userFilter)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, toTableGrantDTOs(rows))
	}
}

// handleGrantTable serves POST
// /connections/{id}/grants/tables/{schema}/{table} — upserts a per-
// table grant. v0.3.2-B.
//
// Path schema may be the literal "_" to denote an empty schema (used
// by single-database engines that don't surface a schema). The router
// pattern accepts any non-slash segment.
func handleGrantTable(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		schema := schemaFromPath(r.PathValue("schema"))
		table := r.PathValue("table")
		setAuditAction(r.Context(), dbadmin.ActionConnGrantMgmt, dbadmin.Target{
			ConnectionID: connID, Schema: schema, Object: table,
		})
		user, _ := userFrom(r.Context())
		if err := authorize(s, r.Context(), user, connID, dbadmin.ActionConnGrantMgmt); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		if table == "" {
			writeError(w, r, http.StatusBadRequest, CodeInvalidInput, "table required")
			return
		}
		store, ok := tableGrantStore(s)
		if !ok {
			writeError(w, r, http.StatusNotImplemented, "not-implemented",
				"connection store does not support per-table grants")
			return
		}
		var in tableGrantInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeError(w, r, http.StatusBadRequest, CodeInvalidJSON, "invalid JSON body")
			return
		}
		if in.UserID == "" {
			writeError(w, r, http.StatusBadRequest, CodeInvalidInput, "user_id required")
			return
		}
		role, okR := roleFromString(in.Role)
		if !okR {
			writeError(w, r, http.StatusBadRequest, CodeInvalidInput, "unknown role: "+in.Role)
			return
		}
		if role == dbadmin.RoleNone {
			writeError(w, r, http.StatusBadRequest, CodeInvalidInput,
				"role required (use DELETE to revoke)")
			return
		}
		if err := store.GrantTable(r.Context(), user.ID, in.UserID, connID, schema, table, role); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleRevokeTable serves DELETE
// /connections/{id}/grants/tables/{schema}/{table} — removes a per-
// table grant. ?user_id=… selects the target user (required).
// v0.3.2-B.
func handleRevokeTable(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		schema := schemaFromPath(r.PathValue("schema"))
		table := r.PathValue("table")
		setAuditAction(r.Context(), dbadmin.ActionConnGrantMgmt, dbadmin.Target{
			ConnectionID: connID, Schema: schema, Object: table,
		})
		user, _ := userFrom(r.Context())
		if err := authorize(s, r.Context(), user, connID, dbadmin.ActionConnGrantMgmt); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		store, ok := tableGrantStore(s)
		if !ok {
			writeError(w, r, http.StatusNotImplemented, "not-implemented",
				"connection store does not support per-table grants")
			return
		}
		userID := r.URL.Query().Get("user_id")
		if userID == "" {
			// Allow the user_id to ride in JSON body too, for clients
			// that prefer not to put PII in the query string.
			var in tableGrantInput
			if r.ContentLength > 0 {
				_ = json.NewDecoder(r.Body).Decode(&in)
			}
			userID = in.UserID
		}
		if userID == "" {
			writeError(w, r, http.StatusBadRequest, CodeInvalidInput, "user_id required")
			return
		}
		if err := store.RevokeTable(r.Context(), user.ID, userID, connID, schema, table); err != nil {
			if errors.Is(err, dbadmin.ErrNotFound) {
				writeError(w, r, http.StatusNotFound, CodeNotFound, "grant not found")
				return
			}
			writeMappedErr(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// schemaFromPath normalizes the {schema} path segment. "_" is treated
// as the empty-schema sentinel (single-DB engines); everything else
// passes through verbatim.
func schemaFromPath(s string) string {
	if s == "_" {
		return ""
	}
	return s
}

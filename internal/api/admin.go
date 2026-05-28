package api

import (
	"encoding/json"
	"net/http"

	"github.com/auracp/auracp/internal/auth"
	"github.com/auracp/auracp/internal/instance"
	"github.com/auracp/auracp/internal/store"
)

// audit records a mutating action attributed to the current user.
func (s *Server) audit(r *http.Request, action, target string) {
	email := "system"
	if u, ok := s.currentUser(r); ok {
		email = u.Email
	}
	_ = s.store.AddAudit(email, action, target, "")
}

// GET /api/audit — recent activity (admin only).
func (s *Server) auditLog(w http.ResponseWriter, r *http.Request) {
	entries, err := s.store.RecentAudit(200)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if entries == nil {
		entries = []store.AuditEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// GET /api/instance — host info + live metrics.
func (s *Server) instanceInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, instance.GetStats())
}

// GET /api/instance/services — managed service status.
func (s *Server) instanceServices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, instance.Services(r.Context(), s.runner))
}

// GET /api/admin/users
func (s *Server) listAdminUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func normalizeRole(role string) string {
	switch role {
	case "ROLE_ADMIN", "ROLE_SITE_MANAGER", "ROLE_USER":
		return role
	}
	return "ROLE_USER"
}

// POST /api/admin/users  {email, role, password?, permissions?(JSON), sitesScope?(JSON)}
func (s *Server) createAdminUser(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email, Role, Password, Permissions, SitesScope string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.Email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email required"})
		return
	}
	role := normalizeRole(in.Role)
	if in.Password == "" {
		in.Password, _ = auth.RandomPassword()
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if _, err := s.store.CreateUser(in.Email, hash, role, in.Permissions, in.SitesScope); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "user.create", in.Email)
	writeJSON(w, http.StatusCreated, map[string]string{"email": in.Email, "role": role, "password": in.Password})
}

// PUT /api/admin/users/{email}  {role, permissions(JSON), sitesScope(JSON)}
func (s *Server) updateAdminUser(w http.ResponseWriter, r *http.Request) {
	email := r.PathValue("email")
	var in struct{ Role, Permissions, SitesScope string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.UpdateUserAccess(email, normalizeRole(in.Role), in.Permissions, in.SitesScope); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.audit(r, "user.update", email)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// DELETE /api/admin/users/{email}
func (s *Server) deleteAdminUser(w http.ResponseWriter, r *http.Request) {
	email := r.PathValue("email")
	if cur, ok := s.currentUser(r); ok && cur.Email == email {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot delete your own account"})
		return
	}
	if err := s.store.DeleteUserByEmail(email); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.audit(r, "user.delete", email)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// GET /api/settings
func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	all, err := s.store.AllSettings()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, all)
}

// PUT /api/settings  {key: value, ...}
func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var in map[string]string
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	for k, v := range in {
		if err := s.store.SetSetting(k, v); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// GET /api/sites/{domain}/backups
func (s *Server) listBackups(w http.ResponseWriter, r *http.Request) {
	b, err := s.store.BackupsForSite(r.PathValue("domain"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if b == nil {
		b = []store.Backup{}
	}
	writeJSON(w, http.StatusOK, b)
}

// POST /api/sites/{domain}/backups
func (s *Server) createBackup(w http.ResponseWriter, r *http.Request) {
	rec, err := s.backups.CreateSite(r.Context(), r.PathValue("domain"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// Push to the configured remote, best-effort.
	if enc, ok := s.store.GetSetting(remoteBackupKey); ok {
		if target, derr := s.secret.Decrypt(enc); derr == nil && target != "" {
			_ = s.backups.PushRemote(r.Context(), rec.Path, target)
		}
	}
	s.audit(r, "backup.create", r.PathValue("domain"))
	writeJSON(w, http.StatusCreated, rec)
}

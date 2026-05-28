package store

import (
	"database/sql"
	"time"
)

// User is a panel user (admin / site-manager / regular).
type User struct {
	ID           int64
	Email        string
	PasswordHash string
	Role         string
	Permissions  string // JSON CRUD matrix ("" = role default)
	SitesScope   string // JSON array of allowed domains ("" = all sites; v0.2.15+)
	TOTPSecret   sql.NullString
}

func (u User) MFAEnabled() bool { return u.TOTPSecret.Valid && u.TOTPSecret.String != "" }

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.Permissions, &u.SitesScope, &u.TOTPSecret)
	return u, err
}

func (s *Store) UserByEmail(email string) (User, error) {
	return scanUser(s.DB.QueryRow(
		`SELECT id, email, password_hash, role, permissions, sites_scope, totp_secret FROM panel_users WHERE email = ?`, email))
}

func (s *Store) UserByID(id int64) (User, error) {
	return scanUser(s.DB.QueryRow(
		`SELECT id, email, password_hash, role, permissions, sites_scope, totp_secret FROM panel_users WHERE id = ?`, id))
}

func (s *Store) CreateUser(email, passwordHash, role, permissions, sitesScope string) (int64, error) {
	res, err := s.DB.Exec(`INSERT INTO panel_users (email, password_hash, role, permissions, sites_scope) VALUES (?, ?, ?, ?, ?)`,
		email, passwordHash, role, permissions, sitesScope)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateUserAccess changes a user's role, permission matrix, and site scope.
// sitesScope is a JSON array of domains; empty string = "all sites".
func (s *Store) UpdateUserAccess(email, role, permissions, sitesScope string) error {
	_, err := s.DB.Exec(`UPDATE panel_users SET role = ?, permissions = ?, sites_scope = ? WHERE email = ?`,
		role, permissions, sitesScope, email)
	return err
}

// SetUserTOTP stores (or clears, when secret == "") a user's TOTP secret.
func (s *Store) SetUserTOTP(id int64, secret string) error {
	if secret == "" {
		_, err := s.DB.Exec(`UPDATE panel_users SET totp_secret = NULL WHERE id = ?`, id)
		return err
	}
	_, err := s.DB.Exec(`UPDATE panel_users SET totp_secret = ? WHERE id = ?`, secret, id)
	return err
}

func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM panel_users`).Scan(&n)
	return n, err
}

// ---- sessions ----

func (s *Store) CreateSession(token string, userID int64, mfaPending bool, ttl time.Duration) error {
	_, err := s.DB.Exec(`INSERT INTO sessions (token, user_id, mfa_pending, expires_at) VALUES (?, ?, ?, ?)`,
		token, userID, b2i(mfaPending), time.Now().Add(ttl).UTC())
	return err
}

// Session returns the user id and pending-MFA flag for a non-expired token.
func (s *Store) Session(token string) (userID int64, mfaPending bool, ok bool) {
	var pending int
	var expires time.Time
	err := s.DB.QueryRow(`SELECT user_id, mfa_pending, expires_at FROM sessions WHERE token = ?`, token).
		Scan(&userID, &pending, &expires)
	if err != nil || time.Now().After(expires) {
		return 0, false, false
	}
	return userID, pending == 1, true
}

func (s *Store) ClearSessionMFAPending(token string) error {
	_, err := s.DB.Exec(`UPDATE sessions SET mfa_pending = 0 WHERE token = ?`, token)
	return err
}

func (s *Store) DeleteSession(token string) error {
	_, err := s.DB.Exec(`DELETE FROM sessions WHERE token = ?`, token)
	return err
}

package store

// UserView is a panel user without secrets, for admin listings.
type UserView struct {
	Email       string `json:"email"`
	Role        string `json:"role"`
	Permissions string `json:"permissions"` // JSON ("" = role default)
	MFAEnabled  bool   `json:"mfaEnabled"`
}

func (s *Store) ListUsers() ([]UserView, error) {
	rows, err := s.DB.Query(`SELECT email, role, permissions, totp_secret FROM panel_users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserView
	for rows.Next() {
		var v UserView
		var secret *string
		if err := rows.Scan(&v.Email, &v.Role, &v.Permissions, &secret); err != nil {
			return nil, err
		}
		v.MFAEnabled = secret != nil && *secret != ""
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) DeleteUserByEmail(email string) error {
	_, err := s.DB.Exec(`DELETE FROM panel_users WHERE email = ?`, email)
	return err
}

func (s *Store) UpdateUserPassword(email, passwordHash string) error {
	_, err := s.DB.Exec(`UPDATE panel_users SET password_hash = ? WHERE email = ?`, passwordHash, email)
	return err
}

// ---- settings ----

func (s *Store) GetSetting(key string) (string, bool) {
	var v string
	err := s.DB.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	return v, err == nil
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.DB.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (s *Store) AllSettings() (map[string]string, error) {
	rows, err := s.DB.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

package store

// ---- per-site config (k/v: cache, https redirect, http3, cloudflare, etc.) ----

func (s *Store) SiteConfig(domain string) (map[string]string, error) {
	rows, err := s.DB.Query(`SELECT key, value FROM site_config WHERE site_domain = ?`, domain)
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

func (s *Store) SetSiteConfig(domain, key, value string) error {
	_, err := s.DB.Exec(`INSERT INTO site_config (site_domain, key, value) VALUES (?, ?, ?)
		ON CONFLICT(site_domain, key) DO UPDATE SET value = excluded.value`, domain, key, value)
	return err
}

// ---- per-site SSH/FTP users ----

type SSHUser struct {
	ID       int64  `json:"id"`
	Domain   string `json:"-"`
	Username string `json:"username"`
	Type     string `json:"type"` // ssh|sftp
}

func (s *Store) SSHUsersForSite(domain string) ([]SSHUser, error) {
	rows, err := s.DB.Query(`SELECT id, site_domain, username, type FROM ssh_users
		WHERE site_domain = ? ORDER BY id`, domain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SSHUser
	for rows.Next() {
		var u SSHUser
		if err := rows.Scan(&u.ID, &u.Domain, &u.Username, &u.Type); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) AddSSHUser(u SSHUser, passwordEnc string) error {
	_, err := s.DB.Exec(`INSERT INTO ssh_users (site_domain, username, type, password_enc) VALUES (?, ?, ?, ?)`,
		u.Domain, u.Username, u.Type, passwordEnc)
	return err
}

func (s *Store) SSHUserByName(domain, username string) (SSHUser, error) {
	var u SSHUser
	err := s.DB.QueryRow(`SELECT id, site_domain, username, type FROM ssh_users
		WHERE site_domain = ? AND username = ?`, domain, username).
		Scan(&u.ID, &u.Domain, &u.Username, &u.Type)
	return u, err
}

func (s *Store) DeleteSSHUser(domain, username string) error {
	_, err := s.DB.Exec(`DELETE FROM ssh_users WHERE site_domain = ? AND username = ?`, domain, username)
	return err
}

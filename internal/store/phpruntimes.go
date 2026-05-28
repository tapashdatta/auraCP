package store

// PHPRuntime is one of the multiple PHP-FPM versions installed side-by-side
// (8.3 / 8.4 / 8.5 from deb.sury.org). Sites pin to a version via sites.php_version.
type PHPRuntime struct {
	Version   string `json:"version"`
	IsDefault bool   `json:"isDefault"`
}

func (s *Store) PHPRuntimes() ([]PHPRuntime, error) {
	rows, err := s.DB.Query(`SELECT version, is_default FROM php_runtimes ORDER BY version DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PHPRuntime
	for rows.Next() {
		var r PHPRuntime
		var def int
		if err := rows.Scan(&r.Version, &def); err != nil {
			return nil, err
		}
		r.IsDefault = def == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) PHPRuntime(version string) (PHPRuntime, bool) {
	var r PHPRuntime
	var def int
	err := s.DB.QueryRow(`SELECT version, is_default FROM php_runtimes WHERE version = ?`, version).
		Scan(&r.Version, &def)
	if err != nil {
		return PHPRuntime{}, false
	}
	r.IsDefault = def == 1
	return r, true
}

func (s *Store) DefaultPHPRuntime() (PHPRuntime, bool) {
	var r PHPRuntime
	var def int
	err := s.DB.QueryRow(`SELECT version, is_default FROM php_runtimes WHERE is_default = 1 LIMIT 1`).
		Scan(&r.Version, &def)
	if err != nil {
		return PHPRuntime{}, false
	}
	r.IsDefault = true
	return r, true
}

func (s *Store) AddPHPRuntime(version string) error {
	_, err := s.DB.Exec(`INSERT OR IGNORE INTO php_runtimes (version, is_default) VALUES (?, 0)`, version)
	return err
}

func (s *Store) SetDefaultPHPRuntime(version string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE php_runtimes SET is_default = 0`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE php_runtimes SET is_default = 1 WHERE version = ?`, version); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeletePHPRuntime(version string) error {
	_, err := s.DB.Exec(`DELETE FROM php_runtimes WHERE version = ?`, version)
	return err
}

// SiteUsesPHPVersion returns true if any site is pinned to this PHP version.
// Used to refuse `phpruntime remove` when something still depends on it.
func (s *Store) SiteUsesPHPVersion(version string) bool {
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM sites WHERE php_version = ?`, version).Scan(&n)
	return n > 0
}

func (s *Store) SetSitePHPVersion(domain, version string) error {
	_, err := s.DB.Exec(`UPDATE sites SET php_version = ? WHERE domain = ?`, version, domain)
	return err
}

// --- per-site PHP settings (memory_limit, upload_max_filesize, …) ---

func (s *Store) PHPSetting(domain, key string) (string, bool) {
	var v string
	err := s.DB.QueryRow(`SELECT value FROM php_settings WHERE domain = ? AND key = ?`, domain, key).Scan(&v)
	if err != nil {
		return "", false
	}
	return v, true
}

func (s *Store) PHPSettings(domain string) (map[string]string, error) {
	rows, err := s.DB.Query(`SELECT key, value FROM php_settings WHERE domain = ?`, domain)
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

func (s *Store) SetPHPSetting(domain, key, value string) error {
	_, err := s.DB.Exec(`INSERT INTO php_settings(domain, key, value) VALUES(?,?,?)
		ON CONFLICT(domain, key) DO UPDATE SET value=excluded.value`, domain, key, value)
	return err
}

func (s *Store) DeletePHPSetting(domain, key string) error {
	_, err := s.DB.Exec(`DELETE FROM php_settings WHERE domain = ? AND key = ?`, domain, key)
	return err
}

// DeleteAllPHPSettings is called when a site is deleted so we don't leave
// orphan overrides behind.
func (s *Store) DeleteAllPHPSettings(domain string) error {
	_, err := s.DB.Exec(`DELETE FROM php_settings WHERE domain = ?`, domain)
	return err
}

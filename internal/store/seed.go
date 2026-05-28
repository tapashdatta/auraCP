package store

// seed runs first-run setup. No admin is auto-created — the panel starts with
// zero users and the UI shows a first-run "create admin" form. No demo sites.
func (s *Store) seed() error {
	return s.seedDatabaseServers()
}

// seedDatabaseServers records the two supported local engines so the
// per-database engine picker has options. Reflects what the installer can set up.
func (s *Store) seedDatabaseServers() error {
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM database_servers`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	servers := []DatabaseServer{
		{Engine: "mariadb", Host: "127.0.0.1", Port: 3306, Version: "11.8", IsDefault: true},
		{Engine: "postgres", Host: "127.0.0.1", Port: 5432, Version: "17", IsDefault: false},
	}
	for _, d := range servers {
		if _, err := s.DB.Exec(`INSERT INTO database_servers (engine, host, port, version, is_default) VALUES (?, ?, ?, ?, ?)`,
			d.Engine, d.Host, d.Port, d.Version, b2i(d.IsDefault)); err != nil {
			return err
		}
	}
	return nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

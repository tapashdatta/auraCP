package store

import (
	"log"

	"github.com/auracp/auracp/internal/auth"
)

// seed runs first-run setup: an initial admin user and the available DB engines.
// No demo/sample sites are created — the panel starts empty.
func (s *Store) seed() error {
	if err := s.seedAdmin(); err != nil {
		return err
	}
	return s.seedDatabaseServers()
}

// seedAdmin creates the first admin user with a random password, printed once.
func (s *Store) seedAdmin() error {
	n, err := s.CountUsers()
	if err != nil || n > 0 {
		return err
	}
	pw, err := auth.RandomPassword()
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	const email = "admin@localhost"
	if _, err := s.CreateUser(email, hash, "ROLE_ADMIN", ""); err != nil {
		return err
	}
	log.Printf("┌─ initial admin account created ─────────────────────────────")
	log.Printf("│  email:    %s", email)
	log.Printf("│  password: %s", pw)
	log.Printf("└─ change it after first login (this is shown only once) ─────")
	return nil
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

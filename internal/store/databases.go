package store

// Database is a provisioned database (engine chosen per-database — the
// MariaDB/PostgreSQL differentiator).
type Database struct {
	ID         int64  `json:"-"`
	SiteDomain string `json:"site"`
	Engine     string `json:"engine"`
	Name       string `json:"name"`
	DBUser     string `json:"user"`
	// password is never returned in listings; stored encrypted.
}

func (s *Store) CreateDatabaseRecord(d Database, passwordEnc string) error {
	_, err := s.DB.Exec(`INSERT INTO databases (site_domain, engine, name, db_user, password_enc)
		VALUES (?, ?, ?, ?, ?)`, d.SiteDomain, d.Engine, d.Name, d.DBUser, passwordEnc)
	return err
}

func (s *Store) DeleteDatabaseRecord(engine, name string) error {
	_, err := s.DB.Exec(`DELETE FROM databases WHERE engine = ? AND name = ?`, engine, name)
	return err
}

func (s *Store) DatabasesForSite(domain string) ([]Database, error) {
	rows, err := s.DB.Query(`SELECT site_domain, engine, name, db_user FROM databases
		WHERE site_domain = ? ORDER BY id`, domain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Database
	for rows.Next() {
		var d Database
		if err := rows.Scan(&d.SiteDomain, &d.Engine, &d.Name, &d.DBUser); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// PR #17 (v0.3.0): DatabasePasswordEnc was removed alongside the Adminer
// SSO endpoint that was its only caller. Aura DB stores its connection
// credentials in the aura_db_* tables via the dbadmin engine; the
// site-level "databases" table no longer needs to expose the encrypted
// blob outside the store.

package store

import "database/sql"

const siteCols = `type, domain, site_user, root_path, app, node_version, port, upstream, php_version, pm2_enabled, status, status_text`

func scanSite(rows interface{ Scan(...any) error }) (Site, error) {
	var st Site
	var pm2 int
	err := rows.Scan(&st.Type, &st.Domain, &st.SiteUser, &st.RootPath, &st.App,
		&st.NodeVersion, &st.Port, &st.Upstream, &st.PHPVersion, &pm2, &st.Status, &st.StatusText)
	st.PM2Enabled = pm2 == 1
	return st, err
}

// Sites returns all sites, newest first.
func (s *Store) Sites() ([]Site, error) {
	rows, err := s.DB.Query(`SELECT ` + siteCols + ` FROM sites ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Site
	for rows.Next() {
		st, err := scanSite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// SiteByDomain returns one site or sql.ErrNoRows.
func (s *Store) SiteByDomain(domain string) (Site, error) {
	return scanSite(s.DB.QueryRow(`SELECT `+siteCols+` FROM sites WHERE domain = ?`, domain))
}

// CreateSite inserts a new site record (the OS/service work happens elsewhere).
func (s *Store) CreateSite(st Site) error {
	_, err := s.DB.Exec(`INSERT INTO sites
		(type, domain, site_user, root_path, app, node_version, port, upstream, php_version, pm2_enabled, status, status_text)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		st.Type, st.Domain, st.SiteUser, st.RootPath, st.App, st.NodeVersion,
		st.Port, st.Upstream, st.PHPVersion, b2i(st.PM2Enabled),
		nz(st.Status, "up"), nz(st.StatusText, "Provisioning"))
	return err
}

// SetSitePM2 toggles PM2 process-manager use for a Node.js site.
func (s *Store) SetSitePM2(domain string, enabled bool) error {
	_, err := s.DB.Exec(`UPDATE sites SET pm2_enabled = ? WHERE domain = ?`, b2i(enabled), domain)
	return err
}

// DeleteSite removes a site record by domain.
func (s *Store) DeleteSite(domain string) error {
	_, err := s.DB.Exec(`DELETE FROM sites WHERE domain = ?`, domain)
	return err
}

// NextPort returns the next free backend loopback port (sequential, base 9000).
func (s *Store) NextPort() (int, error) {
	var max sql.NullInt64
	if err := s.DB.QueryRow(`SELECT MAX(port) FROM sites`).Scan(&max); err != nil {
		return 0, err
	}
	if !max.Valid || max.Int64 < 9000 {
		return 9000, nil
	}
	return int(max.Int64) + 1, nil
}

// DatabaseServers lists the configured local DB engines.
func (s *Store) DatabaseServers() ([]DatabaseServer, error) {
	rows, err := s.DB.Query(`SELECT engine, host, port, version, is_default FROM database_servers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DatabaseServer
	for rows.Next() {
		var d DatabaseServer
		var ver sql.NullString
		if err := rows.Scan(&d.Engine, &d.Host, &d.Port, &ver, &d.IsDefault); err != nil {
			return nil, err
		}
		d.Version = ver.String
		out = append(out, d)
	}
	return out, rows.Err()
}

func nz(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

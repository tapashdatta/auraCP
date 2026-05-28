package store

type NodeRuntime struct {
	Version   string `json:"version"`
	Path      string `json:"-"`
	IsDefault bool   `json:"isDefault"`
}

func (s *Store) NodeRuntimes() ([]NodeRuntime, error) {
	rows, err := s.DB.Query(`SELECT version, path, is_default FROM node_runtimes ORDER BY version DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeRuntime
	for rows.Next() {
		var n NodeRuntime
		var def int
		if err := rows.Scan(&n.Version, &n.Path, &def); err != nil {
			return nil, err
		}
		n.IsDefault = def == 1
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) DefaultNodeRuntime() (NodeRuntime, bool) {
	var n NodeRuntime
	var def int
	err := s.DB.QueryRow(`SELECT version, path, is_default FROM node_runtimes WHERE is_default = 1 LIMIT 1`).
		Scan(&n.Version, &n.Path, &def)
	if err != nil {
		return NodeRuntime{}, false
	}
	n.IsDefault = true
	return n, true
}

func (s *Store) NodeRuntime(version string) (NodeRuntime, bool) {
	var n NodeRuntime
	var def int
	err := s.DB.QueryRow(`SELECT version, path, is_default FROM node_runtimes WHERE version = ?`, version).
		Scan(&n.Version, &n.Path, &def)
	if err != nil {
		return NodeRuntime{}, false
	}
	n.IsDefault = def == 1
	return n, true
}

func (s *Store) AddNodeRuntime(n NodeRuntime) error {
	_, err := s.DB.Exec(`INSERT INTO node_runtimes (version, path, is_default) VALUES (?, ?, ?)`,
		n.Version, n.Path, b2i(n.IsDefault))
	return err
}

// SetDefaultNodeRuntime clears any existing default and sets the named one.
func (s *Store) SetDefaultNodeRuntime(version string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE node_runtimes SET is_default = 0`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE node_runtimes SET is_default = 1 WHERE version = ?`, version); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteNodeRuntime(version string) error {
	_, err := s.DB.Exec(`DELETE FROM node_runtimes WHERE version = ?`, version)
	return err
}

// SiteUsesNodeVersion returns true if any site is pinned to this version.
func (s *Store) SiteUsesNodeVersion(version string) bool {
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM sites WHERE type='nodejs' AND node_version = ?`, version).Scan(&n)
	return n > 0
}

// SetSiteNodeVersion pins a site to a specific Node runtime version.
func (s *Store) SetSiteNodeVersion(domain, version string) error {
	_, err := s.DB.Exec(`UPDATE sites SET node_version = ? WHERE domain = ?`, version, domain)
	return err
}

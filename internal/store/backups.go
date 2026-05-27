package store

type Backup struct {
	ID         int64  `json:"id"`
	SiteDomain string `json:"site"`
	Kind       string `json:"kind"`
	Path       string `json:"path"`
	SizeBytes  int64  `json:"size"`
	CreatedAt  string `json:"createdAt"`
}

func (s *Store) AddBackup(b Backup) error {
	_, err := s.DB.Exec(`INSERT INTO backups (site_domain, kind, path, size_bytes) VALUES (?, ?, ?, ?)`,
		b.SiteDomain, b.Kind, b.Path, b.SizeBytes)
	return err
}

func (s *Store) BackupsForSite(domain string) ([]Backup, error) {
	rows, err := s.DB.Query(`SELECT id, site_domain, kind, path, size_bytes, created_at
		FROM backups WHERE site_domain = ? ORDER BY created_at DESC, id DESC`, domain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Backup
	for rows.Next() {
		var b Backup
		if err := rows.Scan(&b.ID, &b.SiteDomain, &b.Kind, &b.Path, &b.SizeBytes, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// OldBackupPaths returns backup paths beyond the newest keep, for retention pruning.
func (s *Store) OldBackupPaths(domain string, keep int) ([]string, error) {
	rows, err := s.DB.Query(`SELECT path FROM backups WHERE site_domain = ?
		ORDER BY created_at DESC, id DESC LIMIT -1 OFFSET ?`, domain, keep)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

func (s *Store) DeleteBackupByPath(path string) error {
	_, err := s.DB.Exec(`DELETE FROM backups WHERE path = ?`, path)
	return err
}

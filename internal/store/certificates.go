package store

import "database/sql"

// Certificate tracks the ACME-issuance state for one domain. Populated by the
// internal/acme manager; read by the renewal goroutine + the UI.
type Certificate struct {
	Domain     string         `json:"domain"`
	Issuer     string         `json:"issuer"`
	CertPath   sql.NullString `json:"-"`
	KeyPath    sql.NullString `json:"-"`
	IssuedAt   sql.NullInt64  `json:"issuedAt"`
	ExpiresAt  sql.NullInt64  `json:"expiresAt"`
	Status     string         `json:"status"`
	LastError  string         `json:"lastError"`
	Attempts   int            `json:"attempts"`
}

func (s *Store) UpsertCertificate(c Certificate) error {
	_, err := s.DB.Exec(`
		INSERT INTO certificates(domain, issuer, cert_path, key_path, issued_at, expires_at, status, last_error, attempts)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(domain) DO UPDATE SET
			issuer=excluded.issuer,
			cert_path=COALESCE(excluded.cert_path, certificates.cert_path),
			key_path=COALESCE(excluded.key_path, certificates.key_path),
			issued_at=COALESCE(excluded.issued_at, certificates.issued_at),
			expires_at=COALESCE(excluded.expires_at, certificates.expires_at),
			status=excluded.status,
			last_error=excluded.last_error,
			attempts=excluded.attempts`,
		c.Domain, c.Issuer, c.CertPath, c.KeyPath, c.IssuedAt, c.ExpiresAt, c.Status, c.LastError, c.Attempts)
	return err
}

func (s *Store) Certificate(domain string) (Certificate, bool) {
	var c Certificate
	err := s.DB.QueryRow(`SELECT domain, issuer, cert_path, key_path, issued_at, expires_at, status, last_error, attempts
		FROM certificates WHERE domain = ?`, domain).
		Scan(&c.Domain, &c.Issuer, &c.CertPath, &c.KeyPath, &c.IssuedAt, &c.ExpiresAt, &c.Status, &c.LastError, &c.Attempts)
	if err != nil {
		return Certificate{}, false
	}
	return c, true
}

func (s *Store) Certificates() ([]Certificate, error) {
	rows, err := s.DB.Query(`SELECT domain, issuer, cert_path, key_path, issued_at, expires_at, status, last_error, attempts
		FROM certificates ORDER BY domain`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Certificate
	for rows.Next() {
		var c Certificate
		if err := rows.Scan(&c.Domain, &c.Issuer, &c.CertPath, &c.KeyPath, &c.IssuedAt, &c.ExpiresAt, &c.Status, &c.LastError, &c.Attempts); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CertificatesExpiringWithin returns rows whose expires_at is within `secs`
// seconds from `now`. Used by the renewal goroutine.
func (s *Store) CertificatesExpiringWithin(nowUnix, secs int64) ([]Certificate, error) {
	rows, err := s.DB.Query(`SELECT domain, issuer, cert_path, key_path, issued_at, expires_at, status, last_error, attempts
		FROM certificates WHERE expires_at IS NOT NULL AND (expires_at - ?) < ? ORDER BY expires_at`, nowUnix, secs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Certificate
	for rows.Next() {
		var c Certificate
		if err := rows.Scan(&c.Domain, &c.Issuer, &c.CertPath, &c.KeyPath, &c.IssuedAt, &c.ExpiresAt, &c.Status, &c.LastError, &c.Attempts); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) DeleteCertificate(domain string) error {
	_, err := s.DB.Exec(`DELETE FROM certificates WHERE domain = ?`, domain)
	return err
}

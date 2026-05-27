package store

type AuditEntry struct {
	TS     string `json:"ts"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Target string `json:"target"`
	Detail string `json:"detail"`
}

func (s *Store) AddAudit(actor, action, target, detail string) error {
	_, err := s.DB.Exec(`INSERT INTO audit_log (actor, action, target, detail) VALUES (?, ?, ?, ?)`,
		actor, action, target, detail)
	return err
}

func (s *Store) RecentAudit(limit int) ([]AuditEntry, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.DB.Query(`SELECT ts, actor, action, target, detail FROM audit_log
		ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var a AuditEntry
		if err := rows.Scan(&a.TS, &a.Actor, &a.Action, &a.Target, &a.Detail); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

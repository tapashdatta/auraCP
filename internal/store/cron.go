package store

type CronJob struct {
	ID       int64  `json:"id"`
	Domain   string `json:"-"`
	User     string `json:"-"`
	Schedule string `json:"schedule"`
	Command  string `json:"command"`
	Enabled  bool   `json:"enabled"`
}

func (s *Store) CronJobsForSite(domain string) ([]CronJob, error) {
	rows, err := s.DB.Query(`SELECT id, site_domain, site_user, schedule, command, enabled
		FROM cron_jobs WHERE site_domain = ? ORDER BY id`, domain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CronJob
	for rows.Next() {
		var c CronJob
		var en int
		if err := rows.Scan(&c.ID, &c.Domain, &c.User, &c.Schedule, &c.Command, &en); err != nil {
			return nil, err
		}
		c.Enabled = en == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) AddCronJob(c CronJob) error {
	_, err := s.DB.Exec(`INSERT INTO cron_jobs (site_domain, site_user, schedule, command, enabled)
		VALUES (?, ?, ?, ?, ?)`, c.Domain, c.User, c.Schedule, c.Command, b2i(c.Enabled))
	return err
}

func (s *Store) DeleteCronJob(domain string, id int64) error {
	_, err := s.DB.Exec(`DELETE FROM cron_jobs WHERE id = ? AND site_domain = ?`, id, domain)
	return err
}

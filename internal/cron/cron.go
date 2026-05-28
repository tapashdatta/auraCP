// Package cron renders a site user's crontab from the stored jobs and installs
// it with `crontab -u`. Jobs always run as the site's own (unprivileged) user.
package cron

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/auracp/auracp/internal/store"
	"github.com/auracp/auracp/internal/system"
	"github.com/auracp/auracp/internal/validate"
)

type Manager struct {
	R     *system.Runner
	Store *store.Store
}

func New(r *system.Runner, st *store.Store) *Manager { return &Manager{R: r, Store: st} }

// Apply rebuilds the user's crontab from the enabled jobs for the site.
func (m *Manager) Apply(ctx context.Context, user, domain string) error {
	if err := validate.Username(user); err != nil {
		return err
	}
	jobs, err := m.Store.CronJobsForSite(domain)
	if err != nil {
		return err
	}
	var b strings.Builder
	// v0.2.38: pure-ASCII header. The previous em-dash (U+2014) made some
	// crontab parsers (notably vixie-cron in LANG=C) reject line 1 with
	// "bad command errors in crontab file, can't install."
	b.WriteString("# Managed by auraCP - do not edit by hand\n")
	for _, j := range jobs {
		if j.Enabled {
			fmt.Fprintf(&b, "%s %s\n", j.Schedule, j.Command)
		}
	}
	if m.R.DryRun {
		m.R.Run(ctx, "crontab", "-u", user, "<rendered-crontab>") // logged only
		return nil
	}
	tmp, err := os.CreateTemp("", "auracp-cron-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(b.String()); err != nil {
		return err
	}
	tmp.Close()
	_, err = m.R.Run(ctx, "crontab", "-u", user, tmp.Name())
	return err
}

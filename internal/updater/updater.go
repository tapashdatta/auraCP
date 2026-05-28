// Package updater knows how to check GitHub Releases for newer auracp
// versions and trigger the bundled /usr/local/bin/auracp-update script.
// The HTTP API surface (GET / POST /api/instance/update) lives in
// internal/api/updater.go; this package is the lower-level engine.
package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Repo holds the GitHub owner/name we ask /releases/latest about. Overridable
// for forks / mirrors via the AURACP_REPO env var, same shape as the bundled
// auracp-update CLI honours.
const defaultRepo = "tapashdatta/auraCP"

func repoSlug() string {
	if r := os.Getenv("AURACP_REPO"); r != "" {
		return r
	}
	return defaultRepo
}

// Release is the slice of GitHub's /releases/latest payload we care about.
type Release struct {
	Tag         string    `json:"tag_name"`
	Name        string    `json:"name"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Prerelease  bool      `json:"prerelease"`
	Draft       bool      `json:"draft"`
}

// Status is what the panel UI consumes.
type Status struct {
	Current     string    `json:"current"`              // running auracpd version (from main.version)
	Latest      string    `json:"latest"`               // tag_name, e.g. "v0.2.9"
	LatestPlain string    `json:"latestPlain"`          // tag_name with leading v stripped
	Available   bool      `json:"available"`            // strictly newer than Current
	ReleaseURL  string    `json:"releaseUrl,omitempty"` // browser link to the release notes
	CheckedAt   time.Time `json:"checkedAt"`
	Error       string    `json:"error,omitempty"`      // last check error, if any
}

// Manager is owned by cmd/auracpd; it caches the last GitHub probe so the UI
// (which may load the dashboard many times per minute) doesn't hammer
// api.github.com — and our 60-req/h unauthenticated rate limit doesn't burn.
type Manager struct {
	current string
	mu      sync.RWMutex
	last    Status
	ttl     time.Duration
}

func New(currentVersion string) *Manager {
	return &Manager{current: currentVersion, ttl: 1 * time.Hour}
}

// Status returns the cached value if fresh; otherwise probes GitHub once.
// Background goroutine in cmd/auracpd refreshes this every 12h so the UI rarely
// triggers a fresh fetch even on the first paint of the day.
func (m *Manager) Status(ctx context.Context) Status {
	m.mu.RLock()
	if !m.last.CheckedAt.IsZero() && time.Since(m.last.CheckedAt) < m.ttl {
		s := m.last
		m.mu.RUnlock()
		return s
	}
	m.mu.RUnlock()
	return m.Refresh(ctx)
}

// Refresh forces a GitHub probe. Returns the new status either way (errors are
// reported via Status.Error so the UI can show "couldn't check" without
// blowing up).
func (m *Manager) Refresh(ctx context.Context) Status {
	s := Status{Current: m.current, CheckedAt: time.Now().UTC()}
	r, err := latestRelease(ctx)
	if err != nil {
		s.Error = err.Error()
	} else {
		s.Latest = r.Tag
		s.LatestPlain = strings.TrimPrefix(r.Tag, "v")
		s.ReleaseURL = r.HTMLURL
		s.Available = compareVersions(s.LatestPlain, m.current) > 0
	}
	m.mu.Lock()
	m.last = s
	m.mu.Unlock()
	return s
}

// Apply triggers /usr/local/bin/auracp-update in a TRANSIENT systemd scope so
// the upgrade survives `systemctl stop auracpd` (which is what dpkg's prerm
// does). v0.2.13–0.2.15 used `setsid`, which detaches the session but NOT
// the cgroup — systemd kills the whole auracpd.service cgroup on stop, taking
// dpkg with it, leaving a half-installed package and a 502.
//
// `systemd-run --no-block --collect` puts the upgrade into its own transient
// unit (own cgroup), returns immediately, and garbage-collects the unit on
// success. The upgrade log goes to its own journal entries (visible via
// `journalctl -u <unit-name>`) so an operator can review post-mortem.
//
// Returns immediately; caller should respond 202 and the UI polls /api/health
// to know when the new daemon is up.
func (m *Manager) Apply(ctx context.Context) error {
	if _, err := exec.LookPath("auracp-update"); err != nil {
		return fmt.Errorf("auracp-update not found in PATH (re-install the .deb to restore the symlink)")
	}
	unitName := fmt.Sprintf("auracp-update-%d", time.Now().Unix())
	// Use systemd-run when available (every modern Debian/Ubuntu host).
	if _, err := exec.LookPath("systemd-run"); err == nil {
		// --no-block: return immediately. --collect: GC the transient unit on
		// success. --unit: predictable name for `journalctl -u`. The shell
		// wrapper redirects to the long-lived log file so the historical tail
		// stays in one place.
		cmd := exec.Command("systemd-run",
			"--no-block", "--collect", "--unit="+unitName,
			"sh", "-c",
			"sleep 2 && /usr/local/bin/auracp-update >> /var/log/auracp-update.log 2>&1")
		if err := cmd.Run(); err == nil {
			return nil
		}
		// fall through to setsid fallback if systemd-run somehow fails
	}
	// Fallback for non-systemd containers (or systemd-run permission denied):
	// setsid + nohup. NOTE: this is the v0.2.15 behaviour that's known to
	// die mid-upgrade when invoked from auracpd's own cgroup; use only when
	// systemd-run isn't available (the docs warn against this path).
	cmd := exec.Command("setsid", "sh", "-c",
		"sleep 2 && /usr/local/bin/auracp-update >> /var/log/auracp-update.log 2>&1")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		cmd = exec.Command("nohup", "sh", "-c",
			"sleep 2 && /usr/local/bin/auracp-update >> /var/log/auracp-update.log 2>&1 &")
		if err2 := cmd.Start(); err2 != nil {
			return fmt.Errorf("could not detach auracp-update: %w", err)
		}
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// --- helpers ---

func latestRelease(ctx context.Context) (Release, error) {
	url := "https://api.github.com/repos/" + repoSlug() + "/releases/latest"
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "auracp-updater")
	cli := &http.Client{Timeout: 10 * time.Second}
	r, err := cli.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("github: %s", r.Status)
	}
	var rel Release
	if err := json.NewDecoder(r.Body).Decode(&rel); err != nil {
		return Release{}, err
	}
	return rel, nil
}

// compareVersions returns >0 if a > b, <0 if a < b, 0 if equal. Handles
// "v0.2.9" prefixes and missing trailing components (1.0 vs 1.0.0). We don't
// honour the semver build-metadata or pre-release suffixes — we never ship
// those.
func compareVersions(a, b string) int {
	a = strings.TrimPrefix(strings.TrimSpace(a), "v")
	b = strings.TrimPrefix(strings.TrimSpace(b), "v")
	for {
		ap, ar := splitFirst(a)
		bp, br := splitFirst(b)
		ai, _ := strconv.Atoi(ap)
		bi, _ := strconv.Atoi(bp)
		if ai != bi {
			if ai > bi {
				return 1
			}
			return -1
		}
		if ar == "" && br == "" {
			return 0
		}
		a, b = ar, br
	}
}

func splitFirst(s string) (head, rest string) {
	i := strings.IndexByte(s, '.')
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i+1:]
}

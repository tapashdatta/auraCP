// Package instance reports host metrics (load, memory, disk) and service
// status for the dashboard and Instance screen. Metrics read from /proc on
// Linux; on other OSes they degrade to zero (dev convenience).
package instance

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/auracp/auracp/internal/system"
)

type Stats struct {
	Hostname    string  `json:"hostname"`
	OS          string  `json:"os"`
	Load1       float64 `json:"load1"`
	Load5       float64 `json:"load5"`
	Load15      float64 `json:"load15"`
	Cores       int     `json:"cores"`
	MemUsedMB   int64   `json:"memUsedMB"`
	MemTotalMB  int64   `json:"memTotalMB"`
	DiskUsedGB  int64   `json:"diskUsedGB"`
	DiskTotalGB int64   `json:"diskTotalGB"`
}

func GetStats() Stats {
	s := Stats{Cores: runtime.NumCPU(), OS: osPretty()}
	s.Hostname, _ = os.Hostname()
	s.Load1, s.Load5, s.Load15 = loadAvg()
	s.MemUsedMB, s.MemTotalMB = memInfo()
	s.DiskUsedGB, s.DiskTotalGB = diskInfo("/")
	return s
}

func osPretty() string {
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "PRETTY_NAME=") {
				return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
			}
		}
	}
	return runtime.GOOS
}

func loadAvg() (float64, float64, float64) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return 0, 0, 0
	}
	p := func(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }
	return p(f[0]), p(f[1]), p(f[2])
}

func memInfo() (used, total int64) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	var totalKB, availKB int64
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, _ := strconv.ParseInt(f[1], 10, 64)
		switch f[0] {
		case "MemTotal:":
			totalKB = v
		case "MemAvailable:":
			availKB = v
		}
	}
	return (totalKB - availKB) / 1024, totalKB / 1024
}

func diskInfo(path string) (usedGB, totalGB int64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	bs := int64(st.Bsize)
	total := int64(st.Blocks) * bs
	free := int64(st.Bavail) * bs
	const gb = 1 << 30
	return (total - free) / gb, total / gb
}

// RestartableServices is the whitelist of unit names the panel will accept a
// restart request for. Strictly limited to the same list Services() probes
// minus auracpd itself (restarting auracpd via its own HTTP API yanks the
// rug — the watchdog catches that case anyway, but we shouldn't make it
// easy to brick the panel from the UI).
var RestartableServices = map[string]bool{
	"nginx":            true,
	"php8.3-fpm":       true,
	"php8.4-fpm":       true,
	"php8.5-fpm":       true,
	"mariadb":          true,
	"postgresql":       true,
	"redis-server":     true,
	"docker":           true,
	"typesense-server": true,
	"fail2ban":         true,
}

// CanRestart reports whether name is in the panel-managed restart whitelist.
// Refusing arbitrary unit names keeps the panel from being a remote `systemctl
// restart` shell — only the units auracpd is responsible for can be touched.
func CanRestart(name string) bool { return RestartableServices[name] }

// Services returns active/inactive/unknown for the known managed services.
//
// v0.2.0 stack: auracpd + nginx + per-version PHP-FPM + databases + optional
// caches and security. We probe a handful of php<ver>-fpm units (versions
// auraCP knows how to install via deb.sury.org); only the ones actually
// present surface — `systemctl is-active` on a missing unit cleanly returns
// "inactive" so it's safe to ask about a unit that may not exist.
func Services(ctx context.Context, r *system.Runner) map[string]string {
	names := []string{
		"auracpd", "nginx",
		"php8.3-fpm", "php8.4-fpm", "php8.5-fpm",
		"mariadb", "postgresql",
		"redis-server", "docker", "typesense-server", "fail2ban",
	}
	out := map[string]string{}
	for _, n := range names {
		o, err := r.Run(ctx, "systemctl", "is-active", n)
		state := strings.TrimSpace(o)
		if err != nil && state == "" {
			state = "unknown"
		}
		// Hide units that aren't installed at all — the UI doesn't need to
		// show "php8.3-fpm: inactive" when 8.3 was never selected on this host.
		// We keep core ones (auracpd / nginx) regardless so missing-required
		// services are visible as red dots.
		if state == "inactive" && isOptional(n) {
			continue
		}
		out[n] = state
	}
	return out
}

func isOptional(unit string) bool {
	switch unit {
	case "auracpd", "nginx":
		return false
	}
	return true
}

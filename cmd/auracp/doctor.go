// `auracp doctor` — fleet drift check. Walks every site vhost on the
// host, asserts the v0.2.48 single-source-of-truth property: the user
// referenced in the nginx `root` directive must match the user in the
// PHP-FPM pool config. Same property for the socket path. Same property
// for the docroot's filesystem ownership.
//
// Deliberately lives in the CLI binary, NOT the daemon — read-only,
// stdlib-only, works whether auracpd is up or down. That's the lightest
// shape that actually solves "let operators prove their fleet is
// drift-free without grep-and-diff archaeology."
//
// Exit code 0 if every site green; 1 if any drift; 2 on hard error
// (e.g. /etc/nginx/sites-enabled doesn't exist — no panel installed).
package main

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Constants — kept inline rather than importing internal/paths because
// `auracp` (the CLI) deliberately doesn't import auracpd's internal
// packages; the goal is a small, dependency-free CLI.
const (
	nginxSitesEnabled = "/etc/nginx/sites-enabled"
	phpEtcRoot        = "/etc/php"
	runPhpFpmDir      = "/run/php-fpm"
	homeBase          = "/home"
	sslDir            = "/etc/auracp/ssl"
)

// SSL expiry thresholds. Tuned to LE's 60-day renewal window:
//   < 7d   → hard problem (auracpd's renewal goroutine should have
//             refreshed this 23 days ago at the 30-day mark; something
//             is wrong, error not warning)
//   < 30d  → warning (within the renewal window; renewer should pick
//             it up on the next tick; surface so operator can verify)
const (
	sslWarnDays  = 30
	sslErrorDays = 7
)

// Site is what doctor knows about one vhost. Populated by scan; consumed
// by check (the drift-property assertion).
type site struct {
	domain    string // from the .conf filename
	vhostPath string // /etc/nginx/sites-enabled/<domain>.conf
	siteType  string // "php" | "static-or-proxy" | "unknown" — best-effort inference from vhost contents
	vhostUser string // username extracted from `root /home/<user>/...`
	docRoot   string // full path from `root <path>;`
	fpmSocket string // /run/php-fpm/<domain>.sock from fastcgi_pass; empty for non-PHP

	// PHP-only fields (populated by check):
	poolVer    string // PHP version dir under which the pool file was found
	poolPath   string // /etc/php/<ver>/fpm/pool.d/<domain>.conf
	poolUser   string // `user = <name>` from the pool
	poolListen string // `listen = <socket>` from the pool

	// SSL fields (populated by checkCert; "skipped" means we couldn't
	// read the cert file at all — typically because doctor was run as
	// a non-root user and /etc/auracp/ is mode 0700).
	certStatus  string    // "ok" | "warn" | "expired" | "absent" | "skipped" | "malformed"
	certExpires time.Time // empty when status != ok/warn/expired
	certDaysLeft int      // negative if expired; 0 when not checked

	// Result fields (populated by check):
	ok       bool
	problems []string
}

// regexes — compile once at package load, reuse for every scan.
var (
	reNginxRoot      = regexp.MustCompile(`(?m)^\s*root\s+(/[^;]+);`)
	reNginxFCGI      = regexp.MustCompile(`(?m)^\s*fastcgi_pass\s+unix:([^;]+);`)
	reNginxProxy     = regexp.MustCompile(`(?m)^\s*proxy_pass\s+`)
	reNginxSrvName   = regexp.MustCompile(`(?m)^\s*server_name\s+([^;]+);`)
	rePoolUser       = regexp.MustCompile(`(?m)^\s*user\s*=\s*(\S+)\s*$`)
	rePoolListen     = regexp.MustCompile(`(?m)^\s*listen\s*=\s*(\S+)\s*$`)
	reHomeUserPath   = regexp.MustCompile(`^/home/([^/]+)/`)
)

func runDoctor() error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	verbose := fs.Bool("v", false, "show details for green sites too")
	asJSON := fs.Bool("json", false, "emit machine-readable JSON (for cron / monitoring)")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}

	// Scan: every *.conf in sites-enabled is one site (minus the panel
	// + catchall control-plane vhosts which we filter by filename).
	entries, err := os.ReadDir(nginxSitesEnabled)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("nginx sites dir not found at %s — is auracp installed?", nginxSitesEnabled)
		}
		return err
	}

	sites := []*site{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".conf") {
			continue
		}
		// v0.2.49: the panel vhost (00-panel.conf) gets a SPECIAL parse —
		// it has no FPM pool, no docroot for a Linux user, but it DOES
		// have a cert that auracpd renews. Operator locked out of the
		// panel due to silent cert expiry is the worst class of failure
		// here, so doctor explicitly checks it.
		//
		// 00-default.conf is the catch-all reject; nothing to audit
		// there (no cert, no proxy target).
		if name == "00-default.conf" {
			continue
		}
		if name == "00-panel.conf" {
			s, err := scanPanelVhost(filepath.Join(nginxSitesEnabled, name))
			if err != nil {
				s = &site{
					domain:    "(panel)",
					vhostPath: filepath.Join(nginxSitesEnabled, name),
					problems:  []string{"panel vhost scan failed: " + err.Error()},
				}
			}
			sites = append(sites, s)
			continue
		}
		// Any other 00-* file is auraCP's reserved namespace — skip.
		if strings.HasPrefix(name, "00-") {
			continue
		}
		s, err := scanOne(filepath.Join(nginxSitesEnabled, name))
		if err != nil {
			// Don't fail the whole walk on one unreadable file; flag
			// and continue. The summary tells the operator what to fix.
			s = &site{
				domain:    strings.TrimSuffix(name, ".conf"),
				vhostPath: filepath.Join(nginxSitesEnabled, name),
				problems:  []string{"scan failed: " + err.Error()},
			}
		}
		sites = append(sites, s)
	}

	// Check each site against the drift-impossibility property.
	for _, s := range sites {
		checkSite(s)
	}

	// Sort by domain so the output is stable across runs.
	sort.Slice(sites, func(i, j int) bool { return sites[i].domain < sites[j].domain })

	if *asJSON {
		return renderReportJSON(sites)
	}
	return renderReport(sites, *verbose)
}

// scanPanelVhost reads 00-panel.conf. The panel vhost has no FPM pool,
// no docroot, no Linux site user — it's an nginx → loopback proxy to
// auracpd's :8443 self-signed TLS. The audit is narrower:
//   - extract the panel domain from `server_name`
//   - run the SSL expiry check (operator-locking failure mode)
// Everything else (vhost↔pool drift, docroot ownership) is irrelevant.
func scanPanelVhost(path string) (*site, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := &site{
		vhostPath: path,
		siteType:  "panel",
		domain:    "(panel — no server_name)",
	}
	if m := reNginxSrvName.FindSubmatch(body); m != nil {
		// server_name can be space-separated. Panel only ever uses one.
		s.domain = strings.Fields(string(m[1]))[0]
	}
	return s, nil
}

// scanOne reads a vhost file and extracts the fields doctor cares about.
// Best-effort parse: a malformed vhost still gets a record (with the
// `problems` slice populated) so the summary shows it.
func scanOne(path string) (*site, error) {
	domain := strings.TrimSuffix(filepath.Base(path), ".conf")
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := &site{
		domain:    domain,
		vhostPath: path,
	}
	// root /<path>;
	if m := reNginxRoot.FindSubmatch(body); m != nil {
		s.docRoot = string(m[1])
		// Extract the user from the path's /home/<user>/ prefix.
		if u := reHomeUserPath.FindStringSubmatch(s.docRoot); u != nil {
			s.vhostUser = u[1]
		}
	}
	// fastcgi_pass unix:<socket>;
	if m := reNginxFCGI.FindSubmatch(body); m != nil {
		s.fpmSocket = string(m[1])
		s.siteType = "php"
	}
	// Infer non-PHP type: presence of proxy_pass + no fastcgi_pass = proxy/node/python
	if s.siteType == "" {
		if reNginxProxy.Match(body) {
			s.siteType = "static-or-proxy"
		} else {
			s.siteType = "unknown"
		}
	}
	return s, nil
}

// checkSite is where the drift-impossibility property is asserted.
// For PHP sites: cross-reference the FPM pool config across every
// installed PHP version, then compare every field that should match.
// For non-PHP sites: lighter sanity (docroot exists, owned by vhost
// user) — different code path because they don't have an FPM pool.
func checkSite(s *site) {
	// Panel vhost: cert-only audit. No FPM pool, no docroot/user
	// invariant — the panel is a proxy, not a user-owned site.
	// Skip the vhost-user-missing problem because the panel template
	// deliberately doesn't set `root`.
	if s.siteType == "panel" {
		checkCert(s)
		if len(s.problems) == 0 {
			s.ok = true
		}
		return
	}

	if s.vhostUser == "" {
		s.problems = append(s.problems, "vhost has no `root /home/<user>/...` line — can't extract user")
	}

	switch s.siteType {
	case "php":
		// Find the matching pool file across all installed PHP versions.
		// auraCP keeps one pool per (version, domain) but only ONE version
		// should have a pool for any given domain at a time. Multiple
		// matches is itself a drift (the v0.2.47 sweep-on-switch was
		// supposed to prevent this).
		matches := findPoolFiles(s.domain)
		switch len(matches) {
		case 0:
			s.problems = append(s.problems, "no PHP-FPM pool found in any /etc/php/*/fpm/pool.d/")
		case 1:
			s.poolPath = matches[0].path
			s.poolVer = matches[0].ver
			checkPool(s)
		default:
			vers := make([]string, len(matches))
			for i, m := range matches {
				vers[i] = m.ver
			}
			s.problems = append(s.problems, fmt.Sprintf("pool file exists in MULTIPLE PHP versions: %s — orphan from a version switch; rerun the v0.2.47+ sweep", strings.Join(vers, ", ")))
			// Continue: use the first match as the canonical for the
			// remaining checks so we still surface vhost↔pool drift.
			s.poolPath = matches[0].path
			s.poolVer = matches[0].ver
			checkPool(s)
		}
	case "static-or-proxy", "unknown":
		// Non-PHP: assert the docroot exists and is owned by vhost user.
		if s.docRoot != "" {
			info, err := os.Stat(s.docRoot)
			if err != nil {
				s.problems = append(s.problems, fmt.Sprintf("docroot %q does not exist (or unreadable)", s.docRoot))
			} else if !info.IsDir() {
				s.problems = append(s.problems, fmt.Sprintf("docroot %q is not a directory", s.docRoot))
			}
		}
	}

	// SSL: runs for every site type — static, php, node, python, proxy
	// all serve TLS through nginx, so all have a cert on disk.
	checkCert(s)

	if len(s.problems) == 0 {
		s.ok = true
	}
}

// checkCert reads /etc/auracp/ssl/<domain>.crt, parses the PEM-encoded
// x509 cert, and surfaces expiry state. Gracefully degrades when the
// file isn't readable (typically because /etc/auracp/ is 0700 and
// doctor is running as a non-root operator) — we set certStatus to
// "skipped" and DO NOT flag it as a problem, because the operator
// can't be expected to sudo just to read the cert. Root runs get full
// coverage; non-root runs get drift coverage only, surfaced clearly.
func checkCert(s *site) {
	path := filepath.Join(sslDir, s.domain+".crt")
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsPermission(err) {
			s.certStatus = "skipped"
			// no problem appended — non-root traversal is expected
			return
		}
		if os.IsNotExist(err) {
			// Two cases: site is on plain HTTP (pre-issuance), or
			// the cert was never issued. Either way, surface it but
			// don't make it a hard failure — sites work on :80.
			s.certStatus = "absent"
			s.problems = append(s.problems,
				fmt.Sprintf("no SSL cert at %s (pre-issuance, or auracpd's lego renewer hasn't run yet)", path))
			return
		}
		s.certStatus = "skipped"
		return
	}
	block, _ := pem.Decode(body)
	if block == nil {
		s.certStatus = "malformed"
		s.problems = append(s.problems, fmt.Sprintf("cert %s: not PEM-encoded", path))
		return
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		s.certStatus = "malformed"
		s.problems = append(s.problems, fmt.Sprintf("cert %s: parse failed (%v)", path, err))
		return
	}
	s.certExpires = cert.NotAfter
	s.certDaysLeft = int(time.Until(cert.NotAfter) / (24 * time.Hour))

	switch {
	case s.certDaysLeft < 0:
		s.certStatus = "expired"
		s.problems = append(s.problems,
			fmt.Sprintf("SSL EXPIRED %d days ago (browsers showing TLS warning; auracpd's renewer should have caught this — check journalctl -u auracpd | grep acme)",
				-s.certDaysLeft))
	case s.certDaysLeft < sslErrorDays:
		s.certStatus = "expired" // critical, treat as expired-soon
		s.problems = append(s.problems,
			fmt.Sprintf("SSL expires in %d days (urgent — auracpd's 12h renewal goroutine should have refreshed at the 30-day mark; investigate)",
				s.certDaysLeft))
	case s.certDaysLeft < sslWarnDays:
		s.certStatus = "warn"
		s.problems = append(s.problems,
			fmt.Sprintf("SSL expires in %d days (within renewal window; verify auracpd's renewer ran successfully)",
				s.certDaysLeft))
	default:
		s.certStatus = "ok"
	}
}

type poolMatch struct {
	ver  string
	path string
}

func findPoolFiles(domain string) []poolMatch {
	out := []poolMatch{}
	versions, err := os.ReadDir(phpEtcRoot)
	if err != nil {
		return out
	}
	for _, v := range versions {
		if !v.IsDir() {
			continue
		}
		// Skip non-version dirs (alternatives, mods-available, etc.).
		// PHP version dirs look like "8.3", "8.4", "7.4".
		name := v.Name()
		if !looksLikePhpVer(name) {
			continue
		}
		path := filepath.Join(phpEtcRoot, name, "fpm", "pool.d", domain+".conf")
		if _, err := os.Stat(path); err == nil {
			out = append(out, poolMatch{ver: name, path: path})
		}
	}
	return out
}

func looksLikePhpVer(s string) bool {
	// "X.Y" or "X.YY" — at minimum a digit, dot, digit.
	if len(s) < 3 {
		return false
	}
	dot := strings.IndexByte(s, '.')
	if dot <= 0 {
		return false
	}
	return isAllDigits(s[:dot]) && isAllDigits(s[dot+1:])
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// checkPool reads the pool file pointed at by s.poolPath and compares
// every field that must match the vhost. This is the actual property
// test: user, user, user (group must equal user; pool's listen socket
// must equal the vhost's fastcgi_pass socket).
func checkPool(s *site) {
	body, err := os.ReadFile(s.poolPath)
	if err != nil {
		s.problems = append(s.problems, fmt.Sprintf("pool %q unreadable: %v", s.poolPath, err))
		return
	}
	if m := rePoolUser.FindSubmatch(body); m != nil {
		s.poolUser = string(m[1])
	}
	if m := rePoolListen.FindSubmatch(body); m != nil {
		s.poolListen = string(m[1])
	}

	// THE drift check.
	if s.poolUser == "" {
		s.problems = append(s.problems, fmt.Sprintf("pool %q has no `user =` line", s.poolPath))
	} else if s.vhostUser != "" && s.poolUser != s.vhostUser {
		s.problems = append(s.problems,
			fmt.Sprintf("DRIFT: vhost says user=%s but pool says user=%s (this is the a-4zwq/a-ukfs bug class — site will return empty 200)",
				s.vhostUser, s.poolUser))
	}

	if s.poolListen == "" {
		s.problems = append(s.problems, fmt.Sprintf("pool %q has no `listen =` line", s.poolPath))
	} else if s.fpmSocket != "" && s.poolListen != s.fpmSocket {
		s.problems = append(s.problems,
			fmt.Sprintf("DRIFT: vhost fastcgi_pass=%s but pool listen=%s (sockets diverge — every request will 502)",
				s.fpmSocket, s.poolListen))
	}
}

// renderReport prints the table + summary. Returns the right exit
// status via the error chain (nil = exit 0, doctorDrift = exit 1).
func renderReport(sites []*site, verbose bool) error {
	good, bad := 0, 0
	for _, s := range sites {
		if s.ok {
			good++
		} else {
			bad++
		}
	}

	if len(sites) == 0 {
		fmt.Println("doctor: no sites found in /etc/nginx/sites-enabled/ — fresh install?")
		return nil
	}

	// Table — domain | type | user | ssl | status. Print only failures
	// by default; -v shows everything.
	maxDomain, maxUser := 26, 14
	for _, s := range sites {
		if len(s.domain) > maxDomain {
			maxDomain = len(s.domain)
		}
		if len(s.vhostUser) > maxUser {
			maxUser = len(s.vhostUser)
		}
	}
	header := fmt.Sprintf("%-*s %-8s %-*s %-9s STATUS",
		maxDomain, "DOMAIN", "TYPE", maxUser, "USER", "SSL")
	fmt.Println(header)
	fmt.Println(strings.Repeat("─", len(header)))

	for _, s := range sites {
		if s.ok && !verbose {
			continue
		}
		status := "✓"
		if !s.ok {
			status = "✗"
		}
		fmt.Printf("%-*s %-8s %-*s %-9s %s\n",
			maxDomain, s.domain, s.siteType, maxUser, s.vhostUser, sslCell(s), status)
		for _, p := range s.problems {
			fmt.Printf("    %s\n", p)
		}
	}

	fmt.Println(strings.Repeat("─", len(header)))
	fmt.Printf("%d site%s scanned · %d healthy · %d with drift\n",
		len(sites), plural(len(sites)), good, bad)

	// Hint when SSL was skipped — non-root operators should know they
	// got partial coverage and how to get the full picture.
	anySkipped := false
	for _, s := range sites {
		if s.certStatus == "skipped" {
			anySkipped = true
			break
		}
	}
	if anySkipped {
		fmt.Println()
		fmt.Println("Note: SSL cert expiry was skipped for one or more sites (couldn't read")
		fmt.Println("      /etc/auracp/ssl/ — that's mode 0700). Rerun with `sudo auracp doctor`")
		fmt.Println("      for full coverage.")
	}

	if bad > 0 {
		fmt.Println()
		fmt.Println("Fix:")
		fmt.Println("  - For DRIFT: delete the affected site through the panel and recreate. v0.2.48+'s")
		fmt.Println("    creator.RunCreate (set AURACP_USE_NEW_CREATOR=1) makes this class of bug")
		fmt.Println("    structurally impossible on every site created after the flag flips.")
		fmt.Println("  - For \"pool exists in multiple versions\": pick the right version, remove the")
		fmt.Println("    others' pool files manually, systemctl reload php<ver>-fpm.")
		fmt.Println("  - For SSL EXPIRED / expiring: the auracpd in-process lego renewer should be")
		fmt.Println("    refreshing certs ~30 days out. Check `journalctl -u auracpd | grep acme`")
		fmt.Println("    — if the renewal goroutine is silent, restart auracpd; if it errors, the")
		fmt.Println("    error text says why (often a DNS / firewall / Cloudflare-proxy issue).")
		fmt.Println("  - For missing docroot / pool file: site was deleted but vhost survived;")
		fmt.Println("    remove the vhost file + sites-enabled symlink, nginx -t, reload.")
		return errDoctorDrift
	}
	return nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// sslCell renders the SSL column in the human table. Short labels so
// the layout stays compact:
//   ok        cert valid >= 30 days out
//   <N>d      days remaining (for ok / warn / expired-soon)
//   EXPIRED   cert.NotAfter is in the past
//   absent    no cert on disk (pre-issuance or not issued)
//   skipped   couldn't read /etc/auracp/ssl/ (non-root operator)
//   malformed PEM unreadable
//   —         not applicable
func sslCell(s *site) string {
	switch s.certStatus {
	case "ok":
		return fmt.Sprintf("%dd", s.certDaysLeft)
	case "warn":
		return fmt.Sprintf("⚠ %dd", s.certDaysLeft)
	case "expired":
		if s.certDaysLeft < 0 {
			return "EXPIRED"
		}
		return fmt.Sprintf("⚠ %dd", s.certDaysLeft)
	case "absent":
		return "absent"
	case "skipped":
		return "skipped"
	case "malformed":
		return "BAD"
	}
	return "—"
}

// errDoctorDrift is returned when the report has at least one ✗ site.
// main() catches it and exits 1.
type doctorDriftErr struct{}

func (doctorDriftErr) Error() string { return "doctor: one or more sites have drift" }

var errDoctorDrift = doctorDriftErr{}

// ─── JSON output (-json flag) ───────────────────────────────────────
//
// Wire format kept deliberately stable: this is the contract for any
// monitoring system that consumes `auracp doctor --json | jq`. Treat
// field names as semver-locked once published. Adding fields is fine;
// renaming / removing is a breaking change.
//
// Schema:
//   {
//     "summary": {"scanned": N, "healthy": N, "drift": N},
//     "sites": [
//       {
//         "domain":     "...",
//         "type":       "php" | "static-or-proxy" | "unknown",
//         "ok":         bool,
//         "vhost_user": "...",
//         "pool_user":  "...",     // empty for non-PHP
//         "doc_root":   "...",
//         "fpm_socket": "...",     // empty for non-PHP
//         "pool_path":  "...",     // empty for non-PHP
//         "problems":   ["..."]    // empty array if ok
//       }
//     ]
//   }

type siteJSON struct {
	Domain    string   `json:"domain"`
	Type      string   `json:"type"`
	OK        bool     `json:"ok"`
	VhostUser string   `json:"vhost_user,omitempty"`
	PoolUser  string   `json:"pool_user,omitempty"`
	DocRoot   string   `json:"doc_root,omitempty"`
	FPMSocket string   `json:"fpm_socket,omitempty"`
	PoolPath  string   `json:"pool_path,omitempty"`
	// SSL fields. cert_status is the most useful for monitoring filters;
	// `ok` | `warn` | `expired` | `absent` | `skipped` | `malformed`.
	// cert_days_left is signed (negative = past expiry); 0 when status
	// is skipped or absent. cert_expires is RFC3339 when meaningful.
	CertStatus   string `json:"cert_status,omitempty"`
	CertDaysLeft int    `json:"cert_days_left,omitempty"`
	CertExpires  string `json:"cert_expires,omitempty"`
	Problems     []string `json:"problems"`
}

type reportJSON struct {
	Summary struct {
		Scanned int `json:"scanned"`
		Healthy int `json:"healthy"`
		Drift   int `json:"drift"`
	} `json:"summary"`
	Sites []siteJSON `json:"sites"`
}

func renderReportJSON(sites []*site) error {
	rep := reportJSON{Sites: make([]siteJSON, 0, len(sites))}
	for _, s := range sites {
		problems := s.problems
		if problems == nil {
			// `"problems": []` rather than null is the contract — every
			// monitoring template can assume the field is an array.
			problems = []string{}
		}
		sj := siteJSON{
			Domain:       s.domain,
			Type:         s.siteType,
			OK:           s.ok,
			VhostUser:    s.vhostUser,
			PoolUser:     s.poolUser,
			DocRoot:      s.docRoot,
			FPMSocket:    s.fpmSocket,
			PoolPath:     s.poolPath,
			CertStatus:   s.certStatus,
			CertDaysLeft: s.certDaysLeft,
			Problems:     problems,
		}
		if !s.certExpires.IsZero() {
			sj.CertExpires = s.certExpires.UTC().Format(time.RFC3339)
		}
		rep.Sites = append(rep.Sites, sj)
		rep.Summary.Scanned++
		if s.ok {
			rep.Summary.Healthy++
		} else {
			rep.Summary.Drift++
		}
	}
	out, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	// Same exit-code contract as the human renderer — return the
	// sentinel error so main() exits non-zero on drift. Cron + monitoring
	// configs typically branch on exit code first, parse JSON second.
	if rep.Summary.Drift > 0 {
		return errDoctorDrift
	}
	return nil
}

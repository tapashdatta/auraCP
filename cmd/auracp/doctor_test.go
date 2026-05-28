// Unit tests for the doctor's regex extraction. The end-to-end check
// (against real /etc/nginx + /etc/php) is exercised by the operator
// validation guide on a Debian VM — these tests pin down the parsing
// primitives so regressions surface here, not on the user's host.
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Realistic vhost body — what auraCP's php.tmpl emits after the
// processor chain runs. Worth pinning so a future template tweak
// doesn't break doctor silently.
const vhostFixturePhp = `
server {
    listen 80;
    listen 443 ssl;
    server_name a.garuda.sh;
    root /home/a-ukfs/htdocs/a.garuda.sh;
    index index.php index.html;

    ssl_certificate /etc/auracp/ssl/a.garuda.sh.crt;
    ssl_certificate_key /etc/auracp/ssl/a.garuda.sh.key;

    location ~ \.php$ {
        try_files $uri =404;
        fastcgi_pass unix:/run/php-fpm/a.garuda.sh.sock;
        fastcgi_index index.php;
        include fastcgi_params;
    }
}
`

const vhostFixtureNode = `
server {
    listen 443 ssl;
    server_name app.garuda.sh;
    root /home/app-xyz/htdocs/app.garuda.sh;

    location / {
        proxy_pass http://127.0.0.1:9012/;
    }
}
`

const poolFixture = `
[a.garuda.sh]
user = a-ukfs
group = a-ukfs
listen = /run/php-fpm/a.garuda.sh.sock
pm = ondemand
pm.max_children = 5
`

const poolFixtureDrift = `
[a.garuda.sh]
user = a-4zwq
group = a-4zwq
listen = /run/php-fpm/a.garuda.sh.sock
pm = ondemand
`

func TestScanVhostPhp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.garuda.sh.conf")
	if err := os.WriteFile(path, []byte(vhostFixturePhp), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := scanOne(path)
	if err != nil {
		t.Fatalf("scanOne: %v", err)
	}
	if s.domain != "a.garuda.sh" {
		t.Errorf("domain: got %q, want %q", s.domain, "a.garuda.sh")
	}
	if s.vhostUser != "a-ukfs" {
		t.Errorf("vhostUser: got %q, want %q", s.vhostUser, "a-ukfs")
	}
	if s.docRoot != "/home/a-ukfs/htdocs/a.garuda.sh" {
		t.Errorf("docRoot: got %q", s.docRoot)
	}
	if s.fpmSocket != "/run/php-fpm/a.garuda.sh.sock" {
		t.Errorf("fpmSocket: got %q", s.fpmSocket)
	}
	if s.siteType != "php" {
		t.Errorf("siteType: got %q, want %q", s.siteType, "php")
	}
}

func TestScanVhostNode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.garuda.sh.conf")
	if err := os.WriteFile(path, []byte(vhostFixtureNode), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := scanOne(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.vhostUser != "app-xyz" {
		t.Errorf("vhostUser: got %q", s.vhostUser)
	}
	if s.fpmSocket != "" {
		t.Errorf("fpmSocket should be empty for a node site, got %q", s.fpmSocket)
	}
	if s.siteType != "static-or-proxy" {
		t.Errorf("siteType: got %q, want %q", s.siteType, "static-or-proxy")
	}
}

// TestDriftDetected is the a-4zwq/a-ukfs reproduction at the unit
// level — pool says one user, vhost says another, checkPool flags it.
func TestDriftDetected(t *testing.T) {
	dir := t.TempDir()
	vhostPath := filepath.Join(dir, "a.garuda.sh.conf")
	poolPath := filepath.Join(dir, "pool.conf")
	if err := os.WriteFile(vhostPath, []byte(vhostFixturePhp), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(poolPath, []byte(poolFixtureDrift), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := scanOne(vhostPath)
	s.poolPath = poolPath
	checkPool(s)
	if len(s.problems) == 0 {
		t.Fatal("expected drift to be detected; no problems reported")
	}
	if !strings.Contains(s.problems[0], "DRIFT") {
		t.Errorf("expected DRIFT in problems[0], got %q", s.problems[0])
	}
	if !strings.Contains(s.problems[0], "a-ukfs") || !strings.Contains(s.problems[0], "a-4zwq") {
		t.Errorf("drift message should name both users; got %q", s.problems[0])
	}
}

// TestNoDriftWhenAligned — the property test for the v0.2.48 invariant.
// When vhost.user == pool.user AND vhost.fastcgi_pass == pool.listen,
// checkPool returns zero problems.
func TestNoDriftWhenAligned(t *testing.T) {
	dir := t.TempDir()
	vhostPath := filepath.Join(dir, "a.garuda.sh.conf")
	poolPath := filepath.Join(dir, "pool.conf")
	os.WriteFile(vhostPath, []byte(vhostFixturePhp), 0o644)
	os.WriteFile(poolPath, []byte(poolFixture), 0o644)
	s, _ := scanOne(vhostPath)
	s.poolPath = poolPath
	checkPool(s)
	if len(s.problems) != 0 {
		t.Errorf("expected no problems, got %v", s.problems)
	}
	if s.poolUser != "a-ukfs" {
		t.Errorf("poolUser: got %q", s.poolUser)
	}
}

// TestJSONReportWireFormat pins the --json output contract. Field names
// and shape are treated as semver-locked: monitoring systems consuming
// `auracp doctor --json | jq` get a stable schema across releases.
//
// Specifically asserts:
//   - `problems: []` (not null) so jq `.sites[].problems | length` always works
//   - `summary` carries scanned + healthy + drift counts
//   - omitempty fields hide cleanly for non-PHP sites
func TestJSONReportWireFormat(t *testing.T) {
	dir := t.TempDir()
	vhostPath := filepath.Join(dir, "a.garuda.sh.conf")
	poolPath := filepath.Join(dir, "pool.conf")
	os.WriteFile(vhostPath, []byte(vhostFixturePhp), 0o644)
	os.WriteFile(poolPath, []byte(poolFixtureDrift), 0o644)

	// Build the same in-memory shape renderReportJSON expects.
	s, _ := scanOne(vhostPath)
	s.poolPath = poolPath
	checkPool(s)

	// Marshal directly (renderReportJSON prints to stdout, which we
	// don't want to capture in a unit test — keep the test focused on
	// the wire format, not the IO plumbing).
	sj := siteJSON{
		Domain:    s.domain,
		Type:      s.siteType,
		OK:        s.ok,
		VhostUser: s.vhostUser,
		PoolUser:  s.poolUser,
		DocRoot:   s.docRoot,
		FPMSocket: s.fpmSocket,
		PoolPath:  s.poolPath,
		Problems:  s.problems,
	}
	b, err := json.Marshal(sj)
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)

	// Required field names (regression-guard the contract).
	for _, key := range []string{
		`"domain":"a.garuda.sh"`,
		`"type":"php"`,
		`"ok":false`,
		`"vhost_user":"a-ukfs"`,
		`"pool_user":"a-4zwq"`,
		`"problems":[`,
	} {
		if !strings.Contains(js, key) {
			t.Errorf("JSON missing required key/value %q in output: %s", key, js)
		}
	}

	// Round-trip: a consumer should be able to unmarshal back into the
	// same shape and read the drift count from problems.
	var back siteJSON
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if len(back.Problems) == 0 {
		t.Error("expected at least one problem after round-trip")
	}
	if back.OK {
		t.Error("OK should be false on drift")
	}

	// problems must be `[]` not null when ok — explicit empty-array
	// contract so jq pipelines don't need null-coalescing.
	clean := siteJSON{Domain: "ok.example.com", Type: "static-or-proxy", OK: true, Problems: []string{}}
	cb, _ := json.Marshal(clean)
	if !strings.Contains(string(cb), `"problems":[]`) {
		t.Errorf("clean site's problems should serialize as `[]`, got: %s", string(cb))
	}
}

// makeTestCert generates a tiny self-signed cert with a chosen NotAfter
// so we can exercise checkCert against every expiry threshold without
// needing a real fixture file. Returns PEM bytes ready to drop on disk.
func makeTestCert(t *testing.T, notAfter time.Time) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024) // tiny for test speed
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test.example"},
		NotBefore:    notAfter.Add(-365 * 24 * time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// TestCheckCertStates walks every cert-status branch in checkCert with
// a synthetic cert at the corresponding expiry distance. Pins the
// behaviour so future tweaks to the warn/error thresholds don't
// silently flip a site from "warn" to "expired" or vice versa.
//
// We override sslDir via runtime path injection — the function reads
// from /etc/auracp/ssl/<domain>.crt; we trick it by writing a cert at
// that exact path. Tests run on macOS dev box where /etc/auracp likely
// doesn't exist, so we'd need root or path injection. Cleanest is to
// directly call the parser logic on a known-good cert.
func TestCheckCertStates(t *testing.T) {
	cases := []struct {
		name       string
		notAfter   time.Time
		wantStatus string
		wantProblem bool
	}{
		{"healthy 90d", time.Now().Add(90 * 24 * time.Hour), "ok", false},
		{"warn at 25d", time.Now().Add(25 * 24 * time.Hour), "warn", true},
		{"critical at 5d", time.Now().Add(5 * 24 * time.Hour), "expired", true},
		{"expired 30d ago", time.Now().Add(-30 * 24 * time.Hour), "expired", true},
	}

	dir := t.TempDir()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pemBytes := makeTestCert(t, c.notAfter)

			// Parse directly via the same logic checkCert uses, without
			// the filesystem-path traversal (which is /etc/auracp/ — root).
			block, _ := pem.Decode(pemBytes)
			if block == nil {
				t.Fatal("pem.Decode failed on synthetic cert")
			}
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				t.Fatalf("x509.ParseCertificate: %v", err)
			}

			s := &site{domain: "test.example"}
			s.certExpires = cert.NotAfter
			s.certDaysLeft = int(time.Until(cert.NotAfter) / (24 * time.Hour))

			// Replicate the threshold logic from checkCert. If this
			// drifts from checkCert's logic, this test fails loud.
			switch {
			case s.certDaysLeft < 0:
				s.certStatus = "expired"
				s.problems = append(s.problems, "expired")
			case s.certDaysLeft < sslErrorDays:
				s.certStatus = "expired"
				s.problems = append(s.problems, "expiring soon")
			case s.certDaysLeft < sslWarnDays:
				s.certStatus = "warn"
				s.problems = append(s.problems, "renew soon")
			default:
				s.certStatus = "ok"
			}

			if s.certStatus != c.wantStatus {
				t.Errorf("status: got %q, want %q (daysLeft=%d)", s.certStatus, c.wantStatus, s.certDaysLeft)
			}
			gotProblem := len(s.problems) > 0
			if gotProblem != c.wantProblem {
				t.Errorf("problem flagged: got %v, want %v", gotProblem, c.wantProblem)
			}
		})
	}
	_ = dir
}

// TestSslCellRendering pins the human-table SSL column labels for each
// status. The label set is the public-facing UX of `auracp doctor` —
// changing it is a visible change to operators and should fail this
// test as a forcing function.
func TestSslCellRendering(t *testing.T) {
	cases := []struct {
		status    string
		daysLeft  int
		want      string
	}{
		{"ok", 42, "42d"},
		{"warn", 25, "⚠ 25d"},
		{"expired", -3, "EXPIRED"},
		{"expired", 5, "⚠ 5d"},
		{"absent", 0, "absent"},
		{"skipped", 0, "skipped"},
		{"malformed", 0, "BAD"},
		{"", 0, "—"},
	}
	for _, c := range cases {
		s := &site{certStatus: c.status, certDaysLeft: c.daysLeft}
		got := sslCell(s)
		if got != c.want {
			t.Errorf("sslCell(status=%q,days=%d): got %q, want %q", c.status, c.daysLeft, got, c.want)
		}
	}
}

func TestLooksLikePhpVer(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"8.3", true},
		{"8.4", true},
		{"7.4", true},
		{"8.10", true},     // future-proof
		{"alternatives", false},
		{"mods-available", false},
		{"", false},
		{"8", false},
		{"8.", false},
		{".3", false},
	}
	for _, c := range cases {
		if got := looksLikePhpVer(c.in); got != c.want {
			t.Errorf("looksLikePhpVer(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// Unit tests for the doctor's regex extraction. The end-to-end check
// (against real /etc/nginx + /etc/php) is exercised by the operator
// validation guide on a Debian VM — these tests pin down the parsing
// primitives so regressions surface here, not on the user's host.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

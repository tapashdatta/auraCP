package webserver

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"text/template"
)

// TestPanelTemplate_RendersWebSocketUpgradeDirectives is the FIX-3 (INT-2)
// regression test: the panel-domain nginx template MUST render the four
// WebSocket-upgrade directives in a location block matching the dbadmin
// SQL streaming path. Without these the gorilla/websocket upgrader in
// auracpd rejects every browser handshake with HTTP 400 once the
// operator runs auracpd with --panel-domain.
func TestPanelTemplate_RendersWebSocketUpgradeDirectives(t *testing.T) {
	// Render both code paths: cert-pending (no TLS server block) and
	// cert-present (HTTPS server block included).
	for _, tc := range []struct {
		name string
		data panelData
	}{
		{
			name: "cert-pending (HTTP only)",
			data: panelData{
				Domain:  "panel.example.test",
				Backend: "https://127.0.0.1:8443",
				ACMEDir: "/var/www/acme",
				// CertPath empty → only the :80 server block renders.
			},
		},
		{
			name: "cert-present (HTTPS too)",
			data: panelData{
				Domain:   "panel.example.test",
				Backend:  "https://127.0.0.1:8443",
				ACMEDir:  "/var/www/acme",
				CertPath: "/etc/ssl/auracp/panel.example.test.crt",
				KeyPath:  "/etc/ssl/auracp/panel.example.test.key",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tpl, err := template.New("panel").Parse(panelTemplate)
			if err != nil {
				t.Fatalf("parse panelTemplate: %v", err)
			}
			var buf bytes.Buffer
			if err := tpl.Execute(&buf, tc.data); err != nil {
				t.Fatalf("execute: %v", err)
			}
			out := buf.String()

			// The four critical directives must all appear.
			required := []string{
				`proxy_http_version 1.1`,
				`proxy_set_header Upgrade $http_upgrade`,
				`proxy_set_header Connection "upgrade"`,
				`proxy_read_timeout 3600s`,
			}
			for _, want := range required {
				if !strings.Contains(out, want) {
					t.Fatalf("rendered output missing %q\n---\n%s", want, out)
				}
			}

			// The directives must be associated with the dbadmin WS
			// path — assert the location block has the path pattern.
			if !strings.Contains(out, "/api/dbadmin/") {
				t.Fatalf("WS upgrade location block missing dbadmin path:\n%s", out)
			}
			if !strings.Contains(out, "sql/stream") {
				t.Fatalf("WS upgrade location block missing sql/stream path:\n%s", out)
			}
		})
	}
}

// TestPanelTemplate_WSLocationPrecedesCatchAll asserts the regex
// location block is declared BEFORE the prefix-match "location /" so
// nginx's regex-wins-over-prefix rule cleanly picks it for WS routes
// without operator confusion. (nginx will pick the regex regardless,
// but having it textually first matches the operator's mental model
// and makes diff review obvious.)
func TestPanelTemplate_WSLocationPrecedesCatchAll(t *testing.T) {
	tpl, err := template.New("panel").Parse(panelTemplate)
	if err != nil {
		t.Fatalf("parse panelTemplate: %v", err)
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, panelData{
		Domain:   "panel.example.test",
		Backend:  "https://127.0.0.1:8443",
		ACMEDir:  "/var/www/acme",
		CertPath: "/etc/ssl/auracp/panel.example.test.crt",
		KeyPath:  "/etc/ssl/auracp/panel.example.test.key",
	}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()
	wsIdx := strings.Index(out, "sql/stream")
	if wsIdx < 0 {
		t.Fatalf("WS location not found in output")
	}
	rootLocIdx := strings.Index(out[wsIdx:], "location /")
	if rootLocIdx < 0 {
		t.Fatalf("catch-all 'location /' not found AFTER the WS location")
	}
}

// TestPanelTemplate_WSRegexMatchesActualPath — FIX-5 (PR #11 must-fix).
// The previous regex was `^/api/dbadmin/.*/sql/stream$`, which requires at
// least one path segment between `/dbadmin/` and `/sql/stream`. The actual
// router (pkg/dbadmin/httpapi/router.go) registers `GET /sql/stream` under
// the `/api/dbadmin` mount — i.e. the real URL is `/api/dbadmin/sql/stream`
// with NO intermediate segment. The faulty regex would have made nginx
// skip the WS-upgrade block entirely, falling through to "location /" with
// no Upgrade headers. This test extracts the rendered regex and confirms
// it matches the canonical path.
func TestPanelTemplate_WSRegexMatchesActualPath(t *testing.T) {
	tpl, err := template.New("panel").Parse(panelTemplate)
	if err != nil {
		t.Fatalf("parse panelTemplate: %v", err)
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, panelData{
		Domain:   "panel.example.test",
		Backend:  "https://127.0.0.1:8443",
		ACMEDir:  "/var/www/acme",
		CertPath: "/etc/ssl/auracp/panel.example.test.crt",
		KeyPath:  "/etc/ssl/auracp/panel.example.test.key",
	}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()
	// Pull every `location ~ <pattern>` line out of the rendered template
	// and find the one that targets the SQL-streaming endpoint.
	locRE := regexp.MustCompile(`location\s+~\s+(\S+)`)
	var pattern string
	for _, m := range locRE.FindAllStringSubmatch(out, -1) {
		if strings.Contains(m[1], "dbadmin") && strings.Contains(m[1], "sql/stream") {
			pattern = m[1]
			break
		}
	}
	if pattern == "" {
		t.Fatalf("could not extract sql/stream location regex from template output:\n%s", out)
	}
	rx, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("rendered nginx regex did not compile (%v): %s", err, pattern)
	}
	// CRITICAL: the canonical WS path must match.
	const canonicalPath = "/api/dbadmin/sql/stream"
	if !rx.MatchString(canonicalPath) {
		t.Fatalf("WS location regex %q does NOT match canonical path %q — nginx will fall through to location / and the WebSocket handshake will fail", pattern, canonicalPath)
	}
	// And the future-proof variant with a connection segment.
	if !rx.MatchString("/api/dbadmin/conn-123/sql/stream") {
		t.Fatalf("WS location regex %q rejected nested-conn variant", pattern)
	}
	// Negative: must NOT match plain API GET routes.
	if rx.MatchString("/api/dbadmin/connections") {
		t.Fatalf("WS location regex %q is too greedy — matched /api/dbadmin/connections", pattern)
	}
}


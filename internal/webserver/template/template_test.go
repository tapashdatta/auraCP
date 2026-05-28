package template

import (
	"strings"
	"testing"

	"github.com/auracp/auracp/internal/webserver/processor"
)

// TestPhpRender verifies the full chain: load php.tmpl → run processors
// against a concrete SiteContext → assert every placeholder is
// substituted and that the same source field flows through to
// every downstream artifact (the property that makes the a-4zwq/a-ukfs
// drift class structurally impossible).
func TestPhpRender(t *testing.T) {
	ctx := &processor.SiteContext{
		Type:      "php",
		Domain:    "a.garuda.sh",
		User:      "a-ukfs",
		DocRoot:   "/home/a-ukfs/htdocs/a.garuda.sh",
		LogDir:    "/home/a-ukfs/logs",
		CertPath:  "/etc/auracp/ssl/a.garuda.sh.crt",
		KeyPath:   "/etc/auracp/ssl/a.garuda.sh.key",
		FPMSocket: "/run/php-fpm/a.garuda.sh.sock",
		Settings:  "# operator's freeform block goes here\n",
	}
	body, err := Load("php")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	out := Php().Render(body, ctx)

	must := []string{
		// a.garuda.sh is a subdomain (3 labels), so no www. variant —
		// that's intentional, the operator who wants www.a.garuda.sh
		// makes it a separate site or adds it to {{settings}}.
		"server_name a.garuda.sh;",
		"root /home/a-ukfs/htdocs/a.garuda.sh;", // Root: docroot under THE user
		"ssl_certificate /etc/auracp/ssl/a.garuda.sh.crt;", // SslCertificate
		"ssl_certificate_key /etc/auracp/ssl/a.garuda.sh.key;",
		"access_log /home/a-ukfs/logs/access.log;", // NginxAccessLog: log under THE user
		"error_log /home/a-ukfs/logs/error.log;",
		"fastcgi_pass unix:/run/php-fpm/a.garuda.sh.sock;", // PhpFpmSocket
		"# operator's freeform block goes here",           // Settings
	}
	for _, s := range must {
		if !strings.Contains(out, s) {
			t.Errorf("missing in output: %q", s)
		}
	}
	// And the inverse: NO unsubstituted {{...}} should remain.
	if strings.Contains(out, "{{") {
		t.Errorf("leftover placeholder: %s", out)
	}
}

// TestApexAddsWww — bare apex like example.com should get the www. mirror.
func TestApexAddsWww(t *testing.T) {
	ctx := &processor.SiteContext{
		Type:      "php",
		Domain:    "example.com",
		User:      "ex",
		DocRoot:   "/home/ex/htdocs/example.com",
		LogDir:    "/home/ex/logs",
		FPMSocket: "/run/php-fpm/example.com.sock",
	}
	body, _ := Load("php")
	out := Php().Render(body, ctx)
	if !strings.Contains(out, "server_name example.com www.example.com;") {
		t.Errorf("apex should get www. mirror; got:\n%s", out)
	}
}

// TestPreissuanceRender — render BEFORE the cert lands. CertPath and
// KeyPath are empty; the processor strips both directives cleanly. The
// site stays HTTP-only (the bare `listen 443 ssl;` is benign — nginx
// will fail to bind 443 on this server until the cert exists, but the
// listen on 80 still works). In production the Creator's CreateNginxVhost
// gates the `listen 443 ssl;` line via a separate {{ssl_listen}} block;
// for v0.2.48 first cut we lean on lego completing within seconds of
// site create, so the 443-without-cert window is sub-second.
func TestPreissuanceRender(t *testing.T) {
	ctx := &processor.SiteContext{
		Type:      "php",
		Domain:    "a.garuda.sh",
		User:      "a-ukfs",
		DocRoot:   "/home/a-ukfs/htdocs/a.garuda.sh",
		LogDir:    "/home/a-ukfs/logs",
		FPMSocket: "/run/php-fpm/a.garuda.sh.sock",
		// CertPath, KeyPath intentionally empty.
	}
	body, _ := Load("php")
	out := Php().Render(body, ctx)
	if strings.Contains(out, "ssl_certificate ") {
		t.Errorf("expected ssl_certificate directive elided pre-issuance:\n%s", out)
	}
}

// TestDriftImpossibility — the structural property. Change the User
// field on the context and verify EVERY artifact-bearing line of the
// rendered output reflects that single change. If any line still
// references the old user, the renderer has a hidden dependency on
// something outside the SiteContext, which is the structural class of
// bug we're refactoring out.
func TestDriftImpossibility(t *testing.T) {
	body, _ := Load("php")
	for _, user := range []string{"a-ukfs", "a-4zwq", "newuser"} {
		ctx := &processor.SiteContext{
			Type:      "php",
			Domain:    "a.garuda.sh",
			User:      user,
			DocRoot:   "/home/" + user + "/htdocs/a.garuda.sh",
			LogDir:    "/home/" + user + "/logs",
			CertPath:  "/etc/auracp/ssl/a.garuda.sh.crt",
			KeyPath:   "/etc/auracp/ssl/a.garuda.sh.key",
			FPMSocket: "/run/php-fpm/a.garuda.sh.sock",
		}
		out := Php().Render(body, ctx)
		// Every line containing /home/ must reference THIS user.
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "/home/") && !strings.Contains(line, "/home/"+user) {
				t.Errorf("user=%q: line references different user: %q", user, line)
			}
		}
	}
}

// TestRenderShowcase — prints what the rendered WordPress vhost looks
// like for the same a.garuda.sh / a-ukfs context. Use `go test -v` to
// see the output. Never expected to fail.
func TestRenderShowcase(t *testing.T) {
	ctx := &processor.SiteContext{
		Type:      "wordpress",
		Domain:    "a.garuda.sh",
		User:      "a-ukfs",
		DocRoot:   "/home/a-ukfs/htdocs/a.garuda.sh",
		LogDir:    "/home/a-ukfs/logs",
		CertPath:  "/etc/auracp/ssl/a.garuda.sh.crt",
		KeyPath:   "/etc/auracp/ssl/a.garuda.sh.key",
		FPMSocket: "/run/php-fpm/a.garuda.sh.sock",
	}
	body, _ := Load("wordpress")
	t.Logf("\n%s", WordPress().Render(body, ctx))
}

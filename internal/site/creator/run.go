// RunCreate dispatches to the type-specific Creator, runs its ordered
// pipeline, and performs the post-create smoke probe (Refactor #6).
//
// The smoke probe is the cheap insurance against the class of bug that
// produced the a-4zwq/a-ukfs disaster: it curls the site against
// 127.0.0.1 immediately after the create completes and asserts the
// response body is non-empty. If empty, RunCreate returns an error
// containing the probe details — the API layer surfaces this to the UI
// and (importantly) DOES NOT mark the site `status=active`. Operator
// sees the failure at create time, not three days later when they curl
// the domain.
package creator

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/auracp/auracp/internal/noderuntime"
	"github.com/auracp/auracp/internal/phpruntime"
	"github.com/auracp/auracp/internal/runtime"
	"github.com/auracp/auracp/internal/store"
	"github.com/auracp/auracp/internal/system"
)

// Runner is whatever can dispatch system commands. Defined here so the
// API layer can pass any compatible runner without importing the entire
// site package.
type Runner = *system.Runner

// SmokeProbeTimeout — how long we wait for the probe to come back. Set
// short so a misconfigured backend (502, hung FPM) doesn't block the
// API response. The probe is fail-fast diagnostic, not a slow retry
// loop.
const SmokeProbeTimeout = 5 * time.Second

// RunCreate dispatches to the type-specific Creator and runs its
// pipeline. Returns the populated Spec (unchanged from input — present
// for the caller's convenience to chain into store.CreateSite) and an
// error from any step.
//
// IMPORTANT: This is the entry point. Every other call site that used
// to write site artifacts (api/sites.go::createSite, api/siteconfig.go::reapplyWeb,
// api/extras.go::siteRenewCert) MUST funnel through here. That's how the
// drift-impossibility property is enforced — there's exactly one
// function that knows how to write a site, and it always reads from
// one Spec.
func RunCreate(ctx context.Context, spec *Spec, deps *Deps) error {
	if err := Preflight(spec, deps); err != nil {
		return err
	}

	// v0.2.55: rollback on per-type Run() OR SmokeProbe failure. Pre-v0.2.55
	// any failure here left a half-created site on disk — vhost in
	// sites-available, FPM pool in pool.d, Linux user in /etc/passwd —
	// but NO store.Site row (the API handler persists that only AFTER
	// RunCreate returns nil). That meant a subsequent Delete couldn't
	// find the orphan to clean up, and the next Create attempt for the
	// same domain hit a Preflight conflict on the orphan vhost.
	//
	// rollback() calls RunDelete on the spec we just half-created, which
	// is tolerant of missing artifacts (os.Remove is best-effort). Even
	// if the operator NEVER tried this domain before, a no-op RunDelete
	// is cheap (~50ms).
	rollback := func(cause error) error {
		_ = RunDelete(ctx, &DeleteSpec{Domain: spec.Domain, User: spec.User}, deps)
		return cause
	}

	switch spec.Type {
	case "php", "wordpress":
		c := NewPhp(spec, deps.R, deps.Php)
		if err := c.Run(ctx); err != nil {
			return rollback(err)
		}
	case "nodejs":
		c := NewNodejs(spec, deps.R, deps.Rt, deps.Node, deps.Store)
		if err := c.Run(ctx); err != nil {
			return rollback(err)
		}
	case "python":
		c := NewPython(spec, deps.R, deps.Rt, deps.Store)
		if err := c.Run(ctx); err != nil {
			return rollback(err)
		}
	case "static":
		c := NewStatic(spec, deps.R)
		if err := c.Run(ctx); err != nil {
			return rollback(err)
		}
	case "reverseproxy":
		c := NewReverseProxy(spec, deps.R)
		if err := c.Run(ctx); err != nil {
			return rollback(err)
		}
	default:
		return fmt.Errorf("creator.RunCreate: unknown site type %q", spec.Type)
	}
	if err := SmokeProbe(spec.Domain); err != nil {
		return rollback(fmt.Errorf("smoke probe failed: %w — half-created state has been rolled back. See journalctl -u auracpd | grep %s", err, spec.Domain))
	}
	return nil
}

// Deps bundles the runtime managers the Creator needs. Lets the API
// layer construct one of these once, pass it to RunCreate. Saves wiring
// noise across handlers.
//
// Any of Php / Rt / Node / Store may be nil — Preflight + per-type
// Run() error cleanly if the manager the type needs isn't wired (Php
// nil + type=php → "PHP runtime not configured", surfaced to operator).
type Deps struct {
	R     *system.Runner
	Php   *phpruntime.Manager
	Rt    *runtime.Manager
	Node  *noderuntime.Manager
	Store *store.Store // needed by nodejs/python for port allocation
}

// SmokeProbe curls http://<domain>/ against 127.0.0.1 and asserts the
// response body is non-empty.
//
// v0.2.56: switched from HTTPS to HTTP. Pre-issuance the vhost has no
// cert; v0.2.55 and earlier emitted `listen 443 ssl;` unconditionally,
// which nginx loads (only a warning on -t, no error) but at TLS
// handshake time falls back to the 00-default.conf catch-all because
// no cert is presented → "tls: unrecognized name". Even when v0.2.54
// gave SNI the right name, the 443 block had no cert to use.
//
// v0.2.59: retry on transient EOF / connection-closed responses. The
// operator-reported failure was `probe request: Get "http://a.garuda.sh/":
// EOF`, which traces to nginx's :80 catch-all (`return 444;` in
// internal/webserver/template.go) — when the new vhost's server_name
// hasn't yet been picked up by the running workers, the request
// matches the default_server and gets dropped without a response. The
// reload-to-worker-swap window is sub-second; retrying with backoff
// past it closes the false-positive class.
//
// HTTP-only probe matches reality: pre-issuance the only listener
// that actually serves traffic is :80. The lego goroutine issues the
// cert in the background; after issuance, RunReapply re-renders the
// vhost with the 443 block correctly wired (via the new {{ssl_listen}}
// placeholder added in this release).
//
// The dialer still bypasses DNS by forcing TCP to 127.0.0.1 so the
// probe works regardless of whether the operator's DNS A record
// resolves to this VM yet.
func SmokeProbe(domain string) error {
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// addr is "<domain>:80" — rewrite TCP target to loopback,
			// keeping the port the URL implied.
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				port = "80"
			}
			return dialer.DialContext(ctx, network, "127.0.0.1:"+port)
		},
	}
	client := &http.Client{
		Timeout:   SmokeProbeTimeout,
		Transport: transport,
		// Don't follow redirects — a 301 → https://www.<domain> would
		// fail TLS dial outside loopback. We only want the first response.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// v0.2.59: up to 5 attempts, each ~200ms apart. Total wait ≤ 1s
	// before we declare the probe genuinely failed. Most reload races
	// resolve on the 2nd or 3rd try. Backend warm-up (PHP-FPM ondemand
	// spinning a worker, Node systemd unit binding its port) also
	// benefits from the retry budget — these can take 100–400 ms even
	// on a healthy host.
	const maxAttempts = 5
	const backoff = 200 * time.Millisecond
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequest("GET", "http://"+domain+"/", nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", "auracp-smoke-probe/0.2.60")
		resp, err := client.Do(req)
		if err != nil {
			// Retryable transients: EOF (catch-all 444 mid-reload),
			// connection reset, connection refused (backend coming up).
			msg := err.Error()
			if strings.Contains(msg, "EOF") || strings.Contains(msg, "connection reset") || strings.Contains(msg, "connection refused") {
				lastErr = fmt.Errorf("probe request: %w", err)
				time.Sleep(backoff)
				continue
			}
			return fmt.Errorf("probe request: %w", err)
		}
		// A 3xx redirect (e.g. force-HTTPS, WordPress → /wp-admin/install.php)
		// counts as success — the upstream is responding with intent.
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return nil
		}
		// 502 / 503 / 504 — upstream not ready yet (FPM ondemand worker
		// still spinning, Node systemd unit still binding). Treat as
		// retryable for the first few attempts.
		if resp.StatusCode >= 502 && resp.StatusCode <= 504 && attempt < maxAttempts {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d (upstream not ready)", resp.StatusCode)
			time.Sleep(backoff)
			continue
		}
		// Read the first 512 bytes — enough to detect "empty body"
		// without spending bandwidth on a heavy homepage.
		buf := make([]byte, 512)
		n, _ := resp.Body.Read(buf)
		resp.Body.Close()
		if n == 0 {
			return fmt.Errorf("HTTP %d returned an empty body (vhost↔pool user mismatch is the most likely cause; check /etc/nginx/sites-enabled/%s.conf vs /etc/php/*/fpm/pool.d/%s.conf)", resp.StatusCode, domain, domain)
		}
		return nil
	}
	return lastErr
}

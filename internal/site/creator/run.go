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
	"crypto/tls"
	"fmt"
	"net/http"
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
	switch spec.Type {
	case "php", "wordpress":
		c := NewPhp(spec, deps.R, deps.Php)
		if err := c.Run(ctx); err != nil {
			return err
		}
	case "nodejs":
		c := NewNodejs(spec, deps.R, deps.Rt, deps.Node, deps.Store)
		if err := c.Run(ctx); err != nil {
			return err
		}
	case "python":
		c := NewPython(spec, deps.R, deps.Rt, deps.Store)
		if err := c.Run(ctx); err != nil {
			return err
		}
	case "static":
		c := NewStatic(spec, deps.R)
		if err := c.Run(ctx); err != nil {
			return err
		}
	case "reverseproxy":
		c := NewReverseProxy(spec, deps.R)
		if err := c.Run(ctx); err != nil {
			return err
		}
	default:
		return fmt.Errorf("creator.RunCreate: unknown site type %q", spec.Type)
	}
	if err := SmokeProbe(spec.Domain); err != nil {
		return fmt.Errorf("smoke probe failed: %w — vhost+pool written, but the site doesn't respond. See journalctl -u auracpd | grep %s", err, spec.Domain)
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

// SmokeProbe curls https://<domain>/ against 127.0.0.1 with TLS verify
// disabled (so self-signed certs in the pre-issuance window don't
// throw) and asserts the response body is non-empty.
//
// Bypasses DNS by setting Host header to the domain — the request
// targets the loopback address but nginx routes by Host header. Same
// pattern operators use when debugging: `curl --resolve <domain>:443:127.0.0.1`.
func SmokeProbe(domain string) error {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		// Force every request to 127.0.0.1 regardless of the URL's host.
		DialTLS: nil,
	}
	client := &http.Client{
		Timeout:   SmokeProbeTimeout,
		Transport: transport,
	}
	req, err := http.NewRequest("GET", "https://127.0.0.1/", nil)
	if err != nil {
		return err
	}
	req.Host = domain
	req.Header.Set("User-Agent", "auracp-smoke-probe/0.2.48")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("probe request: %w", err)
	}
	defer resp.Body.Close()
	// Read the first 512 bytes — that's enough to detect "empty body"
	// (the a.garuda.sh bug) without spending bandwidth on a heavy
	// homepage.
	buf := make([]byte, 512)
	n, _ := resp.Body.Read(buf)
	if n == 0 {
		return fmt.Errorf("HTTP %d returned an empty body (vhost↔pool user mismatch is the most likely cause; check /etc/nginx/sites-enabled/%s.conf vs /etc/php/*/fpm/pool.d/%s.conf)", resp.StatusCode, domain, domain)
	}
	return nil
}

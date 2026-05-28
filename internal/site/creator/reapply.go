// RunReapply re-renders a site's nginx vhost from current store state
// and reloads nginx. Used for every post-create lifecycle event:
//
//   - Settings save (cache toggle, basic_auth, block_bots, vhost_override)
//   - Cert renewal post-issuance (vhost gains the ssl_certificate paths)
//   - PHP version switch (after phpruntime.WritePool rewrites the pool)
//   - Manual vhost re-render via the panel's Vhost tab
//
// This was the missing piece of the v0.2.48 migration. Pre-v0.2.52 the
// new pipeline only handled CREATE; every other lifecycle event fell
// back to webserver.Manager.Apply which used the legacy text/template
// renderer. v0.2.52 funnels every event through the same Template +
// Processor chain, making the drift-impossibility guarantee complete
// — not just at create time.
//
// What it does NOT do:
//   - Create the Linux user (already exists)
//   - Create directories (already exist)
//   - Touch the FPM pool or systemd unit (those are reapplied separately
//     by their own managers, e.g. phpruntime.WritePool on version switch)
//   - Re-issue certificates (acme.Manager owns that)
//
// Override behaviour: if cfg["vhost_override"] is non-empty AND the
// operator has indicated they want it as a verbatim replacement (a
// separate cfg key, not just the freeform settings textarea), we write
// it directly. Same semantics the legacy renderer's Override field had.
package creator

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/auracp/auracp/internal/paths"
	"github.com/auracp/auracp/internal/runtime"
	"github.com/auracp/auracp/internal/store"
)

// RunReapply reads the site's current store state, writes the htpasswd
// file (if basic_auth is on), re-renders the vhost via the same per-
// type Template + Processor chain RunCreate uses, atomically swaps it
// in via the existing stage→nginx -t→swap pattern in CreateNginxVhost,
// and reloads nginx.
func RunReapply(ctx context.Context, domain string, deps *Deps) error {
	if deps.Store == nil {
		return fmt.Errorf("creator.RunReapply: Store dep is nil")
	}
	st, err := deps.Store.SiteByDomain(domain)
	if err != nil {
		return fmt.Errorf("creator.RunReapply: site %q not found: %w", domain, err)
	}
	cfg, err := deps.Store.SiteConfig(domain)
	if err != nil {
		cfg = map[string]string{} // best-effort — empty cfg is a valid state
	}

	// Manage the htpasswd file. nginx's auth_basic_user_file directive
	// (emitted by processor.BasicAuth when ctx.BasicAuthUser != "")
	// references this file; we maintain it here so creation /
	// deletion / cred change all flow through one place.
	if cfg["basic_auth"] == "true" && cfg["basic_auth_user"] != "" && cfg["basic_auth_hash"] != "" {
		if err := writeHtpasswd(domain, cfg["basic_auth_user"], cfg["basic_auth_hash"]); err != nil {
			return fmt.Errorf("creator.RunReapply: write htpasswd: %w", err)
		}
	} else {
		// Off / missing creds — remove any stale htpasswd so a future
		// basic_auth re-enable starts from a clean state.
		_ = os.Remove(paths.HTPasswdFile(domain))
	}

	// Compose the Spec from store state. Note we do NOT copy
	// PHP-version-switch fields here — those are owned by
	// phpruntime.WritePool which the caller runs separately before
	// invoking RunReapply.
	spec := &Spec{
		Type:          st.Type,
		Domain:        st.Domain,
		User:          st.SiteUser,
		PHPVersion:    st.PHPVersion,
		Settings:      cfg["vhost_override"], // freeform textarea contents
		Cache:         cfg["cache"] == "true",
		CacheTTL:      cfg["cache_ttl"],
		BlockBots:     cfg["block_bots"] == "true",
		BasicAuthUser: cfg["basic_auth_user"],
		BasicAuthHash: cfg["basic_auth_hash"], // not used by the renderer; here for future audit
	}
	// Per-type extras: Node/Python need the backend port; reverseproxy
	// needs the upstream URL.
	switch st.Type {
	case "nodejs", "python":
		spec.AppPort = st.Port
	case "reverseproxy":
		spec.Upstream = st.Upstream
	}

	// Re-use the base Creator's CreateNginxVhost + ReloadNginx — same
	// atomic stage→test→swap as RunCreate uses, so the rollback
	// semantics on `nginx -t` failure are identical.
	c := New(spec, deps.R)
	t0 := time.Now()
	if err := c.CreateNginxVhost(ctx); err != nil {
		return fmt.Errorf("creator.RunReapply: render vhost: %w", err)
	}
	if err := c.ReloadNginx(ctx); err != nil {
		return fmt.Errorf("creator.RunReapply: reload nginx: %w", err)
	}
	c.Log.Info("RunReapply ok", "took_ms", time.Since(t0).Milliseconds())
	return nil
}

// writeHtpasswd writes a single-line htpasswd file at the canonical
// path. nginx supports the bcrypt $2y$ format that internal/auth.HashPassword
// produces, so we write user:hash directly.
func writeHtpasswd(domain, user, hash string) error {
	if err := os.MkdirAll(paths.HTPasswdDir, 0o755); err != nil {
		return err
	}
	line := user + ":" + hash + "\n"
	return os.WriteFile(paths.HTPasswdFile(domain), []byte(line), 0o644)
}

// RunReapplyRuntime re-deploys the BACKEND for a site (FPM pool for
// PHP, systemd unit for Node/Python). Used when an operator changes
// PHP version, per-site PHP settings, Node version, or PM2 toggle.
// No-op for static / reverseproxy (no backend).
//
// v0.2.52: replaces legacy site.Manager.ReapplyRuntime. Same shape;
// reads current state from the store and pushes it to the runtime
// managers. Doesn't touch the nginx vhost (the caller should follow up
// with RunReapply when both backend and vhost need re-rendering — e.g.
// PHP version switch needs new pool config but vhost stays unchanged
// because the FPM socket path is version-independent in our design).
func RunReapplyRuntime(ctx context.Context, domain string, deps *Deps) error {
	if deps.Store == nil {
		return fmt.Errorf("creator.RunReapplyRuntime: Store dep is nil")
	}
	st, err := deps.Store.SiteByDomain(domain)
	if err != nil {
		return err
	}
	switch st.Type {
	case "php", "wordpress":
		if deps.Php == nil {
			return fmt.Errorf("PHP-FPM runtime manager not configured")
		}
		return deps.Php.WritePool(ctx, st.PHPVersion, st.Domain, st.SiteUser)
	case "nodejs":
		if st.PM2Enabled && deps.Node != nil {
			if err := deps.Node.EnsurePM2(ctx, st.NodeVersion.String); err != nil {
				return err
			}
		}
		if deps.Rt == nil {
			return fmt.Errorf("runtime manager not configured")
		}
		return deps.Rt.Apply(ctx, runtimeSpec(st))
	case "python":
		if deps.Rt == nil {
			return fmt.Errorf("runtime manager not configured")
		}
		return deps.Rt.Apply(ctx, runtimeSpec(st))
	}
	return nil // static / reverseproxy — no backend
}

// runtimeSpec builds the runtime.Spec from a store.Site row. Extracted
// so the dispatch in RunReapplyRuntime stays readable.
func runtimeSpec(st store.Site) runtime.Spec {
	return runtime.Spec{
		Type:      st.Type,
		Domain:    st.Domain,
		User:      st.SiteUser,
		Port:      st.Port,
		StartFile: "", // existing systemd unit owns its start file; rewriting only changes port/user
		NodeVer:   st.NodeVersion.String,
		UsePM2:    st.PM2Enabled,
	}
}

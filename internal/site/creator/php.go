// PHP/WordPress Creator. Extends the base Creator with the PHP-specific
// steps and an ordered Run() that walks every step in the right order.
package creator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/auracp/auracp/internal/paths"
	"github.com/auracp/auracp/internal/phpruntime"
	"github.com/auracp/auracp/internal/system"
)

type PhpCreator struct {
	*Creator
	Php *phpruntime.Manager
}

func NewPhp(spec *Spec, r *system.Runner, php *phpruntime.Manager) *PhpCreator {
	return &PhpCreator{
		Creator: New(spec, r),
		Php:     php,
	}
}

// CreateIndexPhp drops a "Hello World" index.php into the docroot so
// browsers landing on the site BEFORE the operator uploads anything see
// proof of life rather than 403/empty body. Skipped for WordPress
// (wpinstall.Install populates the docroot end-to-end).
func (c *PhpCreator) CreateIndexPhp() error {
	t := time.Now()
	if c.Spec.Type == "wordpress" && c.Spec.WPInstall {
		// Skip — wp-cli will overwrite everything anyway.
		c.logStep("CreateIndexPhp (skipped, WP install)", t, nil)
		return nil
	}
	root := paths.DocRoot(c.Spec.User, c.Spec.Domain)
	path := filepath.Join(root, "index.php")
	if _, err := os.Stat(path); err == nil {
		// Don't clobber operator content on re-create runs.
		c.logStep("CreateIndexPhp (existing kept)", t, nil)
		return nil
	}
	body := "<?php\n\necho 'auraCP — " + c.Spec.Domain + " ready.';\n"
	err := os.WriteFile(path, []byte(body), 0o644)
	if err == nil {
		// chown so PHP-FPM (running as <user>) can read it.
		_, err = c.R.Run(context.Background(), "chown", c.Spec.User+":"+c.Spec.User, path)
	}
	c.logStep("CreateIndexPhp", t, err)
	return err
}

// CreatePhpFpmPool delegates to the existing phpruntime.WritePool, which
// already does the per-version pool render + the cross-version sweep
// (v0.2.47). The Spec-derived inputs guarantee user+domain+version are
// the SAME source as everything else in the pipeline.
func (c *PhpCreator) CreatePhpFpmPool(ctx context.Context) error {
	t := time.Now()
	if c.Php == nil {
		err := fmt.Errorf("PHP-FPM runtime manager not configured")
		c.logStep("CreatePhpFpmPool", t, err)
		return err
	}
	err := c.Php.WritePool(ctx, c.Spec.PHPVersion, c.Spec.Domain, c.Spec.User)
	c.logStep("CreatePhpFpmPool", t, err)
	return err
}

// ─── ordered Run() — the whole pipeline for a PHP/WordPress site ───
//
// Order matters and is intentional:
//
//   1. CreateUser              ← must precede any /home/<user> write
//   2. CreateRootDirectory     ← docroot under the just-created user
//   3. CreateLogrotateFile     ← independent; could run anywhere after #1
//   4. CreateSslCertFiles      ← self-signed seed; lego upgrades later
//   5. CreateIndexPhp          ← needs docroot from #2
//   6. CreatePhpFpmPool        ← writes pool config + reloads its php-fpm
//   7. CreateNginxVhost        ← uses ctx.FPMSocket which must already
//                                 have a pool listening (else the first
//                                 request lands a 502 even with valid
//                                 vhost). #6 before #7.
//   8. ReloadNginx             ← ONE reload, after the vhost is on disk
//   9. ResetPermissions        ← chown -R after every dir is in place
//  10. (Smoke probe runs in RunCreate, not here, so this method stays
//      reusable for re-render-only paths.)
//
// Returns the first error. Doesn't roll back — partial state is fine
// because Preflight catches 95% of failures upfront and the remainder
// (filesystem ENOSPC, FPM service crashes) are operator-level concerns
// surfaced via the structured log.
func (c *PhpCreator) Run(ctx context.Context) error {
	steps := []func() error{
		func() error { return c.CreateUser(ctx) },
		func() error { return c.CreateRootDirectory(ctx) },
		func() error { return c.CreateLogrotateFile() },
		func() error { return c.CreateSslCertFiles(ctx) },
		func() error { return c.CreateIndexPhp() },
		func() error { return c.CreatePhpFpmPool(ctx) },
		func() error { return c.CreateNginxVhost(ctx) },
		func() error { return c.ReloadNginx(ctx) },
		func() error { return c.ResetPermissions(ctx) },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

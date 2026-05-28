// Static HTML and Reverse Proxy creators. Simpler than the others —
// no backend service, no port allocator. Static gets an "index.html"
// seed file so a fresh site doesn't 403; ReverseProxy doesn't seed
// anything because its docroot is only used for the ACME challenge.
package creator

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/auracp/auracp/internal/paths"
	"github.com/auracp/auracp/internal/system"
)

// ─── Static ───

type StaticCreator struct {
	*Creator
}

func NewStatic(spec *Spec, r *system.Runner) *StaticCreator {
	return &StaticCreator{Creator: New(spec, r)}
}

func (c *StaticCreator) CreateIndexHtml() error {
	t := time.Now()
	root := paths.DocRoot(c.Spec.User, c.Spec.Domain)
	path := filepath.Join(root, "index.html")
	if _, err := os.Stat(path); err == nil {
		c.logStep("CreateIndexHtml (existing kept)", t, nil)
		return nil
	}
	body := "<!doctype html>\n<html><head><title>" + c.Spec.Domain + "</title></head>\n" +
		"<body><h1>" + c.Spec.Domain + "</h1>\n" +
		"<p>auraCP static site — ready. Upload your files via SFTP or the File Manager.</p>\n" +
		"</body></html>\n"
	err := os.WriteFile(path, []byte(body), 0o644)
	if err == nil {
		_, err = c.R.Run(context.Background(), "chown", c.Spec.User+":"+c.Spec.User, path)
	}
	c.logStep("CreateIndexHtml", t, err)
	return err
}

func (c *StaticCreator) Run(ctx context.Context) error {
	steps := []func() error{
		func() error { return c.CreateUser(ctx) },
		func() error { return c.CreateRootDirectory(ctx) },
		func() error { return c.CreateLogrotateFile() },
		func() error { return c.CreateSslCertFiles(ctx) },
		func() error { return c.CreateIndexHtml() },
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

// ─── ReverseProxy ───

type ReverseProxyCreator struct {
	*Creator
}

func NewReverseProxy(spec *Spec, r *system.Runner) *ReverseProxyCreator {
	return &ReverseProxyCreator{Creator: New(spec, r)}
}

func (c *ReverseProxyCreator) Run(ctx context.Context) error {
	// ReverseProxy doesn't need a docroot per se, but useradd's skel
	// gives us one for free and we re-use it for the ACME challenge
	// dir. No index seed — there's nothing to serve directly.
	steps := []func() error{
		func() error { return c.CreateUser(ctx) },
		func() error { return c.CreateRootDirectory(ctx) },
		func() error { return c.CreateLogrotateFile() },
		func() error { return c.CreateSslCertFiles(ctx) },
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

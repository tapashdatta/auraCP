// Python Creator. Identical shape to NodejsCreator — the systemd unit
// is the backend (gunicorn / uvicorn). The only difference is the
// runtime.Spec.Module field carries the wsgi/asgi target instead of
// StartFile.
package creator

import (
	"context"
	"fmt"
	"time"

	"github.com/auracp/auracp/internal/runtime"
	"github.com/auracp/auracp/internal/store"
	"github.com/auracp/auracp/internal/system"
)

type PythonCreator struct {
	*Creator
	Rt    *runtime.Manager
	Store *store.Store
}

func NewPython(spec *Spec, r *system.Runner, rt *runtime.Manager, st *store.Store) *PythonCreator {
	return &PythonCreator{
		Creator: New(spec, r),
		Rt:      rt,
		Store:   st,
	}
}

func (c *PythonCreator) AllocatePort() error {
	t := time.Now()
	if c.Spec.AppPort != 0 {
		c.logStep("AllocatePort (provided)", t, nil)
		return nil
	}
	p, err := c.Store.NextPort()
	if err == nil {
		c.Spec.AppPort = p
	}
	c.logStep("AllocatePort", t, err)
	return err
}

func (c *PythonCreator) CreateSystemdUnit(ctx context.Context) error {
	t := time.Now()
	if c.Rt == nil {
		err := fmt.Errorf("runtime manager not configured")
		c.logStep("CreateSystemdUnit", t, err)
		return err
	}
	err := c.Rt.Apply(ctx, runtime.Spec{
		Type:   c.Spec.Type,
		Domain: c.Spec.Domain,
		User:   c.Spec.User,
		Port:   c.Spec.AppPort,
		Module: c.Spec.Module,
	})
	c.logStep("CreateSystemdUnit", t, err)
	return err
}

func (c *PythonCreator) Run(ctx context.Context) error {
	steps := []func() error{
		func() error { return c.CreateUser(ctx) },
		func() error { return c.CreateRootDirectory(ctx) },
		func() error { return c.CreateLogrotateFile() },
		func() error { return c.CreateSslCertFiles(ctx) },
		func() error { return c.AllocatePort() },
		func() error { return c.CreateSystemdUnit(ctx) },
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

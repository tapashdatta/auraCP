// Node.js Creator. The systemd unit is the backend (no FPM equivalent).
// Port allocation comes from store.NextPort — the existing allocator we
// keep using for v0.2.48 (filesystem-derived allocator is Refactor #4
// pending; not blocking the bug fix).
package creator

import (
	"context"
	"fmt"
	"time"

	"github.com/auracp/auracp/internal/noderuntime"
	"github.com/auracp/auracp/internal/runtime"
	"github.com/auracp/auracp/internal/system"
	"github.com/auracp/auracp/internal/store"
)

type NodejsCreator struct {
	*Creator
	Rt    *runtime.Manager
	Node  *noderuntime.Manager
	Store *store.Store // for port allocation
}

func NewNodejs(spec *Spec, r *system.Runner, rt *runtime.Manager, node *noderuntime.Manager, st *store.Store) *NodejsCreator {
	return &NodejsCreator{
		Creator: New(spec, r),
		Rt:      rt,
		Node:    node,
		Store:   st,
	}
}

// AllocatePort picks a loopback port for this site's backend. Persists
// onto the Spec so subsequent steps (vhost render via processor.AppPort)
// see the same value.
func (c *NodejsCreator) AllocatePort() error {
	t := time.Now()
	if c.Spec.AppPort != 0 {
		// Operator pre-supplied — trust them.
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

// CreateSystemdUnit delegates to runtime.Manager.Apply, which already
// writes /etc/systemd/system/<domain>.service + enables + starts it.
func (c *NodejsCreator) CreateSystemdUnit(ctx context.Context) error {
	t := time.Now()
	if c.Rt == nil {
		err := fmt.Errorf("runtime manager not configured")
		c.logStep("CreateSystemdUnit", t, err)
		return err
	}
	// PM2 wrapper bootstrap (if requested) — same as legacy path.
	if c.Spec.UsePM2 && c.Node != nil {
		if err := c.Node.EnsurePM2(ctx, c.Spec.NodeVersion); err != nil {
			c.logStep("CreateSystemdUnit (pm2 bootstrap)", t, err)
			return err
		}
	}
	err := c.Rt.Apply(ctx, runtime.Spec{
		Type:      c.Spec.Type,
		Domain:    c.Spec.Domain,
		User:      c.Spec.User,
		Port:      c.Spec.AppPort,
		StartFile: c.Spec.StartFile,
		NodeVer:   c.Spec.NodeVersion,
		UsePM2:    c.Spec.UsePM2,
	})
	c.logStep("CreateSystemdUnit", t, err)
	return err
}

// Run — same ordering rule as PhpCreator. AllocatePort runs BEFORE
// CreateNginxVhost so the vhost render sees the chosen port.
func (c *NodejsCreator) Run(ctx context.Context) error {
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

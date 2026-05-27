// Package system runs privileged OS commands safely.
//
// Security: commands are executed via exec.Command with explicit argument
// arrays — NEVER by interpolating untrusted input into a shell string. File
// and app operations on a site's content run as that site's Linux user via
// a dropped-privilege credential, so a compromised handler can't escape a home.
package system

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"
	"time"
)

type Runner struct {
	DryRun bool // when true, log the command instead of executing it
	Audit  bool // when true, log every executed command
}

func New() *Runner { return &Runner{Audit: true} }

// Run executes name with args (no shell) and returns combined stdout.
func (r *Runner) Run(ctx context.Context, name string, args ...string) (string, error) {
	return r.run(ctx, nil, name, args...)
}

// RunAs executes a command as the given system user (privilege drop).
func (r *Runner) RunAs(ctx context.Context, username, name string, args ...string) (string, error) {
	cred, err := credentialFor(username)
	if err != nil {
		return "", err
	}
	return r.run(ctx, cred, name, args...)
}

func (r *Runner) run(ctx context.Context, cred *syscall.Credential, name string, args ...string) (string, error) {
	if r.Audit || r.DryRun {
		prefix := "exec"
		if r.DryRun {
			prefix = "dry-run"
		}
		log.Printf("[%s] %s %v", prefix, name, args)
	}
	if r.DryRun {
		return "", nil
	}
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	if cred != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: cred}
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%s: %w: %s", name, err, errb.String())
	}
	return out.String(), nil
}

func credentialFor(username string) (*syscall.Credential, error) {
	u, err := user.Lookup(username)
	if err != nil {
		return nil, fmt.Errorf("lookup user %q: %w", username, err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return nil, err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return nil, err
	}
	return &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}, nil
}

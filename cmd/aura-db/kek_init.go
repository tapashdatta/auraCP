package main

import (
	"fmt"
	"os"

	"github.com/auracp/auracp/pkg/dbadmin/standalone"
)

// runKEKInit implements `aura-db kek-init` (or `aura-db kek init`).
//
// Generates a fresh 32-byte KEK and writes it atomically to disk with
// mode 0400. Refuses to overwrite an existing file. This is the
// first-run path — kek-rotate requires an EXISTING KEK file to read,
// which was the original OPS-01 bug (operators couldn't bootstrap a
// fresh deployment without manually `openssl rand`-ing the key).
func runKEKInit(g globalFlags, args []string) error {
	fs := newFlagSet("kek-init", os.Stderr)
	output := fs.String("output", "", "path to write the new KEK (default: kek.file from config)")
	help := fs.Bool("help", false, "show help")
	fs.Usage = func() { fmt.Fprint(fs.Output(), helpKEKInit) }
	if err := fs.Parse(args); err != nil {
		return userErr(err.Error())
	}
	if *help {
		fmt.Fprint(os.Stdout, helpKEKInit)
		return nil
	}

	path := *output
	if path == "" {
		// Try config; fall back to the well-known default if config is
		// missing or unreadable (init runs BEFORE serve, so a config
		// pointing at a not-yet-created KEK file is expected).
		if cfg, err := standalone.LoadConfig(g.configPath); err == nil && cfg.KEK.File != "" {
			path = cfg.KEK.File
		} else {
			path = standalone.DefaultKEKPath
		}
	}

	if err := standalone.InitKEKFile(path); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote new KEK to %s (mode 0400)\n", path)
	return nil
}

const helpKEKInit = `aura-db kek-init — create a fresh KEK on disk

Generates a new 32-byte AES key, writes it atomically at mode 0400, and
refuses to overwrite an existing file. Used for first-run bootstrap;
once the file exists, use ` + "`aura-db kek-rotate`" + ` to replace it (which
also re-encrypts every connection credential and MFA secret).

Flags:
  --output <path>   path to write the new KEK (default: kek.file from config, then /etc/aura-db/kek.key)
  --help            show this help
`

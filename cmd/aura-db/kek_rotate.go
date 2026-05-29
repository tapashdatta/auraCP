package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strings"

	"github.com/auracp/auracp/pkg/dbadmin/standalone"
)

// isNoKEKErr returns true when err wraps an os.PathError stemming from
// a missing KEK file, regardless of whether LoadKEK wrapped it (OPS-01).
func isNoKEKErr(err error) bool {
	if err == nil {
		return false
	}
	if os.IsNotExist(err) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "no such file or directory") || strings.Contains(msg, "open KEK file")
}

func runKEKRotate(g globalFlags, args []string) error {
	fs := newFlagSet("kek-rotate", os.Stderr)
	newKeyFile := fs.String("new-key-file", "", "path to write the new KEK (default: from config)")
	generate := fs.Bool("generate", false, "generate a new key (mutually exclusive with --new-key-from)")
	newKeyFrom := fs.String("new-key-from", "", "read new key from path (32 bytes raw)")
	backupOldTo := fs.String("backup-old-to", "", "write the old key to this path (required)")
	force := fs.Bool("force", false, "bypass PID file lock check")
	help := fs.Bool("help", false, "show help")
	fs.Usage = func() { fmt.Fprint(fs.Output(), helpKEKRotate) }
	if err := fs.Parse(args); err != nil {
		return userErr(err.Error())
	}
	if *help {
		fmt.Fprint(os.Stdout, helpKEKRotate)
		return nil
	}
	if !*generate && *newKeyFrom == "" {
		return userErr("--generate or --new-key-from required")
	}
	if *generate && *newKeyFrom != "" {
		return userErr("--generate and --new-key-from are mutually exclusive")
	}
	if *backupOldTo == "" {
		return userErr("--backup-old-to is required (key destruction is irreversible)")
	}

	cfg, err := standalone.LoadConfig(g.configPath)
	if err != nil {
		return err
	}
	if *newKeyFile == "" {
		*newKeyFile = cfg.KEK.File
	}

	// PID-file lock: refuse rotation while serve is running.
	if !*force {
		pidPath := resolvePIDFile()
		if pidPath != "" {
			if pid, err := readPIDFile(pidPath); err == nil && pidAlive(pid) {
				return &errKEKRotateRefused{msg: fmt.Sprintf("aura-db serve is running (pid %d); pass --force only if you are sure", pid)}
			}
		}
	}

	oldKEK, err := standalone.LoadKEK(cfg.KEK.File)
	if err != nil {
		// OPS-01: first-run users hitting `kek-rotate --generate` with no
		// existing KEK file used to get a cryptic open() error; redirect
		// them to `kek-init` which is the documented bootstrap path.
		if os.IsNotExist(err) || isNoKEKErr(err) {
			return userErr(fmt.Sprintf(
				"no existing KEK file at %q — use `aura-db kek-init` to create the first key (kek-rotate replaces an EXISTING key while re-encrypting every credential)",
				cfg.KEK.File))
		}
		return err
	}

	var newRaw [32]byte
	if *generate {
		if _, err := rand.Read(newRaw[:]); err != nil {
			return err
		}
	} else {
		b, err := os.ReadFile(*newKeyFrom)
		if err != nil {
			return err
		}
		if len(b) != 32 {
			return userErr(fmt.Sprintf("--new-key-from must point to a 32-byte file; got %d bytes", len(b)))
		}
		copy(newRaw[:], b)
	}

	// Back up the old key first; abort if backup fails.
	oldBytes := *oldKEK.Bytes()
	if err := standalone.WriteKEKFile(*backupOldTo, oldBytes); err != nil {
		return err
	}

	ctx := context.Background()
	store, err := standalone.OpenStore(ctx, cfg.Storage.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()

	// RotateKEK now performs the atomic key-file swap itself (SEC-08).
	connsN, mfaN, err := standalone.RotateKEK(ctx, store, oldKEK.Bytes(), &newRaw, *newKeyFile)
	if err != nil {
		// Re-encryption failed mid-flight; surface the error so the
		// caller can consult the runbook. (If the DB committed but the
		// key file write failed, RotateKEK reports both facts in the
		// error message.)
		return err
	}
	// Zero the old key bytes in memory after a successful swap.
	for i := range oldBytes {
		oldBytes[i] = 0
	}
	oldKEK.Zero()

	fmt.Fprintf(os.Stderr, "rotated KEK; re-encrypted %d connections and %d MFA secrets\n", connsN, mfaN)
	return nil
}

package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// defaultPIDFile is the standalone server's PID file location. Falls
// back to $XDG_RUNTIME_DIR/aura-db.pid if /var/run is not writable.
const defaultPIDFile = "/var/run/aura-db.pid"

// resolvePIDFile picks a writable PID path.
func resolvePIDFile() string {
	if testWritable("/var/run") {
		return defaultPIDFile
	}
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return xdg + "/aura-db.pid"
	}
	if home, _ := os.UserHomeDir(); home != "" {
		return home + "/.aura-db.pid"
	}
	return ""
}

func testWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".aura-db-pidprobe-*")
	if err != nil {
		return false
	}
	_ = os.Remove(f.Name())
	_ = f.Close()
	return true
}

// writePIDFile creates path containing the current PID. Refuses to
// overwrite an existing file whose PID is alive.
func writePIDFile(path string) error {
	if path == "" {
		return nil
	}
	if existing, err := readExistingPID(path); err == nil && pidAlive(existing) {
		return fmt.Errorf("aura-db: PID file %q exists and pid %d is alive; refuse to start", path, existing)
	}
	pid := os.Getpid()
	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o644)
}

// readPIDFile returns the PID stored in path or an error.
func readPIDFile(path string) (int, error) {
	return readExistingPID(path)
}

func readExistingPID(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, err
	}
	return pid, nil
}

// pidAlive returns true when a process with pid responds to signal 0.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := p.Signal(syscall.Signal(0)); err != nil {
		// EPERM means process exists but we lack permission.
		return errors.Is(err, syscall.EPERM)
	}
	return true
}

// removePIDFile is best-effort.
func removePIDFile(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
}

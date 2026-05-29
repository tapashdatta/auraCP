package standalone

import (
	"context"
	"path/filepath"
	"testing"
)

func TestBootstrap_WiresEngine(t *testing.T) {
	dir := t.TempDir()
	// Write a KEK file at the expected mode (0400).
	kekPath := filepath.Join(dir, "kek.key")
	if _, err := LoadOrGenerateKEK(kekPath); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Storage.DBPath = filepath.Join(dir, "aura.db")
	cfg.Storage.AuditLogPath = filepath.Join(dir, "audit.log")
	cfg.Storage.HistoryDBPath = filepath.Join(dir, "history.db")
	cfg.KEK.File = kekPath
	cfg.Logging.Destination = "stderr"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	app, err := Bootstrap(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer app.Close()
	if app.Engine == nil {
		t.Fatal("nil engine")
	}
	if app.Engine.Handler() == nil {
		t.Fatal("nil handler")
	}
}

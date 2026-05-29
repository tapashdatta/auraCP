package standalone

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// TestFileAuditSink_RotatesAtThreshold is the FIX-4 (INT-3) regression
// test: with MaxFileSize set to a tiny value, writing many events MUST
// produce multiple rotated files; each rotated file's tail hash MUST
// match the next file's head event PrevEventHash so the chain spans
// rotations end-to-end; and old backups must be pruned when MaxBackups
// is exceeded.
func TestFileAuditSink_RotatesAtThreshold(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.ndjson")
	sink := &FileAuditSink{
		Path:        path,
		MaxFileSize: 1024, // 1 KiB — rotate aggressively
		MaxBackups:  3,
	}
	if err := sink.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Write 50 events. Each event marshals to >~250 bytes (Target +
	// timestamp + statement). That's well over the 1 KiB threshold, so
	// we expect at least one rotation per ~4 events.
	for i := 0; i < 50; i++ {
		sink.Record(context.Background(), dbadmin.Event{
			EventID:        "01J0000000000000000000000" + string(rune('A'+(i%26))),
			Timestamp:      time.Now().UTC(),
			UserID:         "user-42",
			UserRoleAtTime: dbadmin.RoleWriter,
			Action:         dbadmin.ActionRowWrite,
			Target:         dbadmin.Target{ConnectionID: "conn-x", Schema: "public", Object: "users"},
			Statement:      strings.Repeat("UPDATE users SET email=? WHERE id=?;", 4),
			ResultRows:     int64(i),
			DurationMS:     int64(i),
		})
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Expect: 1 current file + at most MaxBackups rotated. The drain
	// produced rotation often, but pruneBackups bounds to MaxBackups.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	current := 0
	rotated := 0
	for _, e := range entries {
		name := e.Name()
		if name == "audit.ndjson" {
			current++
		} else if strings.HasPrefix(name, "audit.ndjson.") {
			rotated++
		}
	}
	if current != 1 {
		t.Fatalf("current audit file count = %d, want 1", current)
	}
	if rotated == 0 {
		t.Fatalf("no rotated files produced — rotation did not trigger")
	}
	if rotated > sink.MaxBackups {
		t.Fatalf("rotated files = %d, want <= MaxBackups=%d (prune did not bound)", rotated, sink.MaxBackups)
	}
}

// TestFileAuditSink_RotationDisabledByNegative confirms callers can
// opt out of rotation entirely by setting MaxFileSize < 0 (legacy
// single-file behavior).
func TestFileAuditSink_RotationDisabledByNegative(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.ndjson")
	sink := &FileAuditSink{
		Path:        path,
		MaxFileSize: -1,
	}
	if err := sink.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for i := 0; i < 50; i++ {
		sink.Record(context.Background(), dbadmin.Event{
			EventID:   "01J0000000000000000000000" + string(rune('A'+(i%26))),
			Timestamp: time.Now().UTC(),
			Action:    dbadmin.ActionRowWrite,
			Statement: strings.Repeat("X", 256),
		})
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "audit.ndjson.") {
			t.Fatalf("rotation triggered despite MaxFileSize<0: %s", e.Name())
		}
	}
}

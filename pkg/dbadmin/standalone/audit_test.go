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

func TestFileAuditSink_RoundTripChain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	sink := &FileAuditSink{Path: path, QueueSize: 16}
	if err := sink.Start(); err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	for i := 0; i < 5; i++ {
		sink.Record(context.Background(), dbadmin.Event{
			EventID:        NewULID(),
			Timestamp:      time.Now().UTC(),
			UserID:         "alice",
			UserRoleAtTime: dbadmin.RoleOwner,
			Action:         dbadmin.ActionConnList,
		})
	}
	// Allow drain to flush.
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	res, err := VerifyAuditLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("chain broken: %#v", res)
	}
	if res.EventsScanned != 5 {
		t.Fatalf("expected 5 events; got %d", res.EventsScanned)
	}
}

func TestFileAuditSink_DetectsTamper(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	sink := &FileAuditSink{Path: path, QueueSize: 16}
	if err := sink.Start(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		sink.Record(context.Background(), dbadmin.Event{
			EventID:   NewULID(),
			Timestamp: time.Now().UTC(),
			UserID:    "alice",
			Action:    dbadmin.ActionConnList,
		})
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}

	// Read all lines and corrupt one in the middle.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines; got %d", len(lines))
	}
	// Tamper with line 2's user_id; the next line's PrevEventHash will
	// no longer match.
	lines[1] = strings.Replace(lines[1], `"user_id":"alice"`, `"user_id":"mallory"`, 1)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	res, err := VerifyAuditLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatal("expected chain break after tamper")
	}
	if res.BreakLine == 0 {
		t.Fatal("expected non-zero BreakLine")
	}
}

func TestFileAuditSink_PersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	sink := &FileAuditSink{Path: path, QueueSize: 16}
	if err := sink.Start(); err != nil {
		t.Fatal(err)
	}
	sink.Record(context.Background(), dbadmin.Event{EventID: NewULID(), Timestamp: time.Now().UTC(), UserID: "u"})
	sink.Close()

	sink2 := &FileAuditSink{Path: path, QueueSize: 16}
	if err := sink2.Start(); err != nil {
		t.Fatal(err)
	}
	sink2.Record(context.Background(), dbadmin.Event{EventID: NewULID(), Timestamp: time.Now().UTC(), UserID: "u"})
	sink2.Close()

	res, err := VerifyAuditLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("expected continuous chain across reopens: %#v", res)
	}
	if res.EventsScanned != 2 {
		t.Fatalf("expected 2 events; got %d", res.EventsScanned)
	}
}

func TestFileAuditSink_RefusesBroadMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	sink := &FileAuditSink{Path: path}
	if err := sink.Start(); err == nil {
		t.Fatal("expected refusal for mode 0644 audit file")
	}
}

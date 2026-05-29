package standalone

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

func TestVerifyAuditLog_EmptyFileIsOK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	if err := os.WriteFile(path, []byte{}, 0o640); err != nil {
		t.Fatal(err)
	}
	res, err := VerifyAuditLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("empty file should verify OK; %#v", res)
	}
}

func TestVerifyAuditLogFrom_ScansEventsAfterOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	sink := &FileAuditSink{Path: path, QueueSize: 16}
	if err := sink.Start(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		sink.Record(context.Background(), dbadmin.Event{
			EventID: NewULID(), Timestamp: time.Now().UTC(),
			UserID: "u", Action: dbadmin.ActionConnList,
		})
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	// Starting from offset means the running prev hash starts at
	// GENESIS not at the actual prior hash, so verify must report a
	// break (this proves the --from semantics: resumption only works
	// when callers wire in the expected prev hash, which they don't in
	// PR #9 — the flag is informational).
	res, err := VerifyAuditLogFrom(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatal("expected a chain break when starting mid-file without prior hash")
	}
}

func TestAuditReader_IteratesEvents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	sink := &FileAuditSink{Path: path, QueueSize: 16}
	if err := sink.Start(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		sink.Record(context.Background(), dbadmin.Event{
			EventID: NewULID(), Timestamp: time.Now().UTC(),
			UserID: "u", Action: dbadmin.ActionConnList,
		})
	}
	sink.Close()
	r, err := OpenAuditReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	count := 0
	for {
		ev, err := r.NextEvent()
		if ev == nil {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count != 3 {
		t.Fatalf("expected 3 events; got %d", count)
	}
}

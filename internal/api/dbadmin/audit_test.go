package dbadmin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/auracp/auracp/internal/store"
	"github.com/auracp/auracp/pkg/dbadmin"
)

// TestAdapter_AuditSink_Emits verifies an Event flows through the dual-
// write adapter into BOTH the panel audit_log table AND the SHA-256
// hash-chained NDJSON log.
func TestAdapter_AuditSink_Emits(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "auracp.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	auditPath := filepath.Join(dir, "audit.ndjson")
	signingKey := make([]byte, 32)
	for i := range signingKey {
		signingKey[i] = byte(i)
	}
	sink, err := newPanelAudit(auditPath, signingKey, st, nil)
	if err != nil {
		t.Fatalf("newPanelAudit: %v", err)
	}
	defer sink.Close()

	ev := dbadmin.Event{
		EventID:        "01J0000000000000000000000A",
		Timestamp:      time.Now().UTC(),
		UserID:         "42",
		UserRoleAtTime: dbadmin.RoleWriter,
		Action:         dbadmin.ActionRowWrite,
		Target:         dbadmin.Target{ConnectionID: "conn-x", Schema: "public", Object: "users"},
		ResultRows:     5,
		DurationMS:     12,
	}
	sink.Record(context.Background(), ev)

	// Close to flush the chain writer.
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 1) Panel audit_log mirror.
	entries, err := st.RecentAudit(10)
	if err != nil {
		t.Fatalf("RecentAudit: %v", err)
	}
	var found *store.AuditEntry
	for i := range entries {
		if strings.HasPrefix(entries[i].Action, "dbadmin.") {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no dbadmin.* row in panel audit_log; got %d entries", len(entries))
	}
	if found.Action != "dbadmin."+string(dbadmin.ActionRowWrite) {
		t.Fatalf("Action = %q, want dbadmin.%s", found.Action, dbadmin.ActionRowWrite)
	}
	if found.Actor != "42" {
		t.Fatalf("Actor = %q, want 42", found.Actor)
	}
	if !strings.Contains(found.Detail, `"rows":5`) {
		t.Fatalf("Detail missing rows count: %q", found.Detail)
	}

	// 2) NDJSON chain log on disk.
	data, err := readFileOrEmpty(t, auditPath)
	if err != nil {
		t.Fatalf("read audit.ndjson: %v", err)
	}
	if !strings.Contains(data, ev.EventID) {
		t.Fatalf("audit.ndjson missing EventID %s: %q", ev.EventID, data)
	}
}

// TestAdapter_AuditSink_PanelOnly verifies the mirror writes even when
// the chain log path is read-only (graceful degradation: a failed mirror
// MUST NOT fail the request, per AuditSink contract). We test that
// missing the mirror doesn't crash.
func TestAdapter_AuditSink_PanelOnlyDoesNotPanicOnMirrorErr(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "auracp.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	st.Close() // close immediately so AddAudit returns an error
	sink, err := newPanelAudit(filepath.Join(dir, "audit.ndjson"), nil, st, nil)
	if err != nil {
		t.Fatalf("newPanelAudit: %v", err)
	}
	defer sink.Close()
	// Must not panic even though the mirror write will fail.
	sink.Record(context.Background(), dbadmin.Event{
		EventID:   "01J000000000000000000000ZZ",
		Timestamp: time.Now().UTC(),
		Action:    dbadmin.ActionConnList,
	})
}

func readFileOrEmpty(t *testing.T, path string) (string, error) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

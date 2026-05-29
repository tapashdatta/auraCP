package dbadmin

import (
	"testing"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

func TestStepUpStore_SetHasExpire(t *testing.T) {
	s := newStepUpStore()
	defer s.stop()

	const conn = "conn-A"
	if s.hasClass("sess1", dbadmin.ActionClassDDL, conn) {
		t.Fatal("empty store reported step-up")
	}
	s.setClass("sess1", dbadmin.ActionClassDDL, conn, 100*time.Millisecond)
	if !s.hasClass("sess1", dbadmin.ActionClassDDL, conn) {
		t.Fatal("set+has should report true")
	}
	// Different session — not granted.
	if s.hasClass("sess2", dbadmin.ActionClassDDL, conn) {
		t.Fatal("step-up leaked across sessions")
	}
	// Different class — not granted.
	if s.hasClass("sess1", dbadmin.ActionClassDangerous, conn) {
		t.Fatal("step-up leaked across classes")
	}
	// PR #10.5 / FIX-INT-12: different connection id — not granted.
	if s.hasClass("sess1", dbadmin.ActionClassDDL, "conn-B") {
		t.Fatal("step-up leaked across connections")
	}
	// FIX-SDK-3: sibling action in the same class — IS granted.
	siblingClass := dbadmin.Action("query.ddl").Class()
	if siblingClass != dbadmin.ActionClassDDL {
		t.Fatalf("ActionQueryDDL.Class() = %q, want %q", siblingClass, dbadmin.ActionClassDDL)
	}

	time.Sleep(150 * time.Millisecond)
	if s.hasClass("sess1", dbadmin.ActionClassDDL, conn) {
		t.Fatal("expired step-up still reported true")
	}
}

func TestStepUpStore_EmptyKeysIgnored(t *testing.T) {
	s := newStepUpStore()
	defer s.stop()

	s.setClass("", dbadmin.ActionClassDDL, "conn-A", time.Minute)
	s.setClass("sess", dbadmin.ActionClassNone, "conn-A", time.Minute)
	if s.hasClass("", dbadmin.ActionClassDDL, "conn-A") || s.hasClass("sess", dbadmin.ActionClassNone, "conn-A") {
		t.Fatal("empty session or none-class keyed a step-up flag")
	}
}

// TestStepUpStore_InvalidateSession verifies PR #10.5 / FIX-PD-SEC-04:
// logging out the panel session must drop every step-up flag bound to
// that session, not wait for the TTL or the reaper.
func TestStepUpStore_InvalidateSession(t *testing.T) {
	s := newStepUpStore()
	defer s.stop()

	s.setClass("sessA", dbadmin.ActionClassDDL, "c1", time.Hour)
	s.setClass("sessA", dbadmin.ActionClassDangerous, "c2", time.Hour)
	s.setClass("sessB", dbadmin.ActionClassDDL, "c1", time.Hour)

	s.InvalidateSession("sessA")

	if s.hasClass("sessA", dbadmin.ActionClassDDL, "c1") {
		t.Fatal("InvalidateSession left a sessA/DDL flag behind")
	}
	if s.hasClass("sessA", dbadmin.ActionClassDangerous, "c2") {
		t.Fatal("InvalidateSession left a sessA/Dangerous flag behind")
	}
	if !s.hasClass("sessB", dbadmin.ActionClassDDL, "c1") {
		t.Fatal("InvalidateSession bled into sessB")
	}
}

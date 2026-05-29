package dbadmin

import (
	"testing"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

func TestStepUpStore_SetHasExpire(t *testing.T) {
	s := newStepUpStore()
	defer s.stop()

	if s.has("sess1", dbadmin.ActionQueryDDL) {
		t.Fatal("empty store reported step-up")
	}
	s.set("sess1", dbadmin.ActionQueryDDL, 100*time.Millisecond)
	if !s.has("sess1", dbadmin.ActionQueryDDL) {
		t.Fatal("set+has should report true")
	}
	// Different session — not granted.
	if s.has("sess2", dbadmin.ActionQueryDDL) {
		t.Fatal("step-up leaked across sessions")
	}
	// Different action — not granted.
	if s.has("sess1", dbadmin.ActionRowWrite) {
		t.Fatal("step-up leaked across actions")
	}

	time.Sleep(150 * time.Millisecond)
	if s.has("sess1", dbadmin.ActionQueryDDL) {
		t.Fatal("expired step-up still reported true")
	}
}

func TestStepUpStore_EmptyKeysIgnored(t *testing.T) {
	s := newStepUpStore()
	defer s.stop()

	s.set("", dbadmin.ActionQueryDDL, time.Minute)
	s.set("sess", "", time.Minute)
	if s.has("", dbadmin.ActionQueryDDL) || s.has("sess", "") {
		t.Fatal("empty session or action keyed a step-up flag")
	}
}

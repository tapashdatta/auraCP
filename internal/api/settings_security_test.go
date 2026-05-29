package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/auracp/auracp/internal/store"
)

// TestSettings_HidesAuraDBAuditSigningKey is the FIX-1 (PD-SEC-01)
// regression test: even when the legacy audit signing key row is
// present in the settings table, GET /api/settings MUST NOT echo its
// value back to a caller. The handler must replace the value with the
// sentinel {"set":true} shape so the UI can still display "configured"
// without ever exposing the secret.
func TestSettings_HidesAuraDBAuditSigningKey(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "auracp.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	const sentinelValue = "VEVTVF9TRUNSRVRfTk9UX1RPX0xFQUtfQUFBQUFBQUFBQUFBQUE=" // 32 bytes b64
	if err := st.SetSetting("aura_db_audit_signing_key", sentinelValue); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := st.SetSetting("ordinary_key", "ordinary_value"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	s := &Server{store: st}
	r := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	w := httptest.NewRecorder()
	s.getSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, sentinelValue) {
		t.Fatalf("response leaked the signing-key value: %s", body)
	}
	// Ordinary keys must still round-trip.
	if !strings.Contains(body, `"ordinary_key":"ordinary_value"`) {
		t.Fatalf("ordinary keys regressed: %s", body)
	}
	// The presence sentinel must appear.
	if !strings.Contains(body, `"aura_db_audit_signing_key":{"set":true}`) {
		t.Fatalf("expected sentinel object for the signing key, got: %s", body)
	}
}

// TestSettings_RejectsAuraDBAuditSigningKeyWrites is the write-side
// companion to TestSettings_HidesAuraDBAuditSigningKey. The handler
// must refuse PUT /api/settings requests that target any secret-bearing
// key so a confused-deputy attacker can't overwrite the audit key (or
// trigger a key rotation that drops forensic continuity).
func TestSettings_RejectsAuraDBAuditSigningKeyWrites(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "auracp.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	s := &Server{store: st}
	body, _ := json.Marshal(map[string]string{
		"aura_db_audit_signing_key": "TUFMSUNJT1VTX09WRVJXUklURV9BVFRBQ0tfVkFMVUVBQUE=",
	})
	r := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.putSettings(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	// And the store must NOT have a row written.
	if v, ok := st.GetSetting("aura_db_audit_signing_key"); ok && v != "" {
		t.Fatalf("denied write was applied: %q", v)
	}
}

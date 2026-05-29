package dbadmin

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/auracp/auracp/internal/api"
	"github.com/auracp/auracp/internal/secret"
	"github.com/auracp/auracp/internal/store"
)

// TestMount_RoutesUnderPrefix verifies Mount installs the dbadmin engine
// under /api/dbadmin/, that unauthenticated requests are rejected by the
// engine (not the panel mux), and that requests OUTSIDE the prefix still
// reach the panel handlers untouched. The "Adminer coexists" semantic is
// modeled here: Adminer is served by nginx (not auracpd's mux), so the
// only mux concern is that pre-existing panel routes still work.
func TestMount_RoutesUnderPrefix(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "auracp.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	box, err := secret.Open(dir)
	if err != nil {
		t.Fatalf("secret.Open: %v", err)
	}

	mux := http.NewServeMux()
	// Pre-existing panel route ("Adminer co-existence" proxy): the
	// panel's mux serves /api/health and /_adminer/ via nginx (out of
	// process). We model the panel side as a single sentinel handler
	// at /api/health and ensure it still works after Mount runs.
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("panel-health-ok"))
	})

	// Mount the engine.
	cfg := defaultConfig()
	cfg.AuditPath = filepath.Join(dir, "aura-db", "audit.ndjson")
	restore := SetSigningKeyPathForTest(filepath.Join(dir, "aura-db-audit.key"))
	defer restore()
	engine, closer, err := Mount(mux, st, box, func(r *http.Request) (api.IdentitySummary, bool) {
		return api.IdentitySummary{}, false
	}, cfg)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	defer closer.Close()
	if engine == nil {
		t.Fatal("Mount returned nil engine")
	}

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 1) Panel route is unaffected.
	resp, err := http.Get(srv.URL + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/health status = %d, want 200", resp.StatusCode)
	}

	// 2) /api/dbadmin/ is mounted — an unauthenticated request gets a
	// 401 from the engine (not a 404 from the mux). This proves the
	// dbadmin handler is wired in, AND that it owns its own authn gate.
	resp, err = http.Get(srv.URL + "/api/dbadmin/connections")
	if err != nil {
		t.Fatalf("GET /api/dbadmin/connections: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/api/dbadmin/connections status = %d, want 401", resp.StatusCode)
	}
}

// TestMount_NilArgsRejected exercises the defensive guards.
func TestMount_NilArgsRejected(t *testing.T) {
	mux := http.NewServeMux()
	cfg := defaultConfig()
	if _, _, err := Mount(nil, nil, nil, nil, cfg); err == nil {
		t.Fatal("Mount(nil...) returned no error")
	}
	if _, _, err := Mount(mux, nil, nil, nil, cfg); err == nil {
		t.Fatal("Mount(mux, nil store...) returned no error")
	}
}

// TestAdapter_AdminerCoexists is the design-mandated coexistence test.
// In production Adminer is served by nginx at /_adminer/ — not by
// auracpd's mux. The only way our changes could break it is if Mount
// installed a route that swallowed /_adminer/ from the mux. We assert
// that's not the case by verifying a custom /_adminer/ handler on the
// mux survives a Mount call.
func TestAdapter_AdminerCoexists(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "auracp.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	box, err := secret.Open(dir)
	if err != nil {
		t.Fatalf("secret.Open: %v", err)
	}

	mux := http.NewServeMux()
	// Stand-in for the nginx-served Adminer location. The mux NEVER
	// answers /_adminer/ in production, but we wire one here so we can
	// prove Mount did not register a swallowing pattern.
	mux.HandleFunc("/_adminer/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("adminer-passthrough"))
	})

	cfg := defaultConfig()
	cfg.AuditPath = filepath.Join(dir, "aura-db", "audit.ndjson")
	restore := SetSigningKeyPathForTest(filepath.Join(dir, "aura-db-audit.key"))
	defer restore()
	if _, closer, err := Mount(mux, st, box, func(r *http.Request) (api.IdentitySummary, bool) {
		return api.IdentitySummary{}, false
	}, cfg); err != nil {
		t.Fatalf("Mount: %v", err)
	} else {
		defer closer.Close()
	}

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/_adminer/")
	if err != nil {
		t.Fatalf("GET /_adminer/: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/_adminer/ status = %d, want 200 (Adminer route swallowed by Mount)", resp.StatusCode)
	}
}

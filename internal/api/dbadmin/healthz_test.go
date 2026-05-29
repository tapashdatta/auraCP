package dbadmin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/auracp/auracp/internal/api"
	"github.com/auracp/auracp/internal/secret"
	"github.com/auracp/auracp/internal/store"
)

// TestHealthz_AlwaysOK is the OPS-04 (reassigned from PR #9.5) probe
// test: /api/dbadmin/healthz must return 200 for any process that has
// returned from Mount. It does NOT check downstream dependencies — that
// is /readyz's job.
func TestHealthz_AlwaysOK(t *testing.T) {
	srv, closer := mountForProbe(t)
	defer closer.Close()

	resp, err := http.Get(srv.URL + "/api/dbadmin/healthz")
	if err != nil {
		t.Fatalf("GET healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("healthz body = %s, want status:ok", body)
	}
	if resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("healthz Content-Type = %q, want application/json", resp.Header.Get("Content-Type"))
	}
}

// TestReadyz_OKWhenAllUp verifies /readyz returns 200 with the three
// component flags true when engine + audit + store are all healthy.
func TestReadyz_OKWhenAllUp(t *testing.T) {
	srv, closer := mountForProbe(t)
	defer closer.Close()

	resp, err := http.Get(srv.URL + "/api/dbadmin/readyz")
	if err != nil {
		t.Fatalf("GET readyz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz status = %d, want 200", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("readyz body decode: %v", err)
	}
	if out["status"] != "ready" {
		t.Fatalf("readyz status field = %v, want ready", out["status"])
	}
	for _, k := range []string{"engine", "audit", "store"} {
		if out[k] != true {
			t.Fatalf("readyz %s = %v, want true", k, out[k])
		}
	}
}

// TestReadyz_503OnShutdown verifies /readyz refuses readiness once the
// engine has begun shutting down (Engine.IsShuttingDown returns true).
func TestReadyz_503OnShutdown(t *testing.T) {
	bundle := mountForProbeWithEngine(t)
	defer bundle.closer.Close()

	if err := bundle.engine.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	resp, err := http.Get(bundle.srv.URL + "/api/dbadmin/readyz")
	if err != nil {
		t.Fatalf("GET readyz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readyz after shutdown status = %d, want 503", resp.StatusCode)
	}
}

// TestRequestIDHeader_Echoed verifies WithRequestIDMiddleware writes a
// request id into X-Request-Id on every /api/dbadmin/* response.
func TestRequestIDHeader_Echoed(t *testing.T) {
	srv, closer := mountForProbe(t)
	defer closer.Close()

	resp, err := http.Get(srv.URL + "/api/dbadmin/connections")
	if err != nil {
		t.Fatalf("GET connections: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get(RequestIDHeader); got == "" {
		t.Fatalf("missing %s response header", RequestIDHeader)
	}
}

// TestRequestIDHeader_HonorsInbound verifies a client-supplied id is
// echoed back unmodified (lets external tracing systems thread their
// own id through the panel).
func TestRequestIDHeader_HonorsInbound(t *testing.T) {
	srv, closer := mountForProbe(t)
	defer closer.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/dbadmin/connections", nil)
	req.Header.Set(RequestIDHeader, "abc123client")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get(RequestIDHeader); got != "abc123client" {
		t.Fatalf("X-Request-Id = %q, want abc123client", got)
	}
}

// ----- helpers -----

// engineLike is the subset of *dbadmin.Engine the test needs.
type engineLike interface {
	Shutdown(context.Context) error
}

type probeBundle struct {
	srv    *httptest.Server
	closer io.Closer
	engine engineLike
}

func mountForProbe(t *testing.T) (*httptest.Server, io.Closer) {
	t.Helper()
	b := mountForProbeWithEngine(t)
	return b.srv, b.closer
}

func mountForProbeWithEngine(t *testing.T) probeBundle {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "auracp.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	box, err := secret.Open(dir)
	if err != nil {
		t.Fatalf("secret.Open: %v", err)
	}

	restore := SetSigningKeyPathForTest(filepath.Join(dir, "aura-db-audit.key"))
	t.Cleanup(restore)

	cfg := defaultConfig()
	cfg.AuditPath = filepath.Join(dir, "aura-db", "audit.ndjson")

	mux := http.NewServeMux()
	engine, closer, err := Mount(mux, st, box, func(*http.Request) (api.IdentitySummary, bool) {
		return api.IdentitySummary{}, false
	}, cfg)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return probeBundle{srv: srv, closer: closer, engine: engine}
}

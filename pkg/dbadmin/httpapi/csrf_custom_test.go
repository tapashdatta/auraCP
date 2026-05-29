package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCSRF_HonorsCustomCookieName is the FIX-2 (PD-SEC-02_INT-1)
// regression test: when an embedder passes Options.CSRFCookieName and
// Options.CSRFHeaderName, the CSRF middleware must validate against
// those names — NOT the standalone defaults of __Host-aura_csrf and
// X-Aura-Csrf. This is what unblocks the panel SPA, which mints
// auracp_csrf + X-CSRF-Token.
func TestCSRF_HonorsCustomCookieName(t *testing.T) {
	// We invoke the csrf middleware directly so we don't need a full
	// engine — the middleware only reads s.csrfCookieName /
	// s.csrfHeaderName.
	s := &server{csrfCookieName: "auracp_csrf", csrfHeaderName: "X-CSRF-Token"}
	mw := csrf(s)
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	handler := mw(next)

	// Token that the panel SPA would set on both cookie and header.
	const token = "csrf-token-value-1234567890"

	// Case 1: matching auracp_csrf cookie + X-CSRF-Token header → 200.
	r := httptest.NewRequest(http.MethodPost, "/anything", strings.NewReader("{}"))
	r.AddCookie(&http.Cookie{Name: "auracp_csrf", Value: token})
	r.Header.Set("X-CSRF-Token", token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !called {
		t.Fatalf("case 1 (matching custom names): code=%d called=%v want 200 true", w.Code, called)
	}

	// Case 2: the standalone defaults must NOT validate when names were
	// rebound. Cookie set under __Host-aura_csrf, header under
	// X-Aura-Csrf — should be rejected.
	called = false
	r = httptest.NewRequest(http.MethodPost, "/anything", strings.NewReader("{}"))
	r.AddCookie(&http.Cookie{Name: "__Host-aura_csrf", Value: token})
	r.Header.Set("X-Aura-Csrf", token)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code == http.StatusOK {
		t.Fatalf("case 2 (default names with rebound config): code=%d, want 403", w.Code)
	}
	if called {
		t.Fatalf("case 2: next handler invoked despite CSRF rebinding")
	}

	// Case 3: matching cookie but wrong header value → 403.
	called = false
	r = httptest.NewRequest(http.MethodPost, "/anything", strings.NewReader("{}"))
	r.AddCookie(&http.Cookie{Name: "auracp_csrf", Value: token})
	r.Header.Set("X-CSRF-Token", "wrong-value")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("case 3 (header mismatch): code=%d, want 403", w.Code)
	}
	if called {
		t.Fatalf("case 3: next handler invoked on token mismatch")
	}
}

// TestCSRF_DefaultsUnchangedWhenOptionsZero verifies that the default
// CSRF identity remains __Host-aura_csrf / X-Aura-Csrf when an
// embedder leaves Options.CSRFCookieName / Options.CSRFHeaderName
// empty. This is the standalone deployment contract.
func TestCSRF_DefaultsUnchangedWhenOptionsZero(t *testing.T) {
	s := &server{csrfCookieName: DefaultCSRFCookieName, csrfHeaderName: DefaultCSRFHeaderName}
	mw := csrf(s)
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	const token = "default-token-abcdefghij"

	r := httptest.NewRequest(http.MethodPost, "/anything", strings.NewReader("{}"))
	r.AddCookie(&http.Cookie{Name: "__Host-aura_csrf", Value: token})
	r.Header.Set("X-Aura-Csrf", token)
	w := httptest.NewRecorder()
	mw(next).ServeHTTP(w, r)
	if w.Code != http.StatusOK || !called {
		t.Fatalf("defaults: code=%d called=%v want 200 true", w.Code, called)
	}
}

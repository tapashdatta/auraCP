package standalone

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHIBP_DetectsPwned(t *testing.T) {
	// "password" SHA-1 = 5BAA61E4C9B93F3F0682250B6CF8331B7EE68FD8.
	// Prefix = "5BAA6"; tail = "1E4C9B93F3F0682250B6CF8331B7EE68FD8".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/5BAA6") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte("1E4C9B93F3F0682250B6CF8331B7EE68FD8:1234\nDEADBEEF:0001\n"))
	}))
	defer srv.Close()
	c := &HIBPClient{Endpoint: srv.URL + "/"}
	err := c.Check(context.Background(), "password")
	if !errors.Is(err, ErrPasswordPwned) {
		t.Fatalf("expected ErrPasswordPwned, got %v", err)
	}
}

func TestHIBP_AcceptsNovelPassword(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA:1\n"))
	}))
	defer srv.Close()
	c := &HIBPClient{Endpoint: srv.URL + "/"}
	if err := c.Check(context.Background(), "definitely-not-in-the-corpus"); err != nil {
		t.Fatalf("expected nil; got %v", err)
	}
}

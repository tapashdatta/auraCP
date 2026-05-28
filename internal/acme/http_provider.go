package acme

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/registration"
)

// httpProvider satisfies lego's challenge.Provider for HTTP-01 by writing the
// keyAuth file into auracpd's shared ACME directory. nginx serves
// /.well-known/acme-challenge/<token> from there via a stock `location` block
// that lives in every vhost's HTTP server{}.
type httpProvider struct{ dir string }

func newHTTPProvider(dir string) *httpProvider { return &httpProvider{dir: dir} }

func (p *httpProvider) Present(domain, token, keyAuth string) error {
	if err := os.MkdirAll(p.dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(p.dir, token), []byte(keyAuth), 0o644)
}

func (p *httpProvider) CleanUp(domain, token, keyAuth string) error {
	_ = os.Remove(filepath.Join(p.dir, token))
	return nil
}

// Ensure we implement lego's Provider interface (Present + CleanUp).
var _ challenge.Provider = (*httpProvider)(nil)

// registration persistence — simple JSON to disk.

func loadRegistration(path string) (*registration.Resource, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var r registration.Resource
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, false
	}
	return &r, true
}

func saveRegistration(path string, r *registration.Resource) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

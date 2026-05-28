// Package acme owns the in-process ACME client. auraCP issues + renews Let's
// Encrypt certificates itself via go-acme/lego (HTTP-01 by default; DNS-01 via
// Cloudflare when a token is configured). Issued certs land in /etc/auracp/ssl
// and are loaded by nginx; renewal runs as a goroutine in cmd/auracpd.
//
// State machine per domain (in the `certificates` table):
//
//	pending  → first issuance not yet attempted (or initial attempt running)
//	issued   → cert is on disk and valid
//	renewing → renewal in flight; do not re-enter
//	failed   → last attempt errored; retry with exponential backoff
package acme

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	mathrand "math/rand"
	"os"
	"path/filepath"
	"sync"
	"time"

	legoacme "github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"

	"github.com/auracp/auracp/internal/paths"
	"github.com/auracp/auracp/internal/store"
)

// Manager is the single ACME orchestrator owned by auracpd.
type Manager struct {
	store    *store.Store
	etcDir   string // /etc/auracp
	staging  bool   // use LE staging endpoint when true (dev safety)
	mu       sync.Mutex
	acct     *legoAccount
	client   *lego.Client
	reloader func(context.Context) error // called after a cert lands → nginx reload
}

// New builds the manager. The reloader callback is invoked after each
// successful issuance/renewal so nginx picks up the new cert immediately.
func New(st *store.Store, etcDir string, reloader func(context.Context) error) *Manager {
	return &Manager{store: st, etcDir: etcDir, reloader: reloader}
}

// SetStaging flips the ACME endpoint to LE staging — useful during install on
// boxes where you'd otherwise blow LE's rate limits while iterating.
func (m *Manager) SetStaging(s bool) { m.staging = s }

// EnsureCert is the public entry-point: it makes sure a valid cert exists for
// `domain`, issuing or renewing as needed. Idempotent + safe to call from
// multiple goroutines for different domains.
func (m *Manager) EnsureCert(ctx context.Context, domain string) error {
	cur, ok := m.store.Certificate(domain)
	if ok && cur.Status == "issued" && cur.ExpiresAt.Valid {
		remaining := time.Until(time.Unix(cur.ExpiresAt.Int64, 0))
		if remaining > 30*24*time.Hour {
			return nil // still fresh
		}
	}
	return m.issue(ctx, domain)
}

// IssueOnce attempts to issue a cert exactly once, regardless of current
// state. Used for the "force renew" admin path and the renewal loop.
func (m *Manager) IssueOnce(ctx context.Context, domain string) error {
	return m.issue(ctx, domain)
}

func (m *Manager) issue(ctx context.Context, domain string) error {
	if err := m.ensureClient(); err != nil {
		return err
	}
	// Mark renewing so concurrent callers / the renewal loop see it.
	prev, _ := m.store.Certificate(domain)
	prev.Domain = domain
	if prev.Issuer == "" {
		prev.Issuer = "letsencrypt"
	}
	prev.Status = "renewing"
	prev.Attempts++
	if err := m.store.UpsertCertificate(prev); err != nil {
		return err
	}

	req := legoacme.ObtainRequest{
		Domains: []string{domain},
		Bundle:  true,
	}
	res, err := m.client.Certificate.Obtain(req)
	if err != nil {
		prev.Status = "failed"
		prev.LastError = err.Error()
		_ = m.store.UpsertCertificate(prev)
		return fmt.Errorf("obtain cert for %s: %w", domain, err)
	}

	if err := os.MkdirAll(paths.SSLDir, 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(paths.CertPath(domain), res.Certificate, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(paths.KeyPath(domain), res.PrivateKey, 0o600); err != nil {
		return err
	}

	exp, _ := parseLeafExpiry(res.Certificate)
	prev.Status = "issued"
	prev.LastError = ""
	prev.CertPath = sql.NullString{String: paths.CertPath(domain), Valid: true}
	prev.KeyPath = sql.NullString{String: paths.KeyPath(domain), Valid: true}
	prev.IssuedAt = sql.NullInt64{Int64: time.Now().Unix(), Valid: true}
	if !exp.IsZero() {
		prev.ExpiresAt = sql.NullInt64{Int64: exp.Unix(), Valid: true}
	}
	if err := m.store.UpsertCertificate(prev); err != nil {
		return err
	}
	log.Printf("acme: issued cert for %s (expires %s)", domain, exp.Format(time.RFC3339))

	// Tell nginx about the new file so it actually serves it.
	if m.reloader != nil {
		if err := m.reloader(ctx); err != nil {
			log.Printf("acme: reload after issuance failed: %v", err)
		}
	}
	return nil
}

// StartRenewalLoop kicks off the daily renewal scheduler. Each tick: pull all
// certs expiring within 30 days, jitter, renew. Failures recorded in
// last_error; retries happen on subsequent ticks (no in-tick retry storm).
func (m *Manager) StartRenewalLoop(ctx context.Context) {
	go func() {
		// Initial delay so we don't slam ACME on startup.
		select {
		case <-time.After(30 * time.Second):
		case <-ctx.Done():
			return
		}
		ticker := time.NewTicker(12 * time.Hour)
		defer ticker.Stop()
		for {
			m.renewDue(ctx)
			select {
			case <-ticker.C:
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (m *Manager) renewDue(ctx context.Context) {
	due, err := m.store.CertificatesExpiringWithin(time.Now().Unix(), int64((30 * 24 * time.Hour).Seconds()))
	if err != nil {
		log.Printf("acme: list due certs: %v", err)
		return
	}
	for _, c := range due {
		// Random ±2h jitter so a host with 50 certs doesn't burst-renew.
		jitter := time.Duration(mathrand.Int63n(int64(2*time.Hour))) - time.Hour
		time.Sleep(jitter)
		if err := m.IssueOnce(ctx, c.Domain); err != nil {
			log.Printf("acme: renew %s: %v", c.Domain, err)
		}
	}
}

// --- lego plumbing ---

type legoAccount struct {
	Email        string
	Registration *registration.Resource
	Key          crypto.PrivateKey
}

func (a *legoAccount) GetEmail() string                        { return a.Email }
func (a *legoAccount) GetRegistration() *registration.Resource { return a.Registration }
func (a *legoAccount) GetPrivateKey() crypto.PrivateKey        { return a.Key }

func (m *Manager) ensureClient() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client != nil {
		return nil
	}

	acmeRoot := filepath.Join(m.etcDir, "acme")
	if err := os.MkdirAll(acmeRoot, 0o700); err != nil {
		return err
	}
	keyPath := filepath.Join(acmeRoot, "account.key")
	regPath := filepath.Join(acmeRoot, "registration.json")

	key, err := loadOrCreateAccountKey(keyPath)
	if err != nil {
		return err
	}

	// Contact email is OPTIONAL for Let's Encrypt — accounts can register
	// without one. The previous default of "admin@auracp.local" was rejected
	// outright with `invalidContact :: contact email has invalid domain`
	// because LE validates the TLD against the public suffix list and `.local`
	// (RFC 6762, mDNS) isn't on it. Leave email empty unless the operator
	// explicitly set one via Settings → SSL (key: `acme_email`); LE then
	// registers a no-contact account (you won't get expiry-reminder emails).
	var email string
	if e, ok := m.store.GetSetting("acme_email"); ok && e != "" {
		email = e
	}
	acct := &legoAccount{Email: email, Key: key}
	cfg := lego.NewConfig(acct)
	if m.staging {
		cfg.CADirURL = lego.LEDirectoryStaging
	} else {
		cfg.CADirURL = lego.LEDirectoryProduction
	}
	cli, err := lego.NewClient(cfg)
	if err != nil {
		return err
	}
	// HTTP-01: lego writes the token file via our challenge directory; nginx
	// serves /.well-known/acme-challenge/ from paths.ACMEChallengeDir.
	if err := cli.Challenge.SetHTTP01Provider(newHTTPProvider(paths.ACMEChallengeDir)); err != nil {
		return err
	}

	// Register (or load existing registration) — terms accepted programmatically.
	if reg, ok := loadRegistration(regPath); ok {
		acct.Registration = reg
	} else {
		reg, err := cli.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			return fmt.Errorf("ACME register: %w", err)
		}
		acct.Registration = reg
		_ = saveRegistration(regPath, reg)
	}
	m.acct = acct
	m.client = cli
	return nil
}

func loadOrCreateAccountKey(path string) (crypto.PrivateKey, error) {
	if b, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(b)
		if block == nil {
			return nil, errors.New("invalid account key PEM")
		}
		return x509.ParseECPrivateKey(block.Bytes)
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return nil, err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		return nil, err
	}
	return k, nil
}

// parseLeafExpiry returns the NotAfter of the leaf cert (the first PEM block).
func parseLeafExpiry(certPEM []byte) (time.Time, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return time.Time{}, errors.New("no PEM block")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, err
	}
	return c.NotAfter, nil
}

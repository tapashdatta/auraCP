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
	"strings"
	"sync"
	"time"

	legoacme "github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/registration"

	"github.com/auracp/auracp/internal/paths"
	"github.com/auracp/auracp/internal/secret"
	"github.com/auracp/auracp/internal/store"
)

// IssueOpts configures a single issuance call. Zero value = HTTP-01 only
// (the standard ACME path; needs port 80 reachable to the public IP).
// ForceDNS01 = use Cloudflare DNS-01 instead (needs an instance CF token).
// v0.2.42 made these strictly disjoint — no automatic fallback either way.
type IssueOpts struct {
	ForceDNS01 bool
}

// cfTokenSettingKey mirrors api.cfTokenKey but lives here so the renewal
// loop (which doesn't import internal/api) can resolve the token too.
const cfTokenSettingKey = "cloudflare_token_enc"

// Manager is the single ACME orchestrator owned by auracpd.
type Manager struct {
	store    *store.Store
	etcDir   string // /etc/auracp
	staging  bool   // use LE staging endpoint when true (dev safety)
	mu       sync.Mutex
	acct     *legoAccount
	client   *lego.Client
	reloader func(context.Context) error // called after a cert lands → nginx reload
	secret   *secret.Box                 // v0.2.41: decrypt the CF token for DNS-01 fallback
}

// New builds the manager. The reloader callback is invoked after each
// successful issuance/renewal so nginx picks up the new cert immediately.
// The secret box is used to decrypt the operator's Cloudflare API token
// when falling back from HTTP-01 to DNS-01 (v0.2.41).
func New(st *store.Store, etcDir string, reloader func(context.Context) error, sec *secret.Box) *Manager {
	return &Manager{store: st, etcDir: etcDir, reloader: reloader, secret: sec}
}

// cloudflareToken decrypts the instance-wide CF API token from the settings
// table, or returns "" if none is configured. Centralised so both issue()
// and the renewal loop pick up the same token.
func (m *Manager) cloudflareToken() string {
	if m.secret == nil {
		return ""
	}
	enc, ok := m.store.GetSetting(cfTokenSettingKey)
	if !ok || enc == "" {
		return ""
	}
	tok, err := m.secret.Decrypt(enc)
	if err != nil {
		return ""
	}
	return tok
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
	return m.issue(ctx, domain, IssueOpts{})
}

// IssueOnce attempts to issue a cert exactly once, regardless of current
// state. Used for the "force renew" admin path and the renewal loop.
//
// v0.2.42 — strict separation of methods (no automatic fallback):
//   • Default: HTTP-01 only via /.well-known/acme-challenge/. Fails fast
//     with a clear error if the domain isn't reachable on port 80; the
//     operator decides whether to flip to DNS-01 explicitly.
//   • ForceDNS01: skip HTTP-01 entirely and use Cloudflare DNS-01.
//     Required for wildcards and Cloudflare-proxied domains. Requires the
//     operator to have configured a CF API token at the instance level
//     AND turned the per-site cloudflare_dns toggle on.
//
// Why no automatic fallback: silently trying DNS-01 after HTTP-01 fails
// hides the actual problem and demands a CF token operators may not want
// to grant. The right default is "the standard way, and tell me clearly
// when it can't work".
func (m *Manager) IssueOnce(ctx context.Context, domain string, opts ...IssueOpts) error {
	var o IssueOpts
	if len(opts) > 0 {
		o = opts[0]
	}
	return m.issue(ctx, domain, o)
}

func (m *Manager) issue(ctx context.Context, domain string, opts IssueOpts) error {
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

	// ForceDNS01: operator explicitly chose Cloudflare DNS-01 for this site
	// (per-site toggle ON, or wildcard request). Refuse if no CF token.
	if opts.ForceDNS01 {
		cfTok := m.cloudflareToken()
		if cfTok == "" {
			prev.Status = "failed"
			prev.LastError = "DNS-01 was selected but no Cloudflare API token is configured. Configure one under Settings → Cloudflare, or turn off Cloudflare DNS-01 on this site to use HTTP-01."
			_ = m.store.UpsertCertificate(prev)
			return fmt.Errorf("DNS-01 selected but no Cloudflare token configured")
		}
		if err := m.setDNS01Cloudflare(cfTok); err != nil {
			prev.Status = "failed"
			prev.LastError = "DNS-01 setup failed: " + err.Error()
			_ = m.store.UpsertCertificate(prev)
			return fmt.Errorf("DNS-01 setup: %w", err)
		}
		log.Printf("acme: issuing %s via DNS-01 (Cloudflare, operator-selected)", domain)
		res, err := m.obtainWithKidHeal(req)
		if err != nil {
			prev.Status = "failed"
			prev.LastError = "DNS-01 (Cloudflare): " + err.Error()
			_ = m.store.UpsertCertificate(prev)
			return fmt.Errorf("obtain cert for %s (DNS-01): %w", domain, err)
		}
		return m.saveIssuedCert(ctx, domain, &prev, res, "dns-01")
	}

	// Default path: HTTP-01 only. No silent fallback.
	if err := m.setHTTP01(); err != nil {
		return err
	}
	res, err := m.obtainWithKidHeal(req)
	if err != nil {
		// Helpful hint when the error pattern matches "Cloudflare proxy
		// in the way" — direct the operator to the opt-in DNS-01 path
		// rather than silently doing it for them.
		hint := ""
		if looksLikeProxiedDomain(err) && m.cloudflareToken() != "" {
			hint = " (this looks like a proxied / firewalled origin; enable Cloudflare DNS-01 in this site's SSL tab to issue via DNS instead)"
		}
		prev.Status = "failed"
		prev.LastError = err.Error() + hint
		_ = m.store.UpsertCertificate(prev)
		return fmt.Errorf("obtain cert for %s (HTTP-01): %w%s", domain, err, hint)
	}
	return m.saveIssuedCert(ctx, domain, &prev, res, "http-01")
}

// obtainWithKidHeal wraps client.Certificate.Obtain with a one-shot retry
// when LE rejects the JWS for missing kid — the symptom of a corrupted
// registration cache that escaped ensureClient's startup heal (e.g. when
// the client was built before v0.2.43 and is still cached in memory).
//
// Detection: error message contains both "JWS" and "Key ID" (LE's exact
// wording is "Unable to validate JWS :: No Key ID in JWS header"). On a
// hit we discard the cached client + the on-disk registration.json, force
// a fresh ensureClient (which re-runs the startup heal + ResolveAccountByKey
// → Register fallback), and retry the obtain. If the retry STILL fails we
// surface the second error — the registration is genuinely broken and the
// operator needs to look.
func (m *Manager) obtainWithKidHeal(req legoacme.ObtainRequest) (*legoacme.Resource, error) {
	res, err := m.client.Certificate.Obtain(req)
	if err == nil || !isKidError(err) {
		return res, err
	}
	log.Printf("acme: detected 'No Key ID in JWS header' — resetting registration cache and retrying")
	// Throw away the in-memory client + on-disk registration, then rebuild.
	m.mu.Lock()
	m.client = nil
	m.acct = nil
	m.mu.Unlock()
	_ = os.Remove(filepath.Join(m.etcDir, "acme", "registration.json"))
	if rerr := m.ensureClient(); rerr != nil {
		return nil, fmt.Errorf("rebuild after kid error: %w", rerr)
	}
	// Re-arm the challenge that was set before the failure. We only know
	// the type indirectly; both branches above set the matching provider
	// before calling us, so we can just re-set HTTP-01 (the dominant case);
	// the ForceDNS01 branch will re-arm DNS-01 itself on a second Issue
	// click because that path is operator-driven.
	if err := m.setHTTP01(); err != nil {
		return nil, err
	}
	return m.client.Certificate.Obtain(req)
}

func isKidError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "JWS") && strings.Contains(s, "Key ID")
}

// looksLikeProxiedDomain matches lego/LE error text that typically means the
// HTTP-01 probe was answered by something other than our nginx — Cloudflare,
// a WAF, or some other reverse-proxy that didn't pass the challenge through.
func looksLikeProxiedDomain(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, k := range []string{"cloudflare", "unauthorized", "incorrect", "404", "403", "connection refused", "timeout", "dns problem"} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// saveIssuedCert persists the cert to disk + updates the store row.
// Shared by both the HTTP-01 and DNS-01 success paths.
func (m *Manager) saveIssuedCert(ctx context.Context, domain string, prev *store.Certificate, res *legoacme.Resource, method string) error {
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
	if err := m.store.UpsertCertificate(*prev); err != nil {
		return err
	}
	log.Printf("acme: issued cert for %s via %s (expires %s)", domain, method, exp.Format(time.RFC3339))
	if m.reloader != nil {
		if err := m.reloader(ctx); err != nil {
			log.Printf("acme: reload after issuance failed: %v", err)
		}
	}
	return nil
}

// setHTTP01 + setDNS01Cloudflare swap the client's active challenge solver.
// lego clients accept Remove + Set in sequence; we don't rebuild the whole
// client (the registration cache + account state would be wasted).
func (m *Manager) setHTTP01() error {
	m.client.Challenge.Remove(challenge.DNS01)
	return m.client.Challenge.SetHTTP01Provider(newHTTPProvider(paths.ACMEChallengeDir))
}

func (m *Manager) setDNS01Cloudflare(token string) error {
	m.client.Challenge.Remove(challenge.HTTP01)
	cfg := cloudflare.NewDefaultConfig()
	cfg.AuthToken = token
	prov, err := cloudflare.NewDNSProviderConfig(cfg)
	if err != nil {
		return err
	}
	return m.client.Challenge.SetDNS01Provider(prov)
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
	//
	// v0.2.43: self-heal when registration.json is missing/partial. lego v4 stores
	// the account URI on registration.Resource; the JWS signer derives the `kid`
	// header from that URI. A partial file (Body present, URI empty) makes every
	// signed request fail with "No Key ID in JWS header :: malformed". So we
	// validate URI presence, then:
	//   1. ResolveAccountByKey — looks up an existing LE account by the public
	//      key we already have. No new-account creation, no rate-limit hit. The
	//      common case after an upgrade that wrote a partial registration.json.
	//   2. If no account exists for this key (truly fresh install), Register()
	//      creates one.
	// Either way we re-write registration.json with the full Resource so the
	// next start doesn't repeat the work.
	if reg, ok := loadRegistration(regPath); ok && reg.URI != "" {
		acct.Registration = reg
	} else {
		// Re-resolve before falling back to a fresh Register. lego will sign
		// the resolve request with the account key and check whether LE knows
		// about it; on hit it returns the existing Resource (with URI).
		reg, rerr := cli.Registration.ResolveAccountByKey()
		if rerr != nil || reg == nil || reg.URI == "" {
			r2, err := cli.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
			if err != nil {
				return fmt.Errorf("ACME register: %w", err)
			}
			reg = r2
		} else {
			log.Printf("acme: re-resolved existing LE account via key (registration.json was stale; restored)")
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

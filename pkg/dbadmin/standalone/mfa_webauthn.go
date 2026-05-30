package standalone

// v0.3.2-D: WebAuthn / FIDO2 step-up.
//
// This file is the thin wrapper around github.com/go-webauthn/webauthn.
// It exposes four pure helpers — RegistrationBegin/Finish and
// AssertionBegin/Finish — plus the WebAuthnUser adapter that the
// library's User interface needs. All DB I/O lives in
// users_webauthn.go / auth_webauthn.go; this file MUST stay free of
// store access so it can be unit-tested without a SQLite handle.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	wproto "github.com/go-webauthn/webauthn/protocol"
	wlib "github.com/go-webauthn/webauthn/webauthn"
)

// ErrWebAuthnDisabled is returned by every helper when the operator
// has not enabled WebAuthn in MFAConfig.
var ErrWebAuthnDisabled = errors.New("standalone: WebAuthn is not enabled")

// ErrWebAuthnNoCredentials is returned by AssertionBegin when the
// user has no registered WebAuthn credentials.
var ErrWebAuthnNoCredentials = errors.New("standalone: user has no WebAuthn credentials")

// ErrWebAuthnChallengeNotFound is returned by Finish helpers when the
// matching challenge row has expired or never existed. Mapped to
// dbadmin.ErrUnauthenticated at the HTTP boundary.
var ErrWebAuthnChallengeNotFound = errors.New("standalone: WebAuthn challenge not found or expired")

// WebAuthnCredential is the in-memory + on-disk shape of a registered
// authenticator. credential_id is the WebAuthn raw credential ID
// (binary). The public_key blob is the COSE-encoded credential public
// key as returned by the authenticator during registration; the
// library re-parses it on every assertion. SignCount is the per-
// credential replay counter — MUST be advanced monotonically.
type WebAuthnCredential struct {
	UserID       string
	CredentialID []byte
	PublicKey    []byte
	SignCount    uint32
	AAGUID       []byte
	Transports   []string
	Name         string
	Attestation  []byte
	CreatedAt    int64
	LastUsedAt   int64
}

// webauthnUser adapts a UserRecord + its credential list to the
// go-webauthn library's User interface.
//
// WebAuthnID MUST be a stable opaque byte sequence that uniquely
// identifies the user inside the RP; we use the ULID id directly
// (already random, fits the 64-byte cap). WebAuthnName is the login
// handle the browser surfaces in the "use a key for <X>" prompt;
// WebAuthnDisplayName is a richer label — we mirror the username for
// both since the standalone store has no separate display field.
type webauthnUser struct {
	id          string
	username    string
	credentials []wlib.Credential
}

func (u *webauthnUser) WebAuthnID() []byte               { return []byte(u.id) }
func (u *webauthnUser) WebAuthnName() string             { return u.username }
func (u *webauthnUser) WebAuthnDisplayName() string      { return u.username }
func (u *webauthnUser) WebAuthnCredentials() []wlib.Credential {
	return u.credentials
}

// newWebAuthn constructs a *wlib.WebAuthn from the operator config.
// Returns ErrWebAuthnDisabled when MFA.WebAuthnEnabled is false.
//
// EncodeUserIDAsString=true because we encode the user id as the ULID
// string and the browser library decodes it as a UTF-8 string, not
// base64url. RPID is mandatory; RPOrigins defaults to "https://<RPID>"
// so a minimal config (rp_id only) works for the common case where the
// panel is served from a single hostname over HTTPS.
func newWebAuthn(cfg WebAuthnConfig) (*wlib.WebAuthn, error) {
	if strings.TrimSpace(cfg.RPID) == "" {
		return nil, fmt.Errorf("standalone: webauthn.rp_id is required when webauthn_enabled=true")
	}
	origins := cfg.RPOrigins
	if len(origins) == 0 {
		origins = []string{"https://" + cfg.RPID}
	}
	displayName := strings.TrimSpace(cfg.RPDisplayName)
	if displayName == "" {
		displayName = "aura-db"
	}
	return wlib.New(&wlib.Config{
		RPID:                 cfg.RPID,
		RPDisplayName:        displayName,
		RPOrigins:            origins,
		EncodeUserIDAsString: true,
	})
}

// libCredentialOf converts a stored WebAuthnCredential back into the
// library's Credential shape so go-webauthn can validate an assertion.
//
// We keep just the minimum the library needs at assertion time
// (ID, PublicKey, SignCount, AAGUID, Transports). The full attestation
// blob is only required when re-running attestation validation against
// the FIDO Metadata Service — not on the assertion hot path.
func libCredentialOf(c WebAuthnCredential) wlib.Credential {
	tx := make([]wproto.AuthenticatorTransport, 0, len(c.Transports))
	for _, t := range c.Transports {
		tx = append(tx, wproto.AuthenticatorTransport(t))
	}
	return wlib.Credential{
		ID:        c.CredentialID,
		PublicKey: c.PublicKey,
		Transport: tx,
		Authenticator: wlib.Authenticator{
			AAGUID:    c.AAGUID,
			SignCount: c.SignCount,
		},
	}
}

// RegistrationBegin generates a fresh registration challenge for user.
// Returns the JSON-ready creation options that the browser hands to
// navigator.credentials.create plus the serialized SessionData blob
// the caller MUST persist for the matching Finish call.
//
// existing is the user's already-registered credentials; the library
// uses them to populate excludeCredentials so the browser refuses to
// enroll the same authenticator twice.
func RegistrationBegin(cfg WebAuthnConfig, user UserRecord, existing []WebAuthnCredential) (creation *wproto.CredentialCreation, sessionBlob []byte, err error) {
	wa, err := newWebAuthn(cfg)
	if err != nil {
		return nil, nil, err
	}
	libCreds := make([]wlib.Credential, 0, len(existing))
	for _, c := range existing {
		libCreds = append(libCreds, libCredentialOf(c))
	}
	wu := &webauthnUser{id: user.ID, username: user.Username, credentials: libCreds}

	excludes := make([]wproto.CredentialDescriptor, 0, len(libCreds))
	for _, c := range libCreds {
		excludes = append(excludes, c.Descriptor())
	}
	creation, sess, err := wa.BeginRegistration(wu, wlib.WithExclusions(excludes))
	if err != nil {
		return nil, nil, err
	}
	blob, err := json.Marshal(sess)
	if err != nil {
		return nil, nil, err
	}
	return creation, blob, nil
}

// RegistrationFinish parses the attestation response, validates it
// against sessionBlob (from RegistrationBegin), and returns the
// freshly-minted credential. Caller persists the credential via
// Store.EnrollWebAuthnCredential.
func RegistrationFinish(cfg WebAuthnConfig, user UserRecord, existing []WebAuthnCredential, sessionBlob []byte, body io.Reader) (*WebAuthnCredential, error) {
	wa, err := newWebAuthn(cfg)
	if err != nil {
		return nil, err
	}
	var sess wlib.SessionData
	if err := json.Unmarshal(sessionBlob, &sess); err != nil {
		return nil, fmt.Errorf("standalone: webauthn session blob: %w", err)
	}
	libCreds := make([]wlib.Credential, 0, len(existing))
	for _, c := range existing {
		libCreds = append(libCreds, libCredentialOf(c))
	}
	wu := &webauthnUser{id: user.ID, username: user.Username, credentials: libCreds}

	parsed, err := wproto.ParseCredentialCreationResponseBody(body)
	if err != nil {
		return nil, err
	}
	cred, err := wa.CreateCredential(wu, sess, parsed)
	if err != nil {
		return nil, err
	}

	transports := make([]string, 0, len(cred.Transport))
	for _, t := range cred.Transport {
		transports = append(transports, string(t))
	}
	attBlob, _ := json.Marshal(cred.Attestation)
	return &WebAuthnCredential{
		UserID:       user.ID,
		CredentialID: cred.ID,
		PublicKey:    cred.PublicKey,
		SignCount:    cred.Authenticator.SignCount,
		AAGUID:       cred.Authenticator.AAGUID,
		Transports:   transports,
		Attestation:  attBlob,
	}, nil
}

// AssertionBegin builds the assertion challenge that the browser hands
// to navigator.credentials.get. existing MUST contain at least one
// credential (callers check); the library populates allowCredentials
// from the list so the browser knows which authenticators to wake up.
func AssertionBegin(cfg WebAuthnConfig, user UserRecord, existing []WebAuthnCredential) (assertion *wproto.CredentialAssertion, sessionBlob []byte, err error) {
	wa, err := newWebAuthn(cfg)
	if err != nil {
		return nil, nil, err
	}
	if len(existing) == 0 {
		return nil, nil, ErrWebAuthnNoCredentials
	}
	libCreds := make([]wlib.Credential, 0, len(existing))
	for _, c := range existing {
		libCreds = append(libCreds, libCredentialOf(c))
	}
	wu := &webauthnUser{id: user.ID, username: user.Username, credentials: libCreds}

	assertion, sess, err := wa.BeginLogin(wu)
	if err != nil {
		return nil, nil, err
	}
	blob, err := json.Marshal(sess)
	if err != nil {
		return nil, nil, err
	}
	return assertion, blob, nil
}

// AssertionFinish validates the assertion response against sessionBlob
// (from AssertionBegin) and returns the matched credential id plus the
// new sign-count the caller MUST persist via
// Store.UpdateWebAuthnSignCount. A non-monotonic counter indicates a
// cloned authenticator and triggers ErrUnauthenticated at the call
// site.
func AssertionFinish(cfg WebAuthnConfig, user UserRecord, existing []WebAuthnCredential, sessionBlob []byte, body io.Reader) (credentialID []byte, newSignCount uint32, err error) {
	wa, err := newWebAuthn(cfg)
	if err != nil {
		return nil, 0, err
	}
	var sess wlib.SessionData
	if err := json.Unmarshal(sessionBlob, &sess); err != nil {
		return nil, 0, fmt.Errorf("standalone: webauthn session blob: %w", err)
	}
	libCreds := make([]wlib.Credential, 0, len(existing))
	for _, c := range existing {
		libCreds = append(libCreds, libCredentialOf(c))
	}
	wu := &webauthnUser{id: user.ID, username: user.Username, credentials: libCreds}

	parsed, err := wproto.ParseCredentialRequestResponseBody(body)
	if err != nil {
		return nil, 0, err
	}
	cred, err := wa.ValidateLogin(wu, sess, parsed)
	if err != nil {
		return nil, 0, err
	}
	return cred.ID, cred.Authenticator.SignCount, nil
}


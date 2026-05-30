//go:build !cgo

package standalone

// v0.3.2-D: WebAuthn stub — non-CGO (CGO_ENABLED=0) build path.
//
// Static / cross-compiled binaries cannot use the real go-webauthn
// library because its transitive dependency chain (go-tpm/go-tpm-tools)
// does not compile cleanly on all platforms without CGO. This file
// provides the same exported symbols as mfa_webauthn.go but returns
// ErrWebAuthnDisabled for every call so the binary always links and the
// operator gets a clear error message rather than a build failure.
//
// The split mirrors pkg/dbadmin/classifier/postgres_ast_nocgo.go.

import (
	"errors"
	"io"
)

// ErrWebAuthnDisabled is returned by every helper when the operator
// has not enabled WebAuthn in MFAConfig, OR when the binary was
// compiled with CGO_ENABLED=0 (static build).
var ErrWebAuthnDisabled = errors.New("standalone: WebAuthn is not enabled")

// ErrWebAuthnNoCredentials is returned by AssertionBegin when the
// user has no registered WebAuthn credentials.
var ErrWebAuthnNoCredentials = errors.New("standalone: user has no WebAuthn credentials")

// ErrWebAuthnChallengeNotFound is returned by Finish helpers when the
// matching challenge row has expired or never existed.
var ErrWebAuthnChallengeNotFound = errors.New("standalone: WebAuthn challenge not found or expired")

// WebAuthnCredential is the stored shape of a registered authenticator.
// In the nocgo stub, this type exists for compilation compatibility only.
type WebAuthnCredential struct {
	UserID       string
	CredentialID []byte
	PublicKey    []byte
	SignCount     uint32
	AAGUID       []byte
	Transports   []string
	Name         string
	Attestation  []byte
	CreatedAt    int64
	LastUsedAt   int64
}

// RegistrationBegin always returns ErrWebAuthnDisabled in CGO_ENABLED=0 builds.
func RegistrationBegin(_ WebAuthnConfig, _ UserRecord, _ []WebAuthnCredential) (creation any, sessionBlob []byte, err error) {
	return nil, nil, ErrWebAuthnDisabled
}

// RegistrationFinish always returns ErrWebAuthnDisabled in CGO_ENABLED=0 builds.
func RegistrationFinish(_ WebAuthnConfig, _ UserRecord, _ []WebAuthnCredential, _ []byte, _ io.Reader) (*WebAuthnCredential, error) {
	return nil, ErrWebAuthnDisabled
}

// AssertionBegin always returns ErrWebAuthnDisabled in CGO_ENABLED=0 builds.
func AssertionBegin(_ WebAuthnConfig, _ UserRecord, _ []WebAuthnCredential) (assertion any, sessionBlob []byte, err error) {
	return nil, nil, ErrWebAuthnDisabled
}

// AssertionFinish always returns ErrWebAuthnDisabled in CGO_ENABLED=0 builds.
func AssertionFinish(_ WebAuthnConfig, _ UserRecord, _ []WebAuthnCredential, _ []byte, _ io.Reader) (credentialID []byte, newSignCount uint32, err error) {
	return nil, 0, ErrWebAuthnDisabled
}

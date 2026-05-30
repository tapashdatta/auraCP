// Package ssl reports a site's live TLS certificate status.
// Issuance/renewal is owned by internal/acme (lego); this package only
// observes what is being served.
package ssl

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"time"

	"github.com/auracp/auracp/internal/validate"
)

type Status struct {
	Status     string   `json:"status"`               // active|pending|error
	Issuer     string   `json:"issuer,omitempty"`
	Expires    string   `json:"expires,omitempty"`    // RFC3339
	Domains    []string `json:"domains,omitempty"`
	Message    string   `json:"message,omitempty"`
	SelfSigned bool     `json:"selfSigned,omitempty"` // local placeholder cert, no LE issued yet
}

// OfLocal reads the cert on disk at certPath and returns its status without
// making any network connection. Used when no LE cert has been issued yet —
// showing what a public DNS probe returns would be meaningless (it would show
// whoever actually owns the domain on the internet, not our server).
func OfLocal(certPath string) Status {
	data, err := os.ReadFile(certPath)
	if err != nil {
		return Status{Status: "pending", Message: "no certificate on disk yet"}
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return Status{Status: "pending", Message: "invalid certificate file"}
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return Status{Status: "pending", Message: "could not parse certificate"}
	}
	// The self-signed placeholder has Issuer == Subject (both set to the
	// domain CN). Any LE cert will have a different issuer.
	selfSigned := cert.Issuer.CommonName == cert.Subject.CommonName
	return Status{
		Status:     "active",
		Issuer:     cert.Issuer.CommonName,
		Expires:    cert.NotAfter.UTC().Format(time.RFC3339),
		Domains:    cert.DNSNames,
		SelfSigned: selfSigned,
	}
}

// Of dials the domain over TLS (SNI = domain) and inspects the served cert.
// Only call this when a real LE cert has been issued — dialling the public
// internet for a domain that doesn't point here returns whoever owns it.
func Of(domain string) Status {
	if err := validate.Domain(domain); err != nil {
		return Status{Status: "error", Message: "invalid domain"}
	}
	conn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 4 * time.Second}, "tcp", domain+":443",
		&tls.Config{ServerName: domain, MinVersion: tls.VersionTLS12},
	)
	if err != nil {
		return Status{Status: "pending", Message: "no certificate served yet"}
	}
	defer conn.Close()

	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return Status{Status: "pending", Message: "no certificate"}
	}
	leaf := certs[0]
	return Status{
		Status:  "active",
		Issuer:  leaf.Issuer.CommonName,
		Expires: leaf.NotAfter.UTC().Format(time.RFC3339),
		Domains: leaf.DNSNames,
	}
}

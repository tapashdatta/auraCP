// Package ssl reports a site's live TLS certificate status by reading the leaf
// certificate served on :443. Issuance/renewal is owned by internal/acme
// (lego); this package only observes what nginx is serving.
package ssl

import (
	"crypto/tls"
	"net"
	"time"

	"github.com/auracp/auracp/internal/validate"
)

type Status struct {
	Status  string   `json:"status"` // active|pending|error
	Issuer  string   `json:"issuer"`
	Expires string   `json:"expires"` // RFC3339
	Domains []string `json:"domains"`
	Message string   `json:"message,omitempty"`
}

// Of dials the domain over TLS (SNI = domain) and inspects the served cert.
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

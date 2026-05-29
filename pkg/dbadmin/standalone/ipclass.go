package standalone

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
)

// IPClass returns the privacy-truncated IP block (e.g. "1.2.3.0/24" or
// "2001:db8::/56") suitable for use as a binding key. Falls back to a
// literal "unknown" when r is nil or the RemoteAddr can't be parsed.
//
// This single-arg form ignores forwarded headers — use it only when the
// listener faces the public internet directly. Behind a reverse proxy
// (the documented nginx topology in STANDALONE-DEPLOY.md), every request
// would collapse to 127.0.0.1 here; use IPClassWithTrust instead and
// pass the parsed trusted-proxy CIDR list (SEC-07).
func IPClass(r *http.Request) string {
	return IPClassWithTrust(r, nil)
}

// IPClassWithTrust derives the privacy-truncated IP class for r, honoring
// X-Forwarded-For only when r.RemoteAddr is in one of the trustedProxies
// CIDRs. Picks the LEFTMOST untrusted address from the XFF chain — this
// is the original client even when chained proxies have prepended their
// own hops. When trustedProxies is empty, behavior is identical to
// looking at r.RemoteAddr only (safe default, prevents header spoofing
// from arbitrary callers).
func IPClassWithTrust(r *http.Request, trustedProxies []*net.IPNet) string {
	if r == nil {
		return "unknown"
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if host == "" {
		return "unknown"
	}
	remoteIP := net.ParseIP(host)
	if remoteIP != nil && ipInAny(remoteIP, trustedProxies) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// Walk left → right and return the first address that is
			// NOT itself in a trusted-proxy CIDR. nginx prepends each
			// hop, so the leftmost untrusted hop is the real client.
			for _, raw := range strings.Split(xff, ",") {
				ip := net.ParseIP(strings.TrimSpace(raw))
				if ip == nil {
					continue
				}
				if !ipInAny(ip, trustedProxies) {
					return IPClassFromHost(ip.String())
				}
			}
		}
	}
	return IPClassFromHost(host)
}

func ipInAny(ip net.IP, cidrs []*net.IPNet) bool {
	for _, n := range cidrs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// IPClassFromHost is the bare helper IPClass uses internally; exposed
// for tests + bootstrap diagnostics.
func IPClassFromHost(host string) string {
	ip := net.ParseIP(host)
	if ip == nil {
		return "unknown"
	}
	if v4 := ip.To4(); v4 != nil {
		v4[3] = 0
		return v4.String() + "/24"
	}
	// IPv6: zero everything past bit 56.
	masked := ip.Mask(net.CIDRMask(56, 128))
	return masked.String() + "/56"
}

// UAHash returns the first 16 hex characters of SHA-256(User-Agent).
// Used as a session-binding fingerprint.
func UAHash(r *http.Request) string {
	if r == nil {
		return ""
	}
	ua := r.Header.Get("User-Agent")
	return HashUAString(ua)
}

// HashUAString is exposed for tests / diagnostics.
func HashUAString(ua string) string {
	if strings.TrimSpace(ua) == "" {
		// Empty UA is still a valid binding key; use the digest of
		// the empty string for stability.
		ua = ""
	}
	sum := sha256.Sum256([]byte(ua))
	return hex.EncodeToString(sum[:])[:16]
}

package standalone

import (
	"bufio"
	"context"
	"crypto/sha1" //nolint:gosec // HIBP k-anonymity API requires SHA-1 by spec.
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// HIBPClient checks a candidate password against the haveibeenpwned.com
// pwned-passwords range API using k-anonymity (we only send the first 5
// hex chars of the SHA-1).
//
// HTTPClient is exported so tests can substitute a fake. When nil, a
// 5-second timeout client is used.
type HIBPClient struct {
	HTTPClient *http.Client
	Endpoint   string // default https://api.pwnedpasswords.com/range/
}

// DefaultHIBPClient returns a client wired to the real HIBP endpoint
// with a short timeout.
func DefaultHIBPClient() *HIBPClient {
	return &HIBPClient{
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
		Endpoint:   "https://api.pwnedpasswords.com/range/",
	}
}

// ErrPasswordPwned is returned by Check when the password's hash tail
// appears in the HIBP corpus.
var ErrPasswordPwned = errors.New("standalone: password appears in haveibeenpwned corpus")

// Check returns nil if the password is not seen in the corpus, or
// ErrPasswordPwned if it is. Network errors are surfaced as-is so the
// caller can decide whether to fail-closed (default) or fail-open
// (operator override).
func (c *HIBPClient) Check(ctx context.Context, password string) error {
	if c == nil {
		c = DefaultHIBPClient()
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = "https://api.pwnedpasswords.com/range/"
	}
	sum := sha1.Sum([]byte(password)) //nolint:gosec
	hashHex := strings.ToUpper(hex.EncodeToString(sum[:]))
	prefix, tail := hashHex[:5], hashHex[5:]

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+prefix, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "aura-db/standalone")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return readHIBPError(resp.Body, resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		// "<TAIL>:<COUNT>"
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		if strings.EqualFold(line[:colon], tail) {
			return ErrPasswordPwned
		}
	}
	return sc.Err()
}

func readHIBPError(body io.Reader, status int) error {
	b, _ := io.ReadAll(io.LimitReader(body, 1024))
	return &hibpError{status: status, body: strings.TrimSpace(string(b))}
}

type hibpError struct {
	status int
	body   string
}

func (e *hibpError) Error() string {
	return "standalone: HIBP non-200: " + e.body
}

package standalone

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// WebhookForwarder posts each audit line to URL with an
// X-Aura-Sig: <hex(HMAC-SHA256(secret, body))> header.
type WebhookForwarder struct {
	URL    string
	Secret []byte
	Client *http.Client // optional; default has 5s timeout
}

// Ship implements Forwarder.
func (w *WebhookForwarder) Ship(ctx context.Context, line []byte) error {
	if w == nil {
		return errors.New("standalone: nil WebhookForwarder")
	}
	if w.URL == "" {
		return errors.New("standalone: webhook URL required")
	}
	c := w.Client
	if c == nil {
		c = &http.Client{Timeout: 5 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(line))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if len(w.Secret) > 0 {
		mac := hmac.New(sha256.New, w.Secret)
		mac.Write(line)
		req.Header.Set("X-Aura-Sig", hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("standalone: webhook %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

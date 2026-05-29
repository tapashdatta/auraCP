package standalone

import "context"

// Forwarder ships an audit-log line (raw bytes, already canonical JSON
// terminated with '\n') to a remote sink. Implementations should be
// non-blocking with respect to the main request path: drain
// asynchronously and surface failure via logs/metrics.
type Forwarder interface {
	Ship(ctx context.Context, line []byte) error
}

// MultiForwarder fan-outs to several forwarders, ignoring per-target
// errors so a single misbehaving SIEM doesn't stop other shippers.
type MultiForwarder struct {
	Targets []Forwarder
}

// Ship satisfies Forwarder.
func (m MultiForwarder) Ship(ctx context.Context, line []byte) error {
	for _, t := range m.Targets {
		_ = t.Ship(ctx, line)
	}
	return nil
}

// NopForwarder is the do-nothing fallback used when no forwarders are
// configured.
type NopForwarder struct{}

// Ship satisfies Forwarder.
func (NopForwarder) Ship(context.Context, []byte) error { return nil }

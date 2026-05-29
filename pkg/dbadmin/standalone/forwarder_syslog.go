package standalone

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// SyslogForwarder ships audit lines over plain TCP or UDP. TLS support
// is deferred to a later PR — operators that need TLS today should run a
// local stunnel.
//
// Protocol is RFC 5424-shaped: <PRI>1 ISO-8601 host APP-NAME - - - MSG.
// Facility defaults to local6.
type SyslogForwarder struct {
	Address  string // "host:port"
	Protocol string // "tcp" or "udp" (default "tcp")
	Facility int    // syslog facility number; default 22 (local6)
	AppName  string // default "aura-db"

	mu   sync.Mutex
	conn net.Conn
}

// Ship implements Forwarder.
func (s *SyslogForwarder) Ship(ctx context.Context, line []byte) error {
	if s == nil {
		return errors.New("standalone: nil SyslogForwarder")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		proto := s.Protocol
		if proto == "" {
			proto = "tcp"
		}
		var d net.Dialer
		d.Timeout = 2 * time.Second
		c, err := d.DialContext(ctx, proto, s.Address)
		if err != nil {
			return err
		}
		s.conn = c
	}
	facility := s.Facility
	if facility == 0 {
		facility = 22
	}
	app := s.AppName
	if app == "" {
		app = "aura-db"
	}
	pri := facility*8 + 6 // severity = info
	host, _, _ := net.SplitHostPort(s.conn.LocalAddr().String())
	if host == "" {
		host = "aura-db"
	}
	msg := fmt.Sprintf("<%d>1 %s %s %s - - - %s",
		pri, time.Now().UTC().Format(time.RFC3339Nano), host, app, string(line))
	if !hasTrailingNewline(msg) {
		msg += "\n"
	}
	if err := s.conn.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		_ = s.conn.Close()
		s.conn = nil
		return err
	}
	if _, err := s.conn.Write([]byte(msg)); err != nil {
		_ = s.conn.Close()
		s.conn = nil
		return err
	}
	return nil
}

func hasTrailingNewline(s string) bool {
	return len(s) > 0 && s[len(s)-1] == '\n'
}

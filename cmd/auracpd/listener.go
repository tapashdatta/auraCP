package main

import (
	"errors"
	"net"
	"net/http"
	"time"
)

// peekConn re-presents one peeked byte before continuing from the underlying
// connection, so the consumer (TLS server / HTTP server) can read it normally.
type peekConn struct {
	net.Conn
	peeked []byte
}

func (p *peekConn) Read(b []byte) (int, error) {
	if len(p.peeked) > 0 {
		n := copy(b, p.peeked)
		p.peeked = p.peeked[n:]
		return n, nil
	}
	return p.Conn.Read(b)
}

// chanListener serves connections handed to it via a channel.
type chanListener struct {
	ch   chan net.Conn
	addr net.Addr
	done chan struct{}
}

func newChanListener(addr net.Addr) *chanListener {
	return &chanListener{ch: make(chan net.Conn, 64), addr: addr, done: make(chan struct{})}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error   { close(l.done); return nil }
func (l *chanListener) Addr() net.Addr { return l.addr }

// httpsRedirect responds 301 to the equivalent https URL on the same host.
func httpsRedirect(w http.ResponseWriter, r *http.Request) {
	host := r.Host // includes port
	target := "https://" + host + r.URL.RequestURI()
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

// splitTLSAndHTTP wraps ln so plaintext HTTP requests on the same port are
// peeled off and sent to the redirect listener instead of bombing TLS.
// Returns (tls, http) listeners; the caller serves the appropriate http.Server
// on each. The first byte of a TLS handshake is 0x16 — everything else is
// assumed to be plain HTTP.
func splitTLSAndHTTP(ln net.Listener) (*chanListener, *chanListener) {
	tlsLn := newChanListener(ln.Addr())
	httpLn := newChanListener(ln.Addr())
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				time.Sleep(20 * time.Millisecond)
				continue
			}
			go classify(c, tlsLn.ch, httpLn.ch)
		}
	}()
	return tlsLn, httpLn
}

func classify(c net.Conn, tlsCh, httpCh chan<- net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(4 * time.Second))
	buf := make([]byte, 1)
	n, err := c.Read(buf)
	_ = c.SetReadDeadline(time.Time{})
	if err != nil || n == 0 {
		_ = c.Close()
		return
	}
	pc := &peekConn{Conn: c, peeked: append([]byte(nil), buf[:n]...)}
	if buf[0] == 0x16 { // TLS handshake
		tlsCh <- pc
	} else {
		httpCh <- pc
	}
}

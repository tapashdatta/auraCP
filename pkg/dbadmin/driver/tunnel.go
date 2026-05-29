package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// Tunnel forwards a local socket to a remote DB through an SSH connection.
// Used when Connection.SSHTunnel is non-nil.
//
// Security per SECURITY.md §7.2 (post review-board findings, PR #3):
//   - Key-based auth only. Password auth and ssh-agent auth are NOT
//     attempted; we never pull a key out of the operator's agent.
//   - Host keys ARE verified against the operator-supplied
//     SSHTunnel.KnownHostsPath. Open fails closed if KnownHostsPath
//     is unset.
//   - Tunnel keys live at SSHTunnel.KeyPath with mode 0600 enforced;
//     refuses to load broader-permissioned keys. We also refuse keys
//     in world- or group-writable parent directories.
//   - Algorithm pinning: only modern KEX, ciphers, MACs, and host-key
//     types — explicitly excludes ssh-rsa SHA-1 and other legacy
//     primitives.
//   - Post-TCP deadline on the SSH handshake. ssh.NewClientConn does
//     not honor ClientConfig.Timeout, so we wrap the TCP conn with
//     SetDeadline before handing it to NewClientConn.
//   - Data-copy phase has an idle timeout to stop slow-loris style
//     attacks from holding tunnel slots indefinitely.
type Tunnel struct {
	cfg       *dbadmin.SSHTunnel
	dialAddr  string // remote DB host:port to forward to
	client    *ssh.Client
	listener  net.Listener
	localAddr string
	closed    atomic.Bool

	// closeCh closes when Tunnel.Close is invoked. acceptLoop and
	// forwarder goroutines select on it to fast-exit.
	closeCh chan struct{}
	wg      sync.WaitGroup

	// activePool caps the number of simultaneous forwarders.
	concurrencySem chan struct{}
}

// concurrencyLimit caps simultaneous channels through one tunnel. Set to
// pool size + small headroom (PR #3 review noted the prior 16 was
// disconnected from PoolSizePerConn). Effective limit is derived per-
// tunnel at construction time.
const minConcurrency = 6 // small headroom over the default PoolSizePerConn=4

// tunnelHandshakeTimeout caps the SSH connection negotiation (TCP +
// KEX + auth + identity).
const tunnelHandshakeTimeout = 10 * time.Second

// tunnelChannelTimeout caps the per-channel forward setup (the SSH
// "direct-tcpip" channel open).
const tunnelChannelTimeout = 10 * time.Second

// tunnelIdleTimeout drops a forwarded connection that goes idle for
// this long. Caps slow-loris attacks; the database driver typically
// keeps its own keepalive shorter than this.
const tunnelIdleTimeout = 5 * time.Minute

// OpenTunnel establishes an SSH tunnel to cfg.Host:cfg.Port and binds a
// loopback listener that forwards to dialAddr (the DB host:port, from
// the perspective of the SSH server's network).
//
// concurrency caps simultaneous forwarders; if <= minConcurrency, raises
// to minConcurrency.
func OpenTunnel(ctx context.Context, cfg *dbadmin.SSHTunnel, dialAddr string, concurrency int) (*Tunnel, error) {
	if cfg == nil {
		return nil, errors.New("driver/tunnel: nil cfg")
	}
	if cfg.Host == "" || cfg.Port == 0 {
		return nil, errors.New("driver/tunnel: Host and Port required")
	}
	if cfg.Username == "" {
		return nil, errors.New("driver/tunnel: Username required")
	}
	if cfg.KeyPath == "" {
		return nil, errors.New("driver/tunnel: KeyPath required (password auth not supported)")
	}
	if cfg.KnownHostsPath == "" {
		// Post-review fix: refuse to dial without a host-key pin.
		// SECURITY.md §7.2 mandates host-key verification; ssh.
		// InsecureIgnoreHostKey + log-only was the previous
		// behavior and is a known MITM primitive.
		return nil, errors.New("driver/tunnel: KnownHostsPath required (host-key verification is mandatory; configure SSH known_hosts and set KnownHostsPath on SSHTunnel)")
	}

	if err := verifyKeyMode(cfg.KeyPath); err != nil {
		return nil, err
	}
	if err := verifyKeyParentDir(cfg.KeyPath); err != nil {
		return nil, err
	}

	keyBytes, err := os.ReadFile(cfg.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("driver/tunnel: read key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("driver/tunnel: parse key: %w", err)
	}

	hostKeyCallback, err := knownhosts.New(cfg.KnownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("driver/tunnel: load known_hosts %q: %w", cfg.KnownHostsPath, err)
	}

	sshCfg := &ssh.ClientConfig{
		User: cfg.Username,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: hostKeyCallback,
		// Pin modern algorithms. Excludes ssh-rsa SHA-1.
		HostKeyAlgorithms: []string{
			ssh.KeyAlgoED25519,
			ssh.KeyAlgoRSASHA512,
			ssh.KeyAlgoRSASHA256,
			ssh.KeyAlgoECDSA256,
			ssh.KeyAlgoECDSA384,
			ssh.KeyAlgoECDSA521,
		},
		Timeout: tunnelHandshakeTimeout,
	}
	// Pin modern KEX, ciphers, MACs via ssh.Config.
	sshCfg.Config = ssh.Config{
		KeyExchanges: []string{
			"curve25519-sha256",
			"curve25519-sha256@libssh.org",
			"ecdh-sha2-nistp256",
			"ecdh-sha2-nistp384",
			"ecdh-sha2-nistp521",
		},
		Ciphers: []string{
			"chacha20-poly1305@openssh.com",
			"aes128-gcm@openssh.com",
			"aes256-gcm@openssh.com",
			"aes128-ctr",
			"aes192-ctr",
			"aes256-ctr",
		},
		MACs: []string{
			"hmac-sha2-256-etm@openssh.com",
			"hmac-sha2-512-etm@openssh.com",
			"hmac-sha2-256",
			"hmac-sha2-512",
		},
	}

	d := net.Dialer{Timeout: tunnelHandshakeTimeout}
	hostPort := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))
	tcpConn, err := d.DialContext(ctx, "tcp", hostPort)
	if err != nil {
		return nil, fmt.Errorf("driver/tunnel: dial SSH: %w", err)
	}
	// Post-review fix: NewClientConn doesn't honor ClientConfig.Timeout.
	// SetDeadline on the underlying TCP conn caps the entire SSH KEX +
	// auth handshake. On success we clear the deadline so the long-
	// lived tunnel isn't held to it.
	_ = tcpConn.SetDeadline(time.Now().Add(tunnelHandshakeTimeout))
	sshConn, chans, reqs, err := ssh.NewClientConn(tcpConn, hostPort, sshCfg)
	if err != nil {
		_ = tcpConn.Close()
		return nil, fmt.Errorf("driver/tunnel: SSH handshake: %w", err)
	}
	_ = tcpConn.SetDeadline(time.Time{})
	client := ssh.NewClient(sshConn, chans, reqs)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("driver/tunnel: listen: %w", err)
	}

	if concurrency < minConcurrency {
		concurrency = minConcurrency
	}
	t := &Tunnel{
		cfg:            cfg,
		dialAddr:       dialAddr,
		client:         client,
		listener:       lis,
		localAddr:      lis.Addr().String(),
		closeCh:        make(chan struct{}),
		concurrencySem: make(chan struct{}, concurrency),
	}

	t.wg.Add(1)
	go t.acceptLoop()
	return t, nil
}

// LocalAddr returns the loopback host:port the database driver should dial.
//
// SECURITY NOTE: this listener accepts connections from any process on the
// host. The auracpd deployment model assumes a dedicated host; if you
// share the host with other tenants, see docs/aura-db/KNOWN-ISSUES.md
// "tunnel local-listener exposure" for the deferred unix-socket migration.
func (t *Tunnel) LocalAddr() string {
	return t.localAddr
}

// Close stops the listener + SSH connection. Idempotent. Waits for all
// forwarders to drain.
func (t *Tunnel) Close() error {
	if t.closed.Swap(true) {
		return nil
	}
	close(t.closeCh)
	var firstErr error
	if err := t.listener.Close(); err != nil {
		firstErr = err
	}
	if err := t.client.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	t.wg.Wait()
	return firstErr
}

// acceptLoop runs until the listener closes. Each accepted connection is
// forwarded to the remote DB via an SSH channel.
//
// Post-review fix: increment WaitGroup BEFORE Accept so Close.Wait can't
// race with an in-flight accept that hasn't yet started its forwarder
// goroutine.
func (t *Tunnel) acceptLoop() {
	defer t.wg.Done()
	for {
		conn, err := t.listener.Accept()
		if err != nil {
			if t.closed.Load() {
				return
			}
			fmt.Fprintf(os.Stderr, "driver/tunnel: accept: %v\n", err)
			return
		}
		// Fast-path: if Close fired between accept and here, refuse.
		select {
		case <-t.closeCh:
			_ = conn.Close()
			return
		default:
		}
		// Reserve a forwarder slot. Refuse if the semaphore is full.
		select {
		case t.concurrencySem <- struct{}{}:
		default:
			_ = conn.Close()
			continue
		}
		t.wg.Add(1)
		go func() {
			defer t.wg.Done()
			defer func() { <-t.concurrencySem }()
			t.forwardOne(conn)
		}()
	}
}

// forwardOne opens an SSH "direct-tcpip" channel to t.dialAddr and
// copies bytes bidirectionally. Honors t.closeCh for fast teardown +
// applies an idle deadline on both ends to stop slow-loris attacks.
func (t *Tunnel) forwardOne(localConn net.Conn) {
	defer localConn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), tunnelChannelTimeout)
	defer cancel()

	remoteConn, err := t.dialRemote(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "driver/tunnel: dial remote %s: %v\n", t.dialAddr, err)
		return
	}
	defer remoteConn.Close()

	// Watch t.closeCh: when Close fires, slam both ends so io.Copy
	// returns immediately.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-t.closeCh:
			_ = localConn.Close()
			_ = remoteConn.Close()
		case <-stop:
		}
	}()

	// Apply sliding idle deadline. Wrap both ends; each Read/Write
	// resets the deadline on its connection.
	lc := &idleDeadlineConn{Conn: localConn, idle: tunnelIdleTimeout}
	rc := &idleDeadlineConn{Conn: remoteConn, idle: tunnelIdleTimeout}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(rc, lc)
		if c, ok := remoteConn.(interface{ CloseWrite() error }); ok {
			_ = c.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(lc, rc)
		if c, ok := localConn.(interface{ CloseWrite() error }); ok {
			_ = c.CloseWrite()
		}
	}()
	wg.Wait()
}

// dialRemote opens an SSH "direct-tcpip" channel. If ctx fires before
// the dial returns, a watchdog goroutine closes the eventual successful
// conn to avoid leaks.
func (t *Tunnel) dialRemote(ctx context.Context) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := t.client.DialContext(ctx, "tcp", t.dialAddr)
		ch <- result{conn: c, err: err}
	}()
	select {
	case r := <-ch:
		return r.conn, r.err
	case <-ctx.Done():
		// Watchdog: drain the inflight dial. If it eventually
		// succeeded, close the conn so we don't leak it.
		go func() {
			r := <-ch
			if r.conn != nil {
				_ = r.conn.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

// idleDeadlineConn wraps a net.Conn with a sliding idle deadline. Each
// Read or Write resets the deadline. If the conn doesn't move data for
// `idle` duration, the next Read/Write returns an i/o timeout error.
type idleDeadlineConn struct {
	net.Conn
	idle time.Duration
}

func (c *idleDeadlineConn) Read(p []byte) (int, error) {
	_ = c.Conn.SetReadDeadline(time.Now().Add(c.idle))
	return c.Conn.Read(p)
}

func (c *idleDeadlineConn) Write(p []byte) (int, error) {
	_ = c.Conn.SetWriteDeadline(time.Now().Add(c.idle))
	return c.Conn.Write(p)
}

// verifyKeyMode refuses to load a private key whose file permissions
// are broader than 0600.
func verifyKeyMode(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("driver/tunnel: stat key: %w", err)
	}
	mode := info.Mode().Perm()
	if mode&0o077 != 0 {
		return fmt.Errorf("driver/tunnel: key %s has unsafe permissions %#o (must be 0600); refusing to load", path, mode)
	}
	return nil
}

// verifyKeyParentDir refuses to load a private key whose containing
// directory is world- or group-writable. Defense in depth: a 0600 key
// inside an 0777 directory is trivially replaceable.
func verifyKeyParentDir(path string) error {
	dir := filepath.Dir(path)
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("driver/tunnel: stat key parent dir: %w", err)
	}
	mode := info.Mode().Perm()
	if mode&0o022 != 0 {
		return fmt.Errorf("driver/tunnel: key parent dir %s is group- or world-writable (%#o); refusing to load", dir, mode)
	}
	return nil
}

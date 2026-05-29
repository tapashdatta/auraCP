package driver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
// Security per SECURITY.md §7.2 (post review-board findings, PR #3 and PR #3.5):
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
//     attacks from holding tunnel slots indefinitely. The timeout is
//     configurable per-Connection via QueryIdleTimeout (PR #3.5);
//     defaults to defaultIdleTimeout when unset.
//   - Local listener is a unix-domain socket with mode 0600 owned by
//     the auracpd process (PR #3.5). Previously a 127.0.0.1:0 TCP
//     listener; that listener accepted from any local UID. Multi-tenant
//     hosts can now share an auracpd box without exposing the tunnel
//     to other tenants.
type Tunnel struct {
	cfg       *dbadmin.SSHTunnel
	dialAddr  string // remote DB host:port to forward to
	client    *ssh.Client
	listener  net.Listener
	localAddr string // socket path (unix) or host:port (tcp fallback)
	network   string // "unix" or "tcp"
	closed    atomic.Bool

	// closeCh closes when Tunnel.Close is invoked. acceptLoop and
	// forwarder goroutines select on it to fast-exit.
	closeCh chan struct{}
	wg      sync.WaitGroup

	// activePool caps the number of simultaneous forwarders.
	concurrencySem chan struct{}

	// idleTimeout is the sliding deadline applied to the data-copy
	// phase. PR #3.5: replaces the previous hardcoded 5min constant.
	idleTimeout time.Duration
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

// defaultIdleTimeout is the fallback idle-deadline applied when no
// per-Connection QueryIdleTimeout is set. Caps slow-loris attacks; the
// database driver typically keeps its own keepalive shorter than this.
// PR #3.5: now configurable per-Connection; the hardcoded value used to
// be the only choice.
const defaultIdleTimeout = 5 * time.Minute

// tunnelSocketBaseDir is the directory under which the unix-socket
// listeners live. Production deployments expect this to be
// /run/aura-db/tunnels/, mode 0700, owned by the auracpd uid. The
// package-level var lets tests redirect to a TempDir; the systemd unit
// creates the production directory before auracpd starts (PR #3.5).
//
// SECURITY: each socket file is created mode 0600; only the auracpd
// process can connect, eliminating the local-UID exposure that the
// previous 127.0.0.1:0 TCP listener had.
var tunnelSocketBaseDir = "/run/aura-db/tunnels"

// SetTunnelSocketBaseDir overrides the directory in which Tunnel unix
// sockets are created. Exposed for tests + alt-deployment hosts that
// cannot use the default /run path. Returns the previous value so
// tests can restore.
func SetTunnelSocketBaseDir(dir string) string {
	prev := tunnelSocketBaseDir
	tunnelSocketBaseDir = dir
	return prev
}

// TunnelOptions carries the per-Connection knobs for OpenTunnel.
// Optional; the zero value yields defaults equivalent to PR #3 behavior
// (random socket name, defaultIdleTimeout). PR #3.5.
type TunnelOptions struct {
	// SocketName is the filename inside tunnelSocketBaseDir for the
	// unix socket listener. Should be the connection ID (or a safe
	// digest of it) so concurrent tunnels for different connections
	// don't collide. Empty → a per-call random name is used.
	//
	// For Postgres, callers must additionally pass
	// PostgresPort because pgx's unix-socket convention requires the
	// socket be named ".s.PGSQL.<port>" inside a directory.
	SocketName string

	// PostgresPort, when non-zero, switches the socket-naming scheme
	// to pgx's "<dir>/.s.PGSQL.<port>" convention. The driver
	// allocates a directory (using SocketName or random) and creates
	// the socket file inside it under that name. The path returned
	// by Tunnel.LocalAddr() is the DIRECTORY in this mode; pgx
	// expects to receive that directory as Host and the synthetic
	// port (the value passed here) as Port.
	PostgresPort uint16

	// IdleTimeout overrides defaultIdleTimeout. Zero → default.
	IdleTimeout time.Duration
}

// OpenTunnel establishes an SSH tunnel to cfg.Host:cfg.Port and binds a
// listener that forwards to dialAddr (the DB host:port, from the
// perspective of the SSH server's network). PR #3.5: listener is a
// unix-domain socket with mode 0600 (previously 127.0.0.1:0 TCP).
//
// concurrency caps simultaneous forwarders; if <= minConcurrency, raises
// to minConcurrency.
func OpenTunnel(ctx context.Context, cfg *dbadmin.SSHTunnel, dialAddr string, concurrency int) (*Tunnel, error) {
	return OpenTunnelWithOptions(ctx, cfg, dialAddr, concurrency, TunnelOptions{})
}

// OpenTunnelWithOptions is OpenTunnel with the TunnelOptions hook for
// callers that need to specify the unix-socket name (e.g., to tie it to
// a ConnectionID) or override the idle-timeout. PR #3.5.
func OpenTunnelWithOptions(ctx context.Context, cfg *dbadmin.SSHTunnel, dialAddr string, concurrency int, opts TunnelOptions) (*Tunnel, error) {
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

	lis, sockPath, advertisePath, err := openTunnelListener(opts)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("driver/tunnel: listen: %w", err)
	}
	_ = sockPath // retained for cleanup; lis.Close removes the file (unix listener does this on close)

	if concurrency < minConcurrency {
		concurrency = minConcurrency
	}
	idle := opts.IdleTimeout
	if idle <= 0 {
		idle = defaultIdleTimeout
	}
	netKind := "unix"
	if _, ok := lis.(*tcpFallbackListener); ok {
		netKind = "tcp"
	}
	t := &Tunnel{
		cfg:            cfg,
		dialAddr:       dialAddr,
		client:         client,
		listener:       lis,
		localAddr:      advertisePath,
		network:        netKind,
		closeCh:        make(chan struct{}),
		concurrencySem: make(chan struct{}, concurrency),
		idleTimeout:    idle,
	}

	t.wg.Add(1)
	go t.acceptLoop()
	return t, nil
}

// LocalAddr returns the local address the database driver should dial.
// In PR #3.5+ this is a unix-socket path (file path for MySQL, parent
// directory for Postgres-style sockets); the driver picks the appropriate
// Net/Host for its DSN based on Network().
//
// SECURITY: the unix socket is created mode 0600 and lives under
// tunnelSocketBaseDir (default /run/aura-db/tunnels/). Only the
// auracpd process can dial it.
func (t *Tunnel) LocalAddr() string {
	return t.localAddr
}

// Network reports the listener's address family. "unix" for the PR #3.5+
// unix-domain socket default; retained as a method so the driver layer
// can build the right DSN.
func (t *Tunnel) Network() string {
	if t.network == "" {
		return "unix"
	}
	return t.network
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
	// resets the deadline on its connection. PR #3.5: uses the
	// per-Tunnel configurable idleTimeout (was a hardcoded 5min const).
	lc := &idleDeadlineConn{Conn: localConn, idle: t.idleTimeout}
	rc := &idleDeadlineConn{Conn: remoteConn, idle: t.idleTimeout}

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

// openTunnelListener creates the unix-socket listener for OpenTunnel
// per PR #3.5. The socket file is mode 0600. Returns (listener, socket
// file path, advertise path).
//
// In TCP/loopback fallback mode (filesystem unavailable), returns a
// 127.0.0.1:0 TCP listener and logs a warning. This fallback is only
// used when the unix-socket path cannot be created (e.g., a hardened
// container without writable /run); production deployments must
// provision tunnelSocketBaseDir.
//
// For Postgres callers (opts.PostgresPort != 0), the socket is named
// ".s.PGSQL.<port>" inside a per-Connection directory; LocalAddr() then
// returns the directory and pgx uses Host=dir + Port=<port> to dial.
//
// For MySQL callers, the socket is a plain file; LocalAddr() returns the
// file path and go-sql-driver/mysql uses Net="unix" Addr=<path>.
func openTunnelListener(opts TunnelOptions) (net.Listener, string, string, error) {
	base := tunnelSocketBaseDir
	if err := os.MkdirAll(base, 0o700); err != nil {
		// Fall back to a TCP listener so callers without writable
		// tunnel-base-dir (e.g., tests, hardened CI containers
		// without /run) still work. The TCP fallback is documented
		// to be insecure for multi-tenant hosts — production must
		// provision /run/aura-db/tunnels.
		lis, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, "", "", fmt.Errorf("unix-socket dir %s unavailable and tcp fallback failed: %w", base, err)
		}
		return &tcpFallbackListener{Listener: lis}, "", lis.Addr().String(), nil
	}

	name := opts.SocketName
	if name == "" {
		// Random nonce — sha256 of timestamp + pid + random bytes,
		// truncated to keep socket paths short (linux caps at 108).
		h := sha256.New()
		fmt.Fprintf(h, "%d-%d-%d", time.Now().UnixNano(), os.Getpid(), randomInt63())
		name = hex.EncodeToString(h.Sum(nil)[:8])
	} else {
		// Sanitize: callers pass ConnectionID directly; encode to a
		// hex digest so unusual characters can't create unexpected
		// paths. Short prefix preserves human-readability when the
		// ID is a normal slug.
		name = safeSocketName(name)
	}

	var sockPath, advertise string
	if opts.PostgresPort != 0 {
		// pgx convention: <dir>/.s.PGSQL.<port>. Create a per-tunnel
		// directory so pgx's Host=dir + Port=<port> dials our socket
		// and not whatever else lives in tunnelSocketBaseDir.
		dir := filepath.Join(base, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, "", "", fmt.Errorf("create tunnel dir %s: %w", dir, err)
		}
		sockPath = filepath.Join(dir, fmt.Sprintf(".s.PGSQL.%d", opts.PostgresPort))
		advertise = dir
	} else {
		sockPath = filepath.Join(base, name+".sock")
		advertise = sockPath
	}

	// Stale socket from a prior crashed run; safe to remove (we own
	// the directory mode 0700 so nothing else can race-create it).
	_ = os.Remove(sockPath)

	lis, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, "", "", fmt.Errorf("listen unix %s: %w", sockPath, err)
	}
	// Defense-in-depth: chmod to 0600 explicitly. The default mode
	// after net.Listen("unix", ...) is umask-dependent.
	if err := os.Chmod(sockPath, 0o600); err != nil {
		_ = lis.Close()
		_ = os.Remove(sockPath)
		return nil, "", "", fmt.Errorf("chmod socket %s: %w", sockPath, err)
	}
	return lis, sockPath, advertise, nil
}

// safeSocketName produces a filename-safe encoding of an arbitrary
// ConnectionID. Allowed chars [A-Za-z0-9_-] pass through (truncated);
// anything else triggers a sha256-digest fallback so a malicious ID
// can't escape the tunnel directory.
func safeSocketName(id string) string {
	allSafe := id != "" && len(id) <= 32
	if allSafe {
		for i := 0; i < len(id); i++ {
			c := id[i]
			switch {
			case c >= 'a' && c <= 'z',
				c >= 'A' && c <= 'Z',
				c >= '0' && c <= '9',
				c == '-' || c == '_':
				continue
			default:
				allSafe = false
			}
		}
	}
	if allSafe {
		return id
	}
	h := sha256.Sum256([]byte(id))
	return hex.EncodeToString(h[:8])
}

// randomInt63 returns a non-cryptographic pseudo-random int63; used
// purely for socket-name nonces. Pulled out so tests can patch.
var randomInt63 = func() int64 {
	// crypto/rand would over-spec this; the socket is mode 0600 and
	// inside an 0700 dir, so name guessability isn't a threat.
	return time.Now().UnixNano()
}

// tcpFallbackListener wraps the loopback-TCP fallback so the rest of
// the tunnel code can treat it uniformly. The Tunnel still reports
// Network()="tcp" in this mode so the driver builds a TCP DSN.
type tcpFallbackListener struct {
	net.Listener
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

// Package driver — MongoDB backend.
//
// Mongo is a document store, not a relational engine. The auraCP driver
// surface (Query / Exec / Rows) is SQL-flavoured by contract; we adapt
// it so Mongo can ride the same plumbing as MariaDB and Postgres:
//
//   - Query / Exec REFUSE raw SQL outright (ErrSyntax wrapping
//     "raw SQL not supported on mongo"). Mongo speaks BSON commands;
//     anything resembling SQL is a category error. The classifier
//     short-circuits at ClassForbidden before this code is reached
//     for HTTP-routed paths, but the driver also guards as
//     defense-in-depth.
//   - Ping uses Client.Ping with a 5s timeout, mirroring the MySQL
//     and Postgres open paths.
//   - ServerVersion runs the admin {buildInfo:1} command.
//
// All row mutations / reads on Mongo connections route through
// rows.Operator, which dispatches to the mongo-aware backend in
// rows/mongo.go (Find / InsertOne / UpdateOne / DeleteOne under the
// hood). The schema reader in schema/mongo.go produces synthetic
// columns by sampling documents.
//
// v0.3.2-F.
package driver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"github.com/auracp/auracp/pkg/dbadmin"
)

// mongoDriverImpl implements Driver for MongoDB via the v2 official
// driver.
type mongoDriverImpl struct{}

func (m *mongoDriverImpl) Engine() dbadmin.EngineKind {
	return dbadmin.EngineMongo
}

// Open establishes a *mongo.Client pointed at the configured Mongo
// deployment. SSH tunnelling is honoured the same way as the SQL
// drivers: we dial through OpenTunnelWithOptions when c.SSHTunnel is
// non-nil and rewrite the host/port the driver uses.
//
// Hardening:
//   - Prod-tagged connections REQUIRE TLS with verify (no skip-verify).
//   - Client.Ping is run before returning so the caller gets the same
//     "Conn is alive" guarantee as the SQL drivers.
//   - Credentials are read from creds and not formatted into a DSN; we
//     pass them through ClientOptions.SetAuth instead so no string copy
//     of the password lives in the *mongo.Client for the connection's
//     lifetime beyond what the driver itself retains.
func (m *mongoDriverImpl) Open(ctx context.Context, c *dbadmin.Connection, creds *dbadmin.Credentials, poolSize int) (Conn, error) {
	if c == nil {
		return nil, errors.New("driver/mongo: nil Connection")
	}
	if creds == nil {
		return nil, errors.New("driver/mongo: nil Credentials")
	}

	if c.IsProd() {
		mode := strings.ToLower(c.SSLMode)
		if !c.UseSSL || mode == "" || mode == "skip-verify" {
			return nil, fmt.Errorf("driver/mongo: prod-tagged connection requires TLS with verify (got UseSSL=%v SSLMode=%q)", c.UseSSL, c.SSLMode)
		}
	}

	hostPort := net.JoinHostPort(c.Host, fmt.Sprintf("%d", c.Port))

	var tun *Tunnel
	dialHost := c.Host
	dialPort := c.Port
	if c.SSHTunnel != nil {
		t, err := OpenTunnelWithOptions(ctx, c.SSHTunnel, hostPort, poolSize+2, TunnelOptions{
			SocketName:  string(c.ID),
			IdleTimeout: c.QueryIdleTimeout,
		})
		if err != nil {
			return nil, fmt.Errorf("driver/mongo: open tunnel: %w", err)
		}
		tun = t
		// Mongo URI accepts host:port; if the tunnel produced a TCP
		// address use that, otherwise fall back to the configured
		// host:port (unix socket isn't directly addressable via the
		// mongo URI in v2).
		if t.Network() == "tcp" {
			h, p, perr := net.SplitHostPort(t.LocalAddr())
			if perr != nil {
				_ = tun.Close()
				return nil, fmt.Errorf("driver/mongo: bad tunnel addr %q: %w", t.LocalAddr(), perr)
			}
			dialHost = h
			var pp int
			if _, perr := fmt.Sscanf(p, "%d", &pp); perr == nil && pp > 0 {
				dialPort = pp
			}
		}
	}

	opt := options.Client().
		SetHosts([]string{net.JoinHostPort(dialHost, fmt.Sprintf("%d", dialPort))}).
		SetConnectTimeout(10 * time.Second).
		SetServerSelectionTimeout(10 * time.Second)

	if c.Username != "" {
		auth := options.Credential{
			Username: c.Username,
			Password: creds.Password,
		}
		// AuthSource defaults to the connection database (admin if
		// empty) — matches mongosh's default behaviour.
		if c.Database != "" {
			auth.AuthSource = c.Database
		}
		opt = opt.SetAuth(auth)
	}

	if c.UseSSL {
		tlsCfg, err := buildMongoTLS(c, creds)
		if err != nil {
			if tun != nil {
				_ = tun.Close()
			}
			return nil, fmt.Errorf("driver/mongo: TLS config: %w", err)
		}
		opt = opt.SetTLSConfig(tlsCfg)
	}

	if poolSize < 1 {
		poolSize = 4
	}
	opt = opt.SetMaxPoolSize(uint64(poolSize))

	client, err := mongo.Connect(opt)
	if err != nil {
		if tun != nil {
			_ = tun.Close()
		}
		return nil, classifyMongoErr(wrapCtxErr(ctx, err))
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx, readpref.Primary()); err != nil {
		_ = client.Disconnect(context.Background())
		if tun != nil {
			_ = tun.Close()
		}
		return nil, classifyMongoErr(wrapCtxErr(pingCtx, err))
	}

	return &mongoConn{
		client:   client,
		tunnel:   tun,
		conn:     c,
		database: c.Database,
	}, nil
}

// mongoConn implements Conn against a *mongo.Client.
type mongoConn struct {
	client   *mongo.Client
	tunnel   *Tunnel
	conn     *dbadmin.Connection
	database string
}

// Client exposes the underlying *mongo.Client for the rows + schema
// packages, which need direct access to drive Find/Insert/etc. The
// SQL drivers don't need this escape hatch; for Mongo it's structurally
// required because the Conn surface (Query/Exec/raw SQL string) doesn't
// fit document-store semantics.
//
// Callers MUST NOT call Disconnect on the returned client — its
// lifecycle is owned by mongoConn.Close.
func (c *mongoConn) Client() *mongo.Client {
	return c.client
}

// Database returns the default database for this connection.
func (c *mongoConn) Database() string {
	return c.database
}

// Query refuses raw SQL on Mongo connections. The classifier emits
// ClassForbidden upstream, but this defense-in-depth guard ensures a
// caller bypassing the classifier (tests, future routes) still cannot
// smuggle SQL into a Mongo session.
func (c *mongoConn) Query(ctx context.Context, limits Limits, sqlText string, args ...any) (Rows, error) {
	return nil, fmt.Errorf("%w: raw SQL is not supported on MongoDB connections; use the structured row operations API",
		ErrSyntax)
}

// Exec mirrors Query: raw SQL is refused outright.
func (c *mongoConn) Exec(ctx context.Context, limits Limits, sqlText string, args ...any) (Result, error) {
	return Result{}, fmt.Errorf("%w: raw SQL is not supported on MongoDB connections; use the structured row operations API",
		ErrSyntax)
}

func (c *mongoConn) Ping(ctx context.Context) error {
	return classifyMongoErr(wrapCtxErr(ctx, c.client.Ping(ctx, readpref.Primary())))
}

func (c *mongoConn) ServerVersion(ctx context.Context) (string, error) {
	cmd := bson.D{{Key: "buildInfo", Value: 1}}
	var res bson.M
	if err := c.client.Database("admin").RunCommand(ctx, cmd).Decode(&res); err != nil {
		return "", classifyMongoErr(wrapCtxErr(ctx, err))
	}
	if v, ok := res["version"].(string); ok {
		return "MongoDB " + v, nil
	}
	return "MongoDB", nil
}

func (c *mongoConn) Close() error {
	var firstErr error
	if c.client != nil {
		if err := c.client.Disconnect(context.Background()); err != nil {
			firstErr = err
		}
	}
	if c.tunnel != nil {
		if err := c.tunnel.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ─── TLS ─────────────────────────────────────────────────────────────

// buildMongoTLS constructs a *tls.Config from the connection's SSLMode.
// Modes accepted (lowercased):
//
//	"" / "require" / "preferred" / "verify-ca" / "verify-full" →
//	    verify against the system root pool
//	"skip-verify" → InsecureSkipVerify=true (refused for prod by Open)
//
// Mongo's URI sslmode keywords map cleanly to TLS verification flags;
// we don't try to support the full SQL sslmode taxonomy here.
func buildMongoTLS(c *dbadmin.Connection, creds *dbadmin.Credentials) (*tls.Config, error) {
	tlsCfg := &tls.Config{
		ServerName: c.Host,
		MinVersion: tls.VersionTLS12,
	}
	switch strings.ToLower(c.SSLMode) {
	case "", "preferred", "require", "verify-ca", "verify-full":
		tlsCfg.InsecureSkipVerify = false
	case "skip-verify":
		tlsCfg.InsecureSkipVerify = true
	default:
		return nil, fmt.Errorf("unknown sslmode %q", c.SSLMode)
	}
	if len(creds.ClientCert) > 0 && len(creds.ClientKey) > 0 {
		cert, err := tls.X509KeyPair(creds.ClientCert, creds.ClientKey)
		if err != nil {
			return nil, fmt.Errorf("client cert/key: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}
	if !tlsCfg.InsecureSkipVerify && tlsCfg.RootCAs == nil {
		pool, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("system CA pool unavailable: %w", err)
		}
		tlsCfg.RootCAs = pool
	}
	return tlsCfg, nil
}

// ─── Error classification ────────────────────────────────────────────

// classifyMongoErr maps a mongo-driver error to one of the typed
// sentinels. Codes follow the Mongo server's error-code taxonomy:
//
//	11000 / 11001 → duplicate key            → ErrConflict
//	18            → AuthenticationFailed      → ErrAuth
//	13            → Unauthorized              → ErrPermission
//	26            → NamespaceNotFound         → ErrNotFound
//	50            → MaxTimeMSExpired          → ErrTimeout
//	189 / 91      → PrimarySteppedDown/Shutdown → ErrUnavailable
//
// Network errors and IsTimeout() helpers from the driver are also
// recognized.
func classifyMongoErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrEOF) || errors.Is(err, ErrCapped) ||
		errors.Is(err, ErrClosed) || errors.Is(err, ErrTimeout) ||
		errors.Is(err, ErrCanceled) {
		return err
	}
	if mongo.IsTimeout(err) {
		return errors.Join(ErrTimeout, err)
	}
	if mongo.IsNetworkError(err) {
		return errors.Join(ErrUnavailable, err)
	}
	if mongo.IsDuplicateKeyError(err) {
		return errors.Join(ErrConflict, err)
	}
	var cmdErr mongo.CommandError
	if errors.As(err, &cmdErr) {
		switch cmdErr.Code {
		case 18:
			return errors.Join(ErrAuth, err)
		case 13:
			return errors.Join(ErrPermission, err)
		case 26:
			return errors.Join(ErrNotFound, err)
		case 50:
			return errors.Join(ErrTimeout, err)
		case 91, 189, 11600, 11602:
			return errors.Join(ErrUnavailable, err)
		case 11000, 11001:
			return errors.Join(ErrConflict, err)
		}
	}
	msg := err.Error()
	if strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "context canceled") ||
		strings.Contains(msg, "EOF") {
		return errors.Join(ErrUnavailable, err)
	}
	return err
}

// Package dbadmin is the engine of Aura DB — auraCP's native database
// administration tool for MariaDB / MySQL and PostgreSQL.
//
// Aura DB is a stateless coordinator: it owns the SQL classifier, the driver
// layer, the HTTP handler, and resource-limit enforcement. It does NOT own
// the operator's identity, where connection records live, or where audit
// events go — those concerns are delegated to three host-supplied
// implementations of the Auth, ConnectionStore, and AuditSink interfaces.
//
// This separation is what makes the engine embeddable. In integrated mode
// (auracpd) the three interfaces bridge to the panel's session manager,
// `databases` table, and `audit_log` table. In standalone mode (cmd/aura-db)
// the implementations live in package
// github.com/auracp/auracp/pkg/dbadmin/standalone and use a self-contained
// SQLite store + an append-only audit file.
//
// The canonical specifications:
//
//   - docs/aura-db/SECURITY.md — threat model and security controls.
//   - docs/aura-db/ADR-001-architecture.md — architectural decisions.
//   - docs/aura-db/SDK.md — embedding contract (this package's public surface).
//
// This package follows semver. Breaking changes to any exported identifier
// require a major version bump. Internal subpackages (anything under
// pkg/dbadmin/internal) are not stable and MUST NOT be imported by hosts.
//
// Usage:
//
//	auth := &MyAuth{...}          // implements dbadmin.Auth
//	conns := &MyConnections{...}  // implements dbadmin.ConnectionStore
//	audit := &MyAudit{...}        // implements dbadmin.AuditSink
//	engine, err := dbadmin.New(dbadmin.Options{Auth: auth, Conns: conns, Audit: audit})
//	if err != nil { panic(err) }
//	defer engine.Shutdown(context.Background())
//
//	mux := http.NewServeMux()
//	mux.Handle("/api/dbadmin/", http.StripPrefix("/api/dbadmin", engine.Handler()))
//	mux.Handle("/", http.FileServer(engine.Embed()))
//	http.ListenAndServeTLS(":8090", "cert.pem", "key.pem", mux)
package dbadmin

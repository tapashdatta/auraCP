// Package dbadmintest provides in-memory implementations of the
// dbadmin.Auth, dbadmin.ConnectionStore, and dbadmin.AuditSink interfaces
// for use in tests.
//
// All three types follow a builder API so tests can compose realistic
// fixtures inline:
//
//	auth := dbadmintest.NewAuth().
//	    WithUser("alice", "alice@example").
//	    WithGrant("alice", "conn-1", dbadmin.RoleOwner).
//	    WithStepUpVerified("alice", dbadmin.ActionQueryDDL)
//
//	conns := dbadmintest.NewConnections().
//	    WithConnection(dbadmin.Connection{
//	        ID:     "conn-1",
//	        Name:   "test-db",
//	        Engine: dbadmin.EngineMariaDB,
//	        Host:   "localhost",
//	        Port:   3306,
//	    }, dbadmin.Credentials{Password: "secret"})
//
//	audit := dbadmintest.NewAudit()
//
//	engine, _ := dbadmin.New(dbadmin.Options{
//	    Auth: auth, Conns: conns, Audit: audit,
//	})
//	// drive engine.Handler() via httptest, then:
//	events := audit.Events()
//	// assert specific events were recorded
//
// The implementations are stability-stable along with the rest of the
// SDK — tests that depend on dbadmintest may be carried forward across
// engine versions without breakage. Internal package layout may change.
//
// These types are NOT safe for use as production implementations. They
// keep everything in-memory, do not persist across restarts, and do not
// enforce any of the security controls that the standalone or panel
// implementations enforce. Use them only in test code.
package dbadmintest

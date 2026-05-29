package dbadmin

import "context"

// ConnectionStore owns connection metadata and credentials. Implementations
// bridge to the host's persistence layer:
//
//   - In integrated mode, the implementation joins the panel's `databases`
//     table with a new `dbadmin_grants` table for per-(user, connection,
//     role) authority.
//   - In standalone mode, the implementation uses pkg/dbadmin/standalone's
//     SQLite store with AES-256-GCM-encrypted credentials.
//
// The engine never sees encryption keys or raw credential blobs; it asks
// the store for credentials and receives them already decrypted, then
// zeros the returned struct after use.
//
// Listing contract:
//
//   - List MUST return only connections the user has at least one grant
//     on. Filtering by grant is the store's responsibility; the engine
//     does NOT filter the returned slice. Returning extra connections
//     would let an unauthorized user enumerate them.
//
//   - List returns an empty slice (not nil) when the user has no grants.
//
// Get / Delete contract:
//
//   - The engine has already authorized the action via Auth.HasPermission
//     before calling these methods. Implementations do NOT re-check
//     authorization; they simply enforce existence + ownership.
//
//   - Get returns ErrNotFound if the connection does not exist OR if the
//     implementation considers it out-of-tenant. The engine maps both to
//     HTTP 404 indistinguishably.
//
// Save contract:
//
//   - If Connection.ID is empty, a new ID is minted and returned. The
//     engine never invents IDs; the store is the canonical source.
//
//   - If Connection.ID is set, the existing record is updated. The
//     engine has authorized the update via ActionConnUpdate before
//     invoking Save.
//
//   - Credentials are passed separately from Connection so the store can
//     decide encryption details without exposing the encrypted blob shape
//     to the engine.
//
// Credentials contract:
//
//   - The Credentials returned MUST be safe to use immediately and to
//     pass to the driver layer.
//
//   - The engine calls Credentials.Zero() on the returned struct after
//     use. Implementations that cache decrypted credentials internally
//     (for connection-pool reuse) MUST return a fresh copy each time, so
//     Zero on the engine's reference doesn't corrupt the cache.
type ConnectionStore interface {
	// List returns connections the user has any grant on.
	//
	// Filtering by grant is mandatory: a user without any RoleViewer-
	// or-higher grant on a connection MUST NOT see it in the result.
	// The engine does not re-filter; over-broad results leak connection
	// existence to unauthorized users.
	List(context.Context, User) ([]Connection, error)

	// Get fetches a single connection's metadata.
	//
	// Returns ErrNotFound for nonexistent connections AND for
	// connections the implementation considers out-of-tenant. The
	// engine maps both to HTTP 404, matching the "404 for forbidden
	// connection-scoped resources" rule (SECURITY.md §10.3).
	Get(context.Context, ConnectionID) (Connection, error)

	// Credentials returns decrypted credentials for a connection.
	//
	// The engine calls Credentials.Zero() on the returned struct when
	// done. Implementations may cache decrypted material internally
	// for pool reuse, but MUST return a defensive copy so the engine's
	// Zero does not corrupt the cache.
	Credentials(context.Context, ConnectionID) (Credentials, error)

	// Save creates or updates a connection.
	//
	// If Connection.ID is empty, the store mints a new ID and returns
	// it. The engine treats the returned ID as authoritative and
	// stamps it on subsequent operations.
	//
	// If Connection.ID is set, the store updates the existing record.
	// Returns ErrNotFound if the ID does not correspond to an existing
	// connection in the store.
	//
	// The store is responsible for encrypting Credentials before
	// persistence. The engine never sees the encrypted form.
	Save(context.Context, Connection, Credentials) (ConnectionID, error)

	// Delete removes a connection.
	//
	// Returns ErrNotFound if the connection does not exist. The engine
	// emits an audit event before Delete is called; if Delete fails,
	// the engine emits an outcome event recording the failure.
	//
	// Implementations should cascade-delete connection-scoped state
	// (grants, query history rows, saved queries) within the same
	// transaction. If they cannot cascade atomically, they MUST leave
	// the connection intact and return an error rather than producing
	// dangling sub-records.
	Delete(context.Context, ConnectionID) error
}

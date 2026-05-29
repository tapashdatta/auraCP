package dbadmin

import "time"

// User is the operator identity returned by Auth.Authenticate. The engine
// treats it as opaque except for Roles, which is consulted by the default
// permission helpers. Hosts may attach arbitrary additional context via
// Attrs (session ID, IP class, UA hash, etc.) — the engine round-trips
// these into audit events without interpreting them.
type User struct {
	// ID is unique within the host's auth system. Empty means
	// unauthenticated; the engine refuses to invoke any action for an
	// empty-ID user even if Auth.HasPermission would return true.
	ID string

	// Username is for display + audit. Not used for matching; do not
	// rely on it for authorization decisions.
	Username string

	// Roles is the user's role on each connection they have any grant
	// for. Connections the user has no grant for MUST be omitted (not
	// present at the key); this is structurally how we prevent
	// existence leaks via the connection list.
	Roles map[ConnectionID]Role

	// Attrs carries arbitrary host-supplied metadata. The engine writes
	// recognized keys into audit events:
	//   - "ip_class"   → recorded as the source-IP class
	//   - "ua_hash"    → recorded as the UA hash
	//   - "session_id" → not audited; used internally for step-up flag
	//                    lookup if the Auth impl wants to key by it
	Attrs map[string]string
}

// ConnectionID is an opaque identifier minted by ConnectionStore.Save.
// The engine never parses it; it round-trips between host and engine.
type ConnectionID string

// Role grants authority on one connection. Strictly ordered: a higher
// numeric value subsumes every lower one. The engine compares with
// Action.MinRole() and never short-circuits any other way.
type Role uint8

const (
	// RoleNone — no access. Default for any (user, connection) pair
	// that has no explicit grant. Equivalent to the connection not
	// existing from the user's perspective.
	RoleNone Role = iota

	// RoleViewer — can list the connection and inspect its schema
	// metadata, but cannot read row data.
	RoleViewer

	// RoleAnalyst — schema + row reads. Cannot write or run DDL.
	RoleAnalyst

	// RoleWriter — row reads + parameterized row writes (with WHERE).
	// Cannot run DDL or dangerous operations. write-row-mass (DELETE/
	// UPDATE without WHERE) is allowed only with step-up.
	RoleWriter

	// RoleDBA — everything RoleWriter has, plus DDL (with step-up on
	// DROP / TRUNCATE / ALTER per SECURITY.md §5.5).
	RoleDBA

	// RoleOwner — everything RoleDBA has, plus per-connection user
	// management (granting/revoking roles to other panel users) and
	// per-connection settings (tag changes, SSL mode, etc.).
	RoleOwner
)

// String returns the canonical lowercased name for a role. Useful in
// audit events and API payloads.
func (r Role) String() string {
	switch r {
	case RoleViewer:
		return "viewer"
	case RoleAnalyst:
		return "analyst"
	case RoleWriter:
		return "writer"
	case RoleDBA:
		return "dba"
	case RoleOwner:
		return "owner"
	default:
		return "none"
	}
}

// Action enumerates every operation HasPermission may be asked to authorize.
// Action strings are semver-stable; new actions are additive.
type Action string

const (
	ActionConnList       Action = "conn.list"
	ActionConnView       Action = "conn.view"
	ActionConnCreate     Action = "conn.create"
	ActionConnUpdate     Action = "conn.update"
	ActionConnDelete     Action = "conn.delete"
	ActionConnPwdView    Action = "conn.password.view"
	ActionConnGrantMgmt  Action = "conn.grants"
	ActionSchemaRead     Action = "schema.read"
	ActionRowRead        Action = "row.read"
	ActionRowWrite       Action = "row.write"
	ActionQueryRead      Action = "query.read"
	ActionQueryWrite     Action = "query.write"
	ActionQueryDDL       Action = "query.ddl"
	ActionQueryDangerous Action = "query.dangerous"
	ActionExport         Action = "export"
	ActionImport         Action = "import"
	ActionRestore        Action = "backup.restore"
	ActionAuditRead      Action = "audit.read"
	ActionAuditConfig    Action = "audit.config"
)

// MinRole returns the minimum role required for an action under the default
// policy. Hosts implementing Auth.HasPermission MAY return false for an
// action the user nominally has the role for (extra restriction); they
// MUST NOT allow an action below the minimum role here.
//
// Returns RoleNone for unknown actions, which fails closed (no role is
// ≤ RoleNone except RoleNone itself, and RoleNone holders are unauthenticated).
func (a Action) MinRole() Role {
	switch a {
	case ActionConnList, ActionConnView, ActionSchemaRead:
		return RoleViewer
	case ActionRowRead, ActionQueryRead, ActionExport:
		return RoleAnalyst
	case ActionRowWrite, ActionQueryWrite, ActionImport:
		return RoleWriter
	case ActionQueryDDL, ActionRestore:
		return RoleDBA
	case ActionConnCreate, ActionConnUpdate, ActionConnDelete,
		ActionConnPwdView, ActionConnGrantMgmt,
		ActionQueryDangerous, ActionAuditConfig:
		return RoleOwner
	case ActionAuditRead:
		// Audit read is per-connection: viewers and above can see
		// their own actions on a connection.
		return RoleViewer
	default:
		return RoleNone
	}
}

// ActionClass groups Actions that share a step-up identity. A step-up
// flag minted for one Action authorizes any sibling Action in the same
// class for the lifetime of the flag. Without this grouping, a single
// step-up prompt would only authorize one tightly-scoped Action — so
// approving a single DDL statement would force a fresh TOTP prompt on
// the next DDL statement in the same workstation session, which is both
// hostile to operators and trains them to spam-approve prompts.
//
// PR #10.5 / FIX-SDK-3: stepUpStore is now keyed by class (+ session +
// connectionID for connection-scoped classes) rather than raw Action.
type ActionClass string

const (
	// ActionClassNone is the zero class for actions that do not require
	// step-up (the default). Keying by this class never authorizes
	// anything; HasSteppedUp short-circuits on it.
	ActionClassNone ActionClass = ""

	// ActionClassConnAdmin covers per-connection administrative
	// actions: viewing the stored password, updating connection
	// metadata, deleting a connection, managing grants. One step-up
	// authorizes any of these for the same connection.
	ActionClassConnAdmin ActionClass = "conn-admin"

	// ActionClassDDL covers data-definition statements (CREATE/ALTER/
	// DROP/TRUNCATE). One step-up authorizes any DDL statement on the
	// same connection within the step-up TTL.
	ActionClassDDL ActionClass = "ddl"

	// ActionClassDangerous covers unparameterized DELETE/UPDATE,
	// mass-mutation, and other unbounded write actions.
	ActionClassDangerous ActionClass = "dangerous"

	// ActionClassRestore covers backup restore, which mutates the
	// target connection's state catastrophically.
	ActionClassRestore ActionClass = "restore"

	// ActionClassAuditAdmin covers audit-configuration changes
	// (panel-global, not per-connection).
	ActionClassAuditAdmin ActionClass = "audit-admin"
)

// Class returns the step-up class for an Action. Actions in the same
// class share step-up flags. Returns ActionClassNone for actions that
// do not require step-up.
func (a Action) Class() ActionClass {
	switch a {
	case ActionConnPwdView, ActionConnUpdate, ActionConnDelete, ActionConnGrantMgmt:
		return ActionClassConnAdmin
	case ActionQueryDDL:
		return ActionClassDDL
	case ActionQueryDangerous:
		return ActionClassDangerous
	case ActionRestore:
		return ActionClassRestore
	case ActionAuditConfig:
		return ActionClassAuditAdmin
	default:
		return ActionClassNone
	}
}

// RequiresStepUp returns whether the default policy mandates a fresh MFA
// verification at the moment the action is performed. Hosts MAY require
// step-up for additional actions; they MUST NOT skip step-up for actions
// that return true here.
//
// See SECURITY.md §5.5 for the canonical table; this function is its code
// representation.
func (a Action) RequiresStepUp() bool {
	switch a {
	case ActionConnPwdView,
		ActionConnUpdate,
		ActionConnDelete,
		ActionConnGrantMgmt,
		ActionQueryDDL,
		ActionQueryDangerous,
		ActionRestore,
		ActionAuditConfig:
		return true
	default:
		return false
	}
}

// EngineKind identifies the target database product. Used by drivers,
// classifier, and schema readers to dispatch to engine-specific code.
type EngineKind uint8

const (
	// EngineUnknown is the zero value and is always rejected by the
	// engine. Hosts MUST set a real engine on every Connection.
	EngineUnknown EngineKind = iota

	// EngineMariaDB covers MariaDB and Oracle MySQL. Driver:
	// github.com/go-sql-driver/mysql. Classifier: vitess.io/vitess
	// /go/vt/sqlparser.
	EngineMariaDB

	// EnginePostgres covers PostgreSQL (and Postgres-protocol
	// compatibles like CockroachDB, with the caveat that classifier
	// rejects Postgres-specific extensions not in vanilla pg). Driver:
	// github.com/jackc/pgx/v5. Classifier:
	// github.com/pganalyze/pg_query_go/v5.
	EnginePostgres
)

// String returns the canonical lowercased name.
func (e EngineKind) String() string {
	switch e {
	case EngineMariaDB:
		return "mariadb"
	case EnginePostgres:
		return "postgres"
	default:
		return "unknown"
	}
}

// Tag is a connection-level policy decoration. See SECURITY.md §6.2.
type Tag string

const (
	// TagProd: TLS to the DB required, read-only by default for every
	// role (writes require step-up + audit reason), red strip in UI.
	TagProd Tag = "prod"

	// TagStaging: TLS to the DB required, standard role enforcement,
	// amber strip in UI.
	TagStaging Tag = "staging"

	// TagDev: no additional constraints, green strip in UI.
	TagDev Tag = "dev"

	// TagFourEye: dangerous actions queue for a second owner's
	// approval. Default 30-minute window; configurable per
	// connection.
	TagFourEye Tag = "4-eye"

	// TagReadOnly: hard-locks the connection to read-only operations.
	// Cannot be overridden by any role, even RoleOwner. Useful for
	// replica connections where writes would propagate badly.
	TagReadOnly Tag = "read-only"
)

// Origin records how a connection came to exist. The engine uses Origin
// to enforce lifecycle constraints — e.g., OriginPanelSite connections
// cannot be deleted from Aura DB directly; their lifecycle is owned by
// the panel's site machinery.
type Origin string

const (
	// OriginManual: created by an operator via the Aura DB UI.
	OriginManual Origin = "manual"

	// OriginPanelSite: auto-discovered from a panel-managed site's
	// database (integrated mode only). Cannot be deleted from Aura DB;
	// the panel removes it when the originating site is deleted.
	OriginPanelSite Origin = "panel-site"

	// OriginPanelImport: imported via aura-db import-config. Treated
	// like OriginManual once imported.
	OriginPanelImport Origin = "panel-import"
)

// Connection is the engine-visible metadata for one database. Credentials
// live separately in the Credentials type so they can be encrypted at
// rest without coupling the schema.
type Connection struct {
	ID        ConnectionID
	Name      string     // display name
	Engine    EngineKind // MariaDB or Postgres
	Host      string
	Port      int
	Database  string // default schema / database
	Username  string // owner display; the password is in Credentials
	Tags      []Tag
	UseSSL    bool
	SSLMode   string     // engine-specific: "require"/"verify-full"/etc.
	SSHTunnel *SSHTunnel // optional

	// QueryIdleTimeout caps how long an SSH-tunnel forwarded connection
	// may sit idle (no Read/Write) before the driver tears it down.
	// Used as the sliding deadline on idleDeadlineConn. Zero defers to
	// the engine policy (min(Config.Query.TimeoutMax, 5min)). Operators
	// with long-running queries that legitimately stall on a slow
	// backend can raise this per-connection; values exceeding
	// Config.Query.TimeoutMax are clamped by the engine.
	//
	// Added in PR #3.5 (driver hardening). See docs/aura-db/
	// KNOWN-ISSUES.md "tunnel: data-copy idle timeout enforcement".
	QueryIdleTimeout time.Duration

	CreatedAt time.Time
	UpdatedAt time.Time
	Owner     string // user ID who created it (for audit)
	Origin    Origin
}

// HasTag reports whether the connection carries the given tag.
func (c *Connection) HasTag(t Tag) bool {
	for _, x := range c.Tags {
		if x == t {
			return true
		}
	}
	return false
}

// IsReadOnly reports whether write operations are categorically forbidden
// on this connection (regardless of role). Currently means: tagged
// "read-only".
func (c *Connection) IsReadOnly() bool {
	return c.HasTag(TagReadOnly)
}

// IsProd reports whether the connection carries the "prod" tag, which
// triggers extra controls (forced TLS, step-up on writes, audit reason).
func (c *Connection) IsProd() bool {
	return c.HasTag(TagProd)
}

// Credentials carries the decrypted login material for one connection.
// ConnectionStore implementations decrypt at-rest data and return a
// Credentials struct; the engine zeroes the struct before garbage
// collection via the Zero method.
type Credentials struct {
	// Password is the plaintext password. Empty if the connection uses
	// only client-cert auth.
	Password string

	// ClientCert is an optional PEM-encoded client certificate (for
	// connections requiring mTLS to the database).
	ClientCert []byte

	// ClientKey is the matching private key in PEM form. Required if
	// ClientCert is set.
	ClientKey []byte
}

// Zero overwrites all credential bytes with zeros so a heap snapshot or
// post-GC memory inspection can't recover them. Best-effort: Go's GC may
// have moved the original backing array before Zero ran. We do it anyway
// because the worst case is harmless and the typical case improves.
func (c *Credentials) Zero() {
	if c == nil {
		return
	}
	if len(c.Password) > 0 {
		// Strings are immutable; rebuild as an empty string and let
		// the GC collect the original. There's no way to overwrite
		// a Go string's backing bytes from user code; we can only
		// drop the reference promptly.
		c.Password = ""
	}
	for i := range c.ClientCert {
		c.ClientCert[i] = 0
	}
	for i := range c.ClientKey {
		c.ClientKey[i] = 0
	}
	c.ClientCert = nil
	c.ClientKey = nil
}

// SSHTunnel describes an SSH jump configuration. When set on a Connection,
// the driver layer opens an SSH tunnel to Host:Port and proxies the
// database connection through it instead of dialing the database host
// directly.
type SSHTunnel struct {
	Host     string
	Port     int
	Username string
	// KeyPath points at a private key file on the Aura DB host. Mode
	// must be 0600; the driver layer refuses to load keys with broader
	// permissions. Password auth is not supported by design.
	KeyPath string

	// KnownHostsPath points at a SSH known_hosts file used for strict
	// host-key verification. Required by the driver layer — open with
	// no KnownHostsPath fails closed. Path's permissions are not
	// restricted (it's not secret material), but the file must be
	// readable by the auracpd / aura-db process.
	//
	// Format matches OpenSSH's known_hosts (one line per entry,
	// host-pattern + key-type + base64-key). golang.org/x/crypto/ssh/
	// knownhosts parses it.
	//
	// Per SECURITY.md §7.2: blank-trust host-key callbacks are a known
	// MITM primitive; the driver refuses to dial without an explicit
	// pinning source.
	KnownHostsPath string
}

// Target identifies what an action targets, for audit purposes. Empty
// fields are valid (e.g., a connection-create event has no Schema or
// Object).
type Target struct {
	ConnectionID ConnectionID
	Schema       string
	Object       string // table / view / function name, when relevant
}

// String returns a stable string representation for audit logs.
// Format: "<conn>[/<schema>[/<object>]]". Empty fields are elided.
func (t Target) String() string {
	if t.ConnectionID == "" {
		return ""
	}
	s := string(t.ConnectionID)
	if t.Schema == "" {
		// Format is "<conn>[/<schema>[/<object>]]" — nested. An
		// object without a schema is malformed; we drop the object
		// rather than emit "conn//object".
		return s
	}
	s += "/" + t.Schema
	if t.Object != "" {
		s += "/" + t.Object
	}
	return s
}

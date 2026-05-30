# Aura DB — Embedding SDK

**Status:** authoritative (pre-implementation; binding on v0.3.0+).
**Companions:**
- `docs/aura-db/SECURITY.md` — security model.
- `docs/aura-db/ADR-001-architecture.md` — architecture decision record.

This document defines the Go interfaces that host an Aura DB engine.
Implementations of these interfaces are how Aura DB integrates into
auraCP, how it stands alone, and how third parties can embed it in
their own products.

The SDK is the **only** public surface of `pkg/dbadmin`. Anything outside
this document is internal and may change without notice.

---

## 1. The contract

Aura DB's engine is a stateless coordinator. It owns:

- The SQL classifier.
- The driver layer (connect, query, EXPLAIN, schema reads).
- The HTTP handler that the frontend talks to.
- Resource limits and policy enforcement.

It does **not** own:

- Who the operator is (delegated to `Auth`).
- Where connection records and credentials live (delegated to
  `ConnectionStore`).
- Where audit events go (delegated to `AuditSink`).

This separation is what makes Aura DB embeddable. The engine has zero
dependencies on auraCP's internals; auraCP merely happens to be one
implementation of the three interfaces below.

---

## 2. Top-level types

```go
package dbadmin

// User is whatever the host identifies as an operator. The engine
// treats it as opaque; only Auth implementations inspect its fields.
type User struct {
    ID       string            // unique within the host's auth system
    Username string            // for display + audit; not used for matching
    Roles    map[ConnectionID]Role  // populated by the host's Auth impl
    Attrs    map[string]string // arbitrary; e.g. "ip_class", "ua_hash"
}

// ConnectionID is an opaque identifier minted by ConnectionStore.Save.
// The engine never parses it; it round-trips between host and engine.
type ConnectionID string

// Role grants authority on one connection. Strictly ordered.
type Role uint8
const (
    RoleNone Role = iota
    RoleViewer
    RoleAnalyst
    RoleWriter
    RoleDBA
    RoleOwner
)

// Action enumerates everything HasPermission may be asked to authorize.
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

// Engine: the runnable Aura DB.
type Engine struct {
    Auth       Auth
    Conns      ConnectionStore
    Audit      AuditSink
    Config     Config
    // Set by New(). The Engine does not expose its dependencies otherwise.
    // Mutation after New() is undefined behavior; rebuild a new Engine.
}

// New constructs an Engine. Validates that all required interfaces are
// non-nil. Returns an error if a required dependency is missing OR if
// Config is internally inconsistent (e.g., timeout_max < timeout_default).
func New(opt Options) (*Engine, error)

type Options struct {
    Auth       Auth              // required
    Conns      ConnectionStore   // required
    Audit      AuditSink         // required
    Config     Config            // optional; sane defaults if zero-value
    // Optional advanced hooks (see §6):
    QueryHook  QueryHook
    StepUpHook StepUpHook
}

// Handler returns the http.Handler that mounts the engine's REST API
// and WebSocket endpoints. Mount it at any path; the engine emits
// URLs relative to the mount point.
func (e *Engine) Handler() http.Handler

// Embed returns the embedded Svelte SPA as an http.FileSystem.
// Hosts mount this on a different path (e.g., /dbadmin) and the
// engine API on /api/dbadmin. Standalone mode mounts both itself;
// integrated mode reuses the panel's static file serving.
func (e *Engine) Embed() http.FileSystem

// Shutdown gracefully stops the engine: closes open connection pools,
// drains in-flight queries (waiting up to ctx deadline), flushes the
// audit sink. Subsequent calls to Handler() return 503.
func (e *Engine) Shutdown(ctx context.Context) error
```

The engine value is **safe for concurrent use** after `New`. Hosts hand
the same `*Engine` to multiple goroutines; the engine fans out internally.

---

## 3. The Auth interface

```go
// Auth resolves identity and per-action authority. Implementations
// bridge to whatever authentication system is hosting Aura DB.
type Auth interface {
    // Authenticate inspects the incoming request and returns the operator
    // it represents. May return ErrUnauthenticated if no valid session
    // is present. The request is the *raw* request — Authenticate is
    // responsible for cookie / header / mTLS-cert validation.
    //
    // Authenticate is called on EVERY request reaching the engine. It
    // must be O(1) or close to it; cache aggressively if needed.
    Authenticate(*http.Request) (User, error)

    // HasPermission decides whether the user is authorized to perform
    // the action against the connection. ConnectionID may be empty for
    // global actions (e.g., conn.list).
    //
    // Return false for "no" — do NOT return an error to signal denial.
    // Errors are reserved for I/O or system failures (e.g., DB outage
    // while looking up role).
    HasPermission(User, ConnectionID, Action) (bool, error)

    // StepUpRequired reports whether the action requires fresh MFA
    // verification beyond the standing session. The engine consults
    // this BEFORE executing the action and short-circuits with a
    // step-up challenge if the user has not recently stepped up.
    //
    // The default policy in SECURITY.md §5.5 is the recommended
    // baseline; implementations may be more aggressive but not less.
    StepUpRequired(Action) bool

    // VerifyStepUp validates a step-up assertion (WebAuthn / TOTP /
    // recovery code) embedded in the request. Returns the action class
    // the user has just stepped up for (so the engine knows which
    // action-class TTL to apply) and the TTL duration.
    //
    // Implementations are responsible for storing the step-up flag
    // server-side (e.g., in the session). The engine queries
    // HasSteppedUp on subsequent requests; it does not track step-up
    // state itself.
    VerifyStepUp(*http.Request) (Action, time.Duration, error)

    // HasSteppedUp reports whether the user has a valid step-up flag
    // for the given action class. Called by the engine before any
    // action whose StepUpRequired returned true.
    HasSteppedUp(User, Action) bool
}

var ErrUnauthenticated = errors.New("dbadmin: not authenticated")
var ErrStepUpRequired = errors.New("dbadmin: step-up required")
```

### 3.1 Contract guarantees

- The engine calls `Authenticate` exactly once per request, before any
  other Auth call.
- `HasPermission` may be called many times per request (once per resource
  the request touches). It must be fast.
- The engine never persists `User`. The host may regenerate it on each
  request — typical implementations resolve it from the session cookie.
- A `User` returned from `Authenticate` must remain valid for the
  duration of a single request. Across requests, the host may invalidate
  it freely (e.g., on logout).

### 3.2 Reference implementation: standalone

`pkg/dbadmin/standalone/auth.go` provides a reference `StandaloneAuth`:

```go
package standalone

import (
    "github.com/auracp/auracp/pkg/dbadmin"
)

type StandaloneAuth struct {
    DB         *sql.DB         // SQLite, holds users + sessions + step-up flags
    Argon2     argon2.Params   // tuned per release
    WebAuthn   *webauthn.Config
    Clock      func() time.Time
}

func (a *StandaloneAuth) Authenticate(r *http.Request) (dbadmin.User, error) {
    cookie, err := r.Cookie("aura_session")
    if err != nil {
        return dbadmin.User{}, dbadmin.ErrUnauthenticated
    }
    sess, err := a.lookupSession(cookie.Value)
    if err != nil {
        return dbadmin.User{}, dbadmin.ErrUnauthenticated
    }
    if !a.sessionBindingValid(r, sess) {
        a.invalidateSession(sess.ID)
        return dbadmin.User{}, dbadmin.ErrUnauthenticated
    }
    grants, _ := a.loadGrants(sess.UserID)
    return dbadmin.User{
        ID:       sess.UserID,
        Username: sess.Username,
        Roles:    grants,
        Attrs: map[string]string{
            "session_id": sess.ID,
            "ip_class":   ipClass(r),
            "ua_hash":    uaHash(r),
        },
    }, nil
}

func (a *StandaloneAuth) HasPermission(u dbadmin.User, c dbadmin.ConnectionID, action dbadmin.Action) (bool, error) {
    role := u.Roles[c]
    return action.MinRole() <= role, nil
}

func (a *StandaloneAuth) StepUpRequired(action dbadmin.Action) bool {
    return action.RequiresStepUp()
}

func (a *StandaloneAuth) VerifyStepUp(r *http.Request) (dbadmin.Action, time.Duration, error) {
    // Read WebAuthn assertion / TOTP code from the request body.
    // Validate against the user's enrolled factors.
    // On success, store a step-up flag in the sessions DB with the
    // action class and TTL per SECURITY.md §5.5.
}

func (a *StandaloneAuth) HasSteppedUp(u dbadmin.User, action dbadmin.Action) bool {
    // Look up an active step-up flag for the (session, action class) pair.
    // Return true if found and not expired.
}
```

Standalone hosts compose this with the engine:

```go
auth := &standalone.StandaloneAuth{DB: db, /* ... */}
conns := &standalone.StandaloneConnections{DB: db, Secret: secret}
audit := &standalone.StandaloneAudit{Path: "/var/lib/aura-db/audit.log"}

engine, err := dbadmin.New(dbadmin.Options{
    Auth:  auth,
    Conns: conns,
    Audit: audit,
})
```

### 3.3 Reference implementation: auraCP integrated

`internal/api/dbadmin.go` provides a `PanelAuth` that bridges to the
existing panel session + RBAC:

```go
package api

import (
    "github.com/auracp/auracp/pkg/dbadmin"
)

type PanelAuth struct {
    Sessions *session.Manager  // existing panel session manager
    Store    *store.Store      // existing panel store
}

func (a *PanelAuth) Authenticate(r *http.Request) (dbadmin.User, error) {
    sess, err := a.Sessions.FromRequest(r)
    if err != nil {
        return dbadmin.User{}, dbadmin.ErrUnauthenticated
    }
    grants, _ := a.Store.DbadminGrants(sess.UserID)
    return dbadmin.User{
        ID:       sess.UserID,
        Username: sess.Username,
        Roles:    grants,
        Attrs: map[string]string{
            "session_id": sess.ID,
            "panel_perms": sess.Perms.String(), // for fine-grained checks
        },
    }, nil
}

// ... rest of the methods bridge to the panel's existing infrastructure.
```

In integrated mode, the panel's user-management UI gains a "Database
admin grants" tab where panel admins assign Aura DB roles per
connection.

---

## 4. The ConnectionStore interface

```go
// ConnectionStore owns connection metadata and credentials. The engine
// never sees encryption keys; it asks the store for credentials and
// receives them already decrypted.
type ConnectionStore interface {
    // List returns connections the user has any grant on. Filtering
    // by grant is the store's responsibility (it has the join data);
    // the engine does NOT filter the returned list.
    //
    // Returns an empty slice (not nil) when the user has no grants.
    List(context.Context, User) ([]Connection, error)

    // Get fetches a single connection. The engine has already
    // authorized the action via Auth.HasPermission; Get does not
    // re-check. It DOES enforce that the connection exists for the
    // caller's tenant if the store is multi-tenant.
    //
    // Returns ErrNotFound if the connection does not exist. The
    // engine maps this to HTTP 404.
    Get(context.Context, ConnectionID) (Connection, error)

    // Credentials returns decrypted credentials. Implementations are
    // responsible for decryption.
    //
    // Credentials MUST be zeroed by the caller after use; the engine
    // calls Zero() on the returned struct before garbage collection.
    Credentials(context.Context, ConnectionID) (Credentials, error)

    // Save creates or updates a connection. If c.ID is empty, a new
    // ID is minted and returned. If c.ID is set, the existing record
    // is updated.
    //
    // Credentials are passed alongside (rather than embedded in
    // Connection) so the store can decide encryption details without
    // exposing them to the engine.
    Save(context.Context, Connection, Credentials) (ConnectionID, error)

    // Delete removes a connection. Returns ErrNotFound if not present.
    // The engine emits an audit event before calling Delete; if Delete
    // fails, the engine updates the event with the error.
    Delete(context.Context, ConnectionID) error
}

var ErrNotFound = errors.New("dbadmin: connection not found")

// Connection is the engine-visible metadata.
type Connection struct {
    ID          ConnectionID
    Name        string         // display name
    Engine      EngineKind     // MariaDB or Postgres
    Host        string
    Port        int
    Database    string         // default schema/database
    Username    string         // owner display; password is in Credentials
    Tags        []Tag          // see SECURITY.md §6.2
    UseSSL      bool
    SSLMode     string         // engine-specific: "require"/"verify-full"/etc.
    SSHTunnel   *SSHTunnel     // optional
    CreatedAt   time.Time
    UpdatedAt   time.Time
    Owner       string         // user ID who created it (for audit)
    Origin      Origin         // see §4.2
}

type EngineKind uint8
const (
    EngineMariaDB EngineKind = iota + 1
    EnginePostgres
)

type Tag string
const (
    TagProd     Tag = "prod"
    TagStaging  Tag = "staging"
    TagDev      Tag = "dev"
    TagFourEye  Tag = "4-eye"
    TagReadOnly Tag = "read-only"
)

// Origin records where a connection came from.
type Origin string
const (
    OriginManual       Origin = "manual"        // user-created via UI
    OriginPanelSite    Origin = "panel-site"    // auto-discovered from a panel site DB
    OriginPanelImport  Origin = "panel-import"  // imported via aura-db import-config
)

type Credentials struct {
    Password   string  // plaintext after store decryption
    ClientCert []byte  // optional, PEM
    ClientKey  []byte  // optional, PEM
}

// Zero overwrites all credential bytes with zeroes. Implementations
// should call this on returned Credentials values once consumed.
func (c *Credentials) Zero()

type SSHTunnel struct {
    Host     string
    Port     int
    Username string
    KeyPath  string // path to private key file, mode 0600
}
```

### 4.1 Contract guarantees

- `Credentials` returned from the store have an active reference for the
  duration of one engine method call. The engine does not retain the
  reference; it zeros it before returning.
- The store may cache decrypted credentials internally for connection-
  pool reuse; doing so is a store-level optimization, not an engine
  concern.
- `Save` is idempotent for the same `(Connection, Credentials)` tuple.
- `Delete` cascades to any audit-log foreign key inside the store; the
  engine treats the deletion as atomic from its perspective.

### 4.2 Origin tracking

`Origin` exists so integrated mode can:

- Auto-discover connections from panel sites (`OriginPanelSite`).
- Lifecycle-couple them to the site (`OriginPanelSite` connections are
  read-only via the store: trying to `Delete` them returns an error
  unless the originating site is being deleted).
- Distinguish operator-created connections (`OriginManual`) which the
  operator may delete freely.

Standalone mode uses only `OriginManual` and `OriginPanelImport`.

### 4.3 Reference implementation: standalone

```go
package standalone

type StandaloneConnections struct {
    DB     *sql.DB           // SQLite
    Secret *secret.Box       // AES-GCM with key from /etc/aura-db/secret.key
}

func (s *StandaloneConnections) Get(ctx context.Context, id dbadmin.ConnectionID) (dbadmin.Connection, error) {
    var c dbadmin.Connection
    var tags string
    err := s.DB.QueryRowContext(ctx, `
        SELECT id, name, engine, host, port, database, username, tags, ssl, sslmode, ...
        FROM connections WHERE id = ?`, id).Scan(&c.ID, &c.Name, ...)
    if errors.Is(err, sql.ErrNoRows) {
        return dbadmin.Connection{}, dbadmin.ErrNotFound
    }
    c.Tags = parseTags(tags)
    return c, err
}

func (s *StandaloneConnections) Credentials(ctx context.Context, id dbadmin.ConnectionID) (dbadmin.Credentials, error) {
    var enc []byte
    err := s.DB.QueryRowContext(ctx, `SELECT creds_enc FROM connections WHERE id = ?`, id).Scan(&enc)
    if err != nil {
        return dbadmin.Credentials{}, err
    }
    plain, err := s.Secret.Decrypt(enc)
    if err != nil {
        return dbadmin.Credentials{}, fmt.Errorf("decrypt credentials for %s: %w", id, err)
    }
    var c dbadmin.Credentials
    if err := json.Unmarshal(plain, &c); err != nil {
        return dbadmin.Credentials{}, err
    }
    return c, nil
}
```

### 4.4 Reference implementation: auraCP integrated

```go
package api

type PanelConnections struct {
    Store  *store.Store
    Secret *secret.Box   // existing panel secret
}

func (p *PanelConnections) List(ctx context.Context, u dbadmin.User) ([]dbadmin.Connection, error) {
    // Join the panel's `databases` table with `dbadmin_grants`.
    rows, err := p.Store.DB.QueryContext(ctx, `
        SELECT d.id, d.name, d.engine, d.host, d.port, d.name AS database, d.user, ...
        FROM databases d
        JOIN dbadmin_grants g ON g.connection_id = d.id
        WHERE g.user_id = ? AND g.role > 0
        ORDER BY d.name`, u.ID)
    // ...
}
```

In integrated mode, the `databases` table is the source of truth for
connection existence — the panel's site lifecycle owns it. Aura DB
joins onto it but never creates rows there directly.

---

## 5. The AuditSink interface

```go
// AuditSink receives every audit event the engine emits. Implementations
// are responsible for: durable storage, tamper-evidence (chain), optional
// forwarding to syslog / webhooks / S3.
type AuditSink interface {
    // Record persists an event. MUST be append-only: implementations
    // never modify or delete previously recorded events.
    //
    // Record is called from the request goroutine. It must return
    // quickly (< 10 ms typical); heavy work (chain signing, remote
    // forwarding) belongs in a background goroutine owned by the
    // implementation.
    //
    // A failed Record() is logged by the engine but does NOT fail the
    // user-facing request. This is a deliberate trade-off: a temporary
    // audit-storage outage should not block legitimate operator work.
    // The implementation is expected to buffer-or-spool on failure and
    // alert separately.
    Record(context.Context, Event)
}

// Event is the unit of audit. Fields are populated by the engine.
type Event struct {
    EventID         ULID            // monotonic
    Timestamp       time.Time       // UTC
    UserID          string
    UserRoleAtTime  Role
    SourceIP        string
    UserAgentHash   string
    Action          Action
    Target          Target
    Statement       string          // SQL text; redacted for dangerous params
    ParametersRedacted map[string]any
    ResultRows      int64
    DurationMS      int64
    Error           string          // empty on success
    StepUpJTI       string          // empty if no step-up was used
    PrevEventHash   string          // chain link, set by the sink
}

type Target struct {
    ConnectionID ConnectionID
    Schema       string
    Object       string  // table / view / function name
}
```

### 5.1 Contract guarantees

- The engine emits **exactly one** event per state-changing action. The
  event is emitted **before** the action begins; the sink call returns
  before the action's outcome is known.
- The engine emits **a second update event** to convey the action's
  outcome (success or error). Sinks correlate via `EventID` — the
  outcome event's `EventID` is `original.EventID + 1` (still ULID-
  ordered).
- For read-only operations, the engine emits at the sampling rate
  configured in `Engine.Config.Audit.SampleReadQueries` (default 1%).
- The engine does NOT call `Record` from a goroutine — it calls it
  directly in the request path. Implementations doing background work
  must launch their own goroutines.

### 5.2 Reference implementation: file-backed chain

```go
package standalone

type FileAuditSink struct {
    Path          string
    SigningKey    []byte
    SigningEvery  int           // events between signed chain heads
    SigningClock  time.Duration // max time between signed heads

    mu       sync.Mutex
    file     *os.File
    prevHash string
    counter  int
    lastSign time.Time
    queue    chan dbadmin.Event  // overflow buffer
}

func (s *FileAuditSink) Record(ctx context.Context, e dbadmin.Event) {
    select {
    case s.queue <- e:
    default:
        // Queue full → block briefly; if still full, drop and log
        // a panel-level alert. Audit drops are themselves audited
        // via a separate "auracp.audit.dropped" event.
        select {
        case s.queue <- e:
        case <-time.After(50 * time.Millisecond):
            slog.Error("audit queue overflow, event dropped",
                "event_id", e.EventID, "action", e.Action)
        }
    }
}

func (s *FileAuditSink) drain() {
    for e := range s.queue {
        s.mu.Lock()
        e.PrevEventHash = s.prevHash
        line, _ := json.Marshal(e)
        s.file.Write(append(line, '\n'))
        s.prevHash = hashEvent(line)
        s.counter++
        if s.counter >= s.SigningEvery || time.Since(s.lastSign) > s.SigningClock {
            s.signAndShipHead()
            s.counter = 0
            s.lastSign = time.Now()
        }
        s.mu.Unlock()
    }
}
```

### 5.3 Reference implementation: panel-integrated

```go
package api

type PanelAudit struct {
    Store *store.Store  // writes into the existing `audit_log` table
}

func (p *PanelAudit) Record(ctx context.Context, e dbadmin.Event) {
    _ = p.Store.InsertAuditEvent(ctx, store.AuditEvent{
        ID:          e.EventID.String(),
        Timestamp:   e.Timestamp,
        UserID:      e.UserID,
        Source:      "aura-db",
        Action:      string(e.Action),
        Target:      e.Target.String(),
        // ... rest of fields
    })
}
```

The panel's existing audit infrastructure (table, retention,
forwarding) handles Aura DB events identically to site events.

---

## 6. Advanced hooks (optional)

For sophisticated integrations.

### 6.1 QueryHook

```go
// QueryHook receives notifications around query execution. Implementations
// can rate-limit, mutate the audit event, or veto execution.
type QueryHook interface {
    // BeforeQuery is called after authorization but before driver dispatch.
    // Returning a non-nil error vetoes the query. The error is surfaced
    // to the operator verbatim; do not leak internal details.
    BeforeQuery(context.Context, User, Connection, ParsedQuery) error

    // AfterQuery is called once the driver returns (success or failure).
    // Implementations can record metrics, ship to external observability,
    // etc. Returning an error here is ignored — the result is already
    // committed.
    AfterQuery(context.Context, User, Connection, ParsedQuery, QueryResult, error)
}

// ParsedQuery is the classifier's structured view.
type ParsedQuery struct {
    Class      QueryClass
    Statements []ParsedStatement  // multi-statement support
    Forbidden  []string            // empty unless something hit the forbidden list
}

type ParsedStatement struct {
    Class  QueryClass
    Kind   StatementKind
    Tables []Target
    Action Action
}
```

Use cases:
- Rate-limit specific statement classes beyond the global limits.
- Forward `dba`/`owner` queries to a SIEM in real time.
- Implement custom 4-eye approval (override the built-in implementation).

### 6.2 StepUpHook

```go
// StepUpHook is called when an action requires step-up. The default
// engine behavior returns 403 with a "step-up required" payload that
// the frontend handles. Hosts wanting a different flow (e.g., out-of-
// band notification, push to mobile) implement this.
type StepUpHook interface {
    InitiateStepUp(context.Context, User, Action) (StepUpChallenge, error)
}

type StepUpChallenge struct {
    JTI         string
    DeliveredBy string  // "webauthn" / "totp" / "push" / "out-of-band"
    Payload     map[string]any
}
```

---

## 7. HTTP surface

The engine exposes one mount point. All endpoints below are relative
to wherever the host mounts `engine.Handler()` (typically
`/api/dbadmin`).

| Method | Path                                                | Purpose                                  |
| ------ | --------------------------------------------------- | ---------------------------------------- |
| `GET`  | `/connections`                                      | List user-visible connections.            |
| `POST` | `/connections`                                      | Create a connection. Body: ConnectionInput. |
| `GET`  | `/connections/{id}`                                 | Connection details (no creds).            |
| `PUT`  | `/connections/{id}`                                 | Update a connection.                      |
| `DELETE` | `/connections/{id}`                               | Delete a connection.                      |
| `POST` | `/connections/{id}/test`                            | Test the connection. Returns latency, server version. |
| `POST` | `/connections/{id}/password/reveal`                 | Reveal stored password (step-up). One-shot. |
| `GET`  | `/connections/{id}/schemas`                         | List schemas/databases.                  |
| `GET`  | `/connections/{id}/schemas/{s}/objects`             | List tables, views, funcs, procs.        |
| `GET`  | `/connections/{id}/schemas/{s}/tables/{t}`          | Table metadata (cols, indexes, FKs).     |
| `GET`  | `/connections/{id}/schemas/{s}/tables/{t}/rows`     | Paginated rows. Query params: `limit`, `offset`, `sort`, `filter`. |
| `PATCH`| `/connections/{id}/schemas/{s}/tables/{t}/rows/{pk}` | Update a row. Body: column → new value.  |
| `DELETE` | `/connections/{id}/schemas/{s}/tables/{t}/rows/{pk}` | Delete a row.                        |
| `POST` | `/connections/{id}/query`                           | Run SQL. Body: `{statement, parameters}`. |
| `POST` | `/connections/{id}/explain`                         | EXPLAIN a query. Returns normalized plan. |
| `GET`  | `/connections/{id}/history`                         | Query history for the calling user.       |
| `POST` | `/connections/{id}/saved-queries`                   | Save a query.                            |
| `GET`  | `/connections/{id}/saved-queries`                   | List saved queries.                      |
| `POST` | `/connections/{id}/export`                          | Initiate export. Returns a signed URL.   |
| `POST` | `/connections/{id}/import`                          | Initiate import (CSV/JSON/SQL).          |
| `WS`   | `/connections/{id}/slow-log/stream`                 | WebSocket: slow query log tail (MariaDB table mode) / pg_stat_statements snapshot (Postgres) (v0.3.2-C). Subprotocol `aura.slowlog.v1`. |
| `GET`  | `/connections/{id}/audit`                           | Last 1000 events. Filterable.            |
| `POST` | `/step-up/initiate`                                 | Begin a step-up flow (returns challenge). |
| `POST` | `/step-up/verify`                                   | Verify a step-up assertion.              |

Every endpoint returns JSON. Errors are:

```json
{
  "error": {
    "code": "step-up-required",
    "message": "This action requires step-up authentication.",
    "request_id": "01H..."
  }
}
```

Error `code` values are stable across releases (semver-protected).
`message` is human-readable and may change.

---

## 8. Stability guarantees

The SDK follows semver with respect to its public surface:

| Surface                                  | Stability                                     |
| ---------------------------------------- | --------------------------------------------- |
| `pkg/dbadmin` exported types + methods   | semver-stable. Breaking changes require major bump. |
| Interface method signatures (Auth/Conns/Audit) | semver-stable.                          |
| Error sentinel values (`ErrNotFound`, etc.) | semver-stable.                              |
| HTTP endpoint paths                      | semver-stable.                                |
| HTTP error `code` strings                | semver-stable.                                |
| HTTP response payloads                   | additive-stable: new fields ok, removed = breaking. |
| `Engine.Config` fields                   | additive-stable; defaults may change in minor. |
| Behavior tied to a stated default        | semver-stable for the default value.          |
| Internal packages (`pkg/dbadmin/internal/...`) | NOT stable. Do not import.              |

We do not promise the wire format of `Event.Statement` (audit log text)
to be stable; it depends on classifier internals. If you ingest audit
events programmatically, treat statements as opaque strings.

---

## 9. Testing helpers

`pkg/dbadmin/dbadmintest` provides in-memory implementations of all
three interfaces for tests:

```go
import "github.com/auracp/auracp/pkg/dbadmin/dbadmintest"

func TestMyIntegration(t *testing.T) {
    auth := dbadmintest.NewAuth().
        WithUser("alice", dbadmin.RoleOwner).
        WithUser("bob", dbadmin.RoleViewer)

    conns := dbadmintest.NewConnections().
        WithConnection(dbadmin.Connection{
            Name: "test-db", Engine: dbadmin.EngineMariaDB,
            Host: "localhost", Port: 3306, Database: "test",
        }, dbadmin.Credentials{Password: "secret"})

    audit := dbadmintest.NewAudit()  // captures all events; assertable in tests

    engine, _ := dbadmin.New(dbadmin.Options{
        Auth: auth, Conns: conns, Audit: audit,
    })

    // ... drive engine.Handler() with httptest, assert audit.Events()
}
```

The test helpers are part of the public surface (`pkg/dbadmin/dbadmintest`)
and follow the same stability guarantees as the rest of the SDK.

---

## 10. Versioning the SDK separately from auraCP

In integrated mode, the SDK and auraCP release together — same tag,
same version.

In standalone mode, the SDK and standalone binary have independent
tags:

- `pkg/dbadmin v0.3.0` — the SDK release.
- `aura-db v0.3.0` — the standalone binary release.
- `auracp v0.3.0` — the panel release, which embeds the SDK at v0.3.0.

For a given calendar release, all three share the version number.
The independent tags exist for clarity (`go get github.com/auracp/auracp/pkg/dbadmin@v0.3.0`
without fetching the entire panel source).

---

## 11. Migration guide skeleton

When the SDK has its first breaking change (post-v1.0), this section
will become a real `MIGRATION.md`. Until then:

- v0.x releases may break the SDK between minor versions with 30-day
  notice in the changelog.
- v1.0 will be cut once the SDK has been stable for two consecutive
  minor releases without breaking changes.

---

## 12. Example: building a third-party host

The minimal embedded host. Demonstrates that no auraCP internals are
required.

```go
package main

import (
    "context"
    "log"
    "net/http"

    "github.com/auracp/auracp/pkg/dbadmin"
    "github.com/auracp/auracp/pkg/dbadmin/standalone"
)

func main() {
    // Compose with standalone implementations.
    auth, _ := standalone.NewAuth("./aura.db")
    conns, _ := standalone.NewConnections("./aura.db", "./secret.key")
    audit, _ := standalone.NewAudit("./audit.log")

    engine, err := dbadmin.New(dbadmin.Options{
        Auth:  auth,
        Conns: conns,
        Audit: audit,
    })
    if err != nil {
        log.Fatal(err)
    }

    mux := http.NewServeMux()
    mux.Handle("/api/dbadmin/", http.StripPrefix("/api/dbadmin", engine.Handler()))
    mux.Handle("/", http.FileServer(engine.Embed()))

    log.Println("listening on :8090")
    log.Fatal(http.ListenAndServeTLS(":8090", "cert.pem", "key.pem", mux))
}
```

That's a complete Aura DB host. ~30 lines. Bring your own TLS cert,
your own listener, your own filesystem layout.

The auraCP integrated mode is structurally identical — it just
substitutes `panel.NewAuth(...)` / `panel.NewConnections(...)` /
`panel.NewAudit(...)` for the standalone implementations.

---

*This document is canonical. Discrepancies between this and any code,
comment, or downstream documentation resolve in favor of this document.
Updates require an ADR.*

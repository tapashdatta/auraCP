package httpapi

import (
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/driver"
	"github.com/auracp/auracp/pkg/dbadmin/explain"
	"github.com/auracp/auracp/pkg/dbadmin/history"
	"github.com/auracp/auracp/pkg/dbadmin/schema"
)

// ─── Request DTOs ─────────────────────────────────────────────────────

type connectionInput struct {
	Name      string          `json:"name"`
	Engine    string          `json:"engine"`
	Host      string          `json:"host"`
	Port      int             `json:"port"`
	Database  string          `json:"database"`
	Username  string          `json:"username"`
	Password  string          `json:"password,omitempty"`
	SSHTunnel *sshTunnelInput `json:"sshTunnel,omitempty"`
	TLS       *tlsInput       `json:"tls,omitempty"`
	Tags      []string        `json:"tags,omitempty"`
	PoolSize  int             `json:"poolSize,omitempty"`
}

type sshTunnelInput struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Username   string `json:"username"`
	PrivateKey string `json:"privateKey,omitempty"`
	KnownHosts string `json:"knownHosts,omitempty"`
}

type tlsInput struct {
	Mode       string `json:"mode"`
	CACert     string `json:"caCert,omitempty"`
	ClientCert string `json:"clientCert,omitempty"`
	ClientKey  string `json:"clientKey,omitempty"`
}

type queryRequest struct {
	Statement  string `json:"statement"`
	Parameters []any  `json:"parameters,omitempty"`
	MaxRows    int    `json:"maxRows,omitempty"`
	Timeout    string `json:"timeout,omitempty"`
}

type explainRequest struct {
	Statement string `json:"statement"`
	Analyze   bool   `json:"analyze"`
}

type insertRowRequest struct {
	Values map[string]any `json:"values"`
}

type updateRowRequest struct {
	Set map[string]any `json:"set"`

	// edit-1: optimistic-concurrency snapshot. When present, the row is
	// updated only if every {col: val} pair in Where still matches the
	// current value in the target row. On mismatch the handler returns
	// 409 / conflict so the client can refresh + retry. Keys must be
	// declared columns; the rows package validates them.
	Where map[string]any `json:"where,omitempty"`
}

type patchHistoryRequest struct {
	Starred *bool    `json:"starred,omitempty"`
	Tags    []string `json:"tags,omitempty"`
}

type savedQueryInput struct {
	Name      string   `json:"name"`
	Statement string   `json:"statement"`
	Tags      []string `json:"tags,omitempty"`
}

// classifyRequest drives /sql/classify and /connections/{id}/classify.
// Engine is required on the bare /sql/classify path; on the connection
// path it is ignored and the connection record's engine is used instead.
type classifyRequest struct {
	Engine    string `json:"engine,omitempty"`
	Statement string `json:"statement"`
}

// exportRequest drives POST /connections/{id}/export. The endpoint
// builds the SELECT server-side from validated identifier inputs; it
// does NOT accept raw SQL. Format selects the on-wire serialization.
type exportRequest struct {
	Schema        string             `json:"schema"`
	Table         string             `json:"table"`
	Columns       []string           `json:"columns,omitempty"`
	Filter        []exportPredicate  `json:"filter,omitempty"`
	Sort          []exportSortKey    `json:"sort,omitempty"`
	Format        string             `json:"format"`
	Limit         int                `json:"limit,omitempty"`
	IncludeHeader *bool              `json:"includeHeader,omitempty"` // CSV only
	Filename      string             `json:"filename,omitempty"`
}

// exportPredicate mirrors rows.Predicate on the wire.
type exportPredicate struct {
	Column string `json:"column"`
	Op     string `json:"op"`
	Value  any    `json:"value,omitempty"`
}

// exportSortKey mirrors rows.SortKey on the wire.
type exportSortKey struct {
	Column     string `json:"column"`
	Descending bool   `json:"descending,omitempty"`
}

type stepUpInitiateRequest struct {
	Action string `json:"action"`
}

type stepUpVerifyRequest struct {
	JTI       string `json:"jti"`
	Assertion string `json:"assertion"`
}

// ─── Response DTOs ────────────────────────────────────────────────────

type emptyResponse struct{}

// connectionDTO — secrets redacted; presence-only booleans for stored creds.
type connectionDTO struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Engine      string        `json:"engine"`
	Host        string        `json:"host"`
	Port        int           `json:"port"`
	Database    string        `json:"database"`
	Username    string        `json:"username"`
	HasPassword bool          `json:"hasPassword"`
	SSHTunnel   *sshTunnelDTO `json:"sshTunnel,omitempty"`
	TLS         *tlsDTO       `json:"tls,omitempty"`
	Tags        []string      `json:"tags"`
	PoolSize    int           `json:"poolSize"`
	CreatedAt   time.Time     `json:"createdAt"`
	UpdatedAt   time.Time     `json:"updatedAt"`
}

type sshTunnelDTO struct {
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Username      string `json:"username"`
	HasPrivateKey bool   `json:"hasPrivateKey"`
	HasKnownHosts bool   `json:"hasKnownHosts"`
}

type tlsDTO struct {
	Mode          string `json:"mode"`
	HasCACert     bool   `json:"hasCaCert"`
	HasClientCert bool   `json:"hasClientCert"`
	HasClientKey  bool   `json:"hasClientKey"`
}

type testConnectionResponse struct {
	LatencyMS     int64  `json:"latencyMs"`
	ServerVersion string `json:"serverVersion"`
}

type revealPasswordResponse struct {
	Password string    `json:"password"`
	Expires  time.Time `json:"expires"`
}

type listSchemasResponse struct {
	Schemas []string `json:"schemas"`
}

type listObjectsResponse struct {
	Tables     []tableSummaryDTO     `json:"tables"`
	Views      []viewSummaryDTO      `json:"views"`
	Functions  []functionSummaryDTO  `json:"functions"`
	Procedures []procedureSummaryDTO `json:"procedures"`
}

type tableSummaryDTO struct {
	Schema       string `json:"schema"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	Comment      string `json:"comment"`
	RowsEstimate int64  `json:"rowsEstimate"`
	Engine       string `json:"engine"`
}

type viewSummaryDTO struct {
	Schema     string `json:"schema"`
	Name       string `json:"name"`
	Comment    string `json:"comment"`
	Updatable  bool   `json:"updatable"`
	Definition string `json:"definition"`
}

type functionSummaryDTO struct {
	Schema      string `json:"schema"`
	Name        string `json:"name"`
	Language    string `json:"language"`
	ReturnType  string `json:"returnType"`
	Arguments   string `json:"arguments"`
	Comment     string `json:"comment"`
	IsAggregate bool   `json:"isAggregate"`
}

type procedureSummaryDTO struct {
	Schema    string `json:"schema"`
	Name      string `json:"name"`
	Language  string `json:"language"`
	Arguments string `json:"arguments"`
	Comment   string `json:"comment"`
}

type tableDTO struct {
	Schema      string            `json:"schema"`
	Name        string            `json:"name"`
	Kind        string            `json:"kind"`
	Comment     string            `json:"comment"`
	Columns     []columnDTO       `json:"columns"`
	PrimaryKey  []string          `json:"primaryKey"`
	Indexes     []indexDTO        `json:"indexes"`
	ForeignKeys []foreignKeyDTO   `json:"foreignKeys"`
	Triggers    []triggerDTO      `json:"triggers"`
	Extras      map[string]string `json:"extras,omitempty"`
}

type columnDTO struct {
	Name            string `json:"name"`
	Position        int    `json:"position"`
	DataType        string `json:"dataType"`
	Nullable        bool   `json:"nullable"`
	Default         string `json:"default"`
	Comment         string `json:"comment"`
	IsPrimaryKey    bool   `json:"isPrimaryKey"`
	IsAutoIncrement bool   `json:"isAutoIncrement"`
	IsGenerated     bool   `json:"isGenerated"`
	CharacterSet    string `json:"characterSet"`
	Collation       string `json:"collation"`
}

type indexDTO struct {
	Name      string   `json:"name"`
	Columns   []string `json:"columns"`
	Unique    bool     `json:"unique"`
	Primary   bool     `json:"primary"`
	Method    string   `json:"method"`
	Predicate string   `json:"predicate"`
	Comment   string   `json:"comment"`
}

type foreignKeyDTO struct {
	Name              string   `json:"name"`
	Columns           []string `json:"columns"`
	ReferencedSchema  string   `json:"referencedSchema"`
	ReferencedTable   string   `json:"referencedTable"`
	ReferencedColumns []string `json:"referencedColumns"`
	OnDelete          string   `json:"onDelete"`
	OnUpdate          string   `json:"onUpdate"`
}

type triggerDTO struct {
	Schema     string `json:"schema"`
	Table      string `json:"table"`
	Name       string `json:"name"`
	Event      string `json:"event"`
	Timing     string `json:"timing"`
	Comment    string `json:"comment"`
	Definition string `json:"definition"`
}

type columnInfoDTO struct {
	Name             string `json:"name"`
	DatabaseTypeName string `json:"databaseTypeName"`
	Nullable         bool   `json:"nullable"`
	PrimaryKey       bool   `json:"primaryKey"`
}

type readRowsResponse struct {
	Columns []columnInfoDTO `json:"columns"`
	Rows    [][]any         `json:"rows"`
	Total   *int64          `json:"total,omitempty"`
}

type updateResultResponse struct {
	RowsAffected int64 `json:"rowsAffected"`
	LastInsertID int64 `json:"lastInsertId,omitempty"`
}

type queryResponse struct {
	Columns    []columnInfoDTO `json:"columns"`
	Rows       [][]any         `json:"rows"`
	DurationMS int64           `json:"durationMs"`
	Truncated  bool            `json:"truncated"`
	Class      string          `json:"class"`
}

type explainResponse struct {
	Plan *explain.Plan `json:"plan"`
}

type historyEntryDTO struct {
	ID           int64     `json:"id"`
	UserID       string    `json:"userId"`
	ConnectionID string    `json:"connectionId"`
	SQL          string    `json:"sql"`
	Class        string    `json:"class"`
	Tags         []string  `json:"tags"`
	Starred      bool      `json:"starred"`
	DurationMS   int64     `json:"durationMs"`
	RowsReturned int64     `json:"rowsReturned"`
	Error        string    `json:"error"`
	Executed     time.Time `json:"executed"`
	Engine       string    `json:"engine"`
}

type listHistoryResponse struct {
	Entries []historyEntryDTO `json:"entries"`
	Total   int               `json:"total"`
}

type searchHistoryResponse struct {
	Results []searchResultDTO `json:"results"`
}

type searchResultDTO struct {
	historyEntryDTO
	Score float64 `json:"score"`
}

// classifyResponse is the wire form of classifier.ParsedQuery. The
// editor uses Class to drive the toolbar chip and Statements[i].Class
// for per-statement cursor preview; Forbidden drives lint diagnostics.
type classifyResponse struct {
	Class      string                   `json:"class"`
	Statements []classifiedStatementDTO `json:"statements"`
	Forbidden  []forbiddenMatchDTO      `json:"forbidden"`
}

type classifiedStatementDTO struct {
	Class    string `json:"class"`
	Kind     string `json:"kind"`
	Action   string `json:"action"`
	HasWhere bool   `json:"hasWhere"`
	Offset   int    `json:"offset"`
	RawText  string `json:"rawText"`
}

type forbiddenMatchDTO struct {
	Pattern        string `json:"pattern"`
	Reason         string `json:"reason"`
	StatementIndex int    `json:"statementIndex"`
	TokenOffset    int    `json:"tokenOffset"`
}

type savedQueryDTO struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Statement string    `json:"statement"`
	Tags      []string  `json:"tags"`
	CreatedAt time.Time `json:"createdAt"`
}

type importResponse struct {
	RowsImported int64  `json:"rowsImported"`
	JobID        string `json:"jobId"`
}

type auditEventDTO struct {
	EventID            string         `json:"eventId"`
	Timestamp          time.Time      `json:"timestamp"`
	UserID             string         `json:"userId"`
	UserRoleAtTime     string         `json:"userRoleAtTime"`
	SourceIP           string         `json:"sourceIp"`
	UserAgentHash      string         `json:"userAgentHash"`
	Action             string         `json:"action"`
	Target             string         `json:"target"`
	Statement          string         `json:"statement"`
	ParametersRedacted map[string]any `json:"parametersRedacted"`
	ResultRows         int64          `json:"resultRows"`
	DurationMS         int64          `json:"durationMs"`
	Error              string         `json:"error"`
	StepUpJTI          string         `json:"stepUpJti"`
	PrevEventHash      string         `json:"prevEventHash"`
}

type listAuditResponse struct {
	Events []auditEventDTO `json:"events"`
}

type stepUpInitiateResponse struct {
	JTI         string    `json:"jti"`
	DeliveredBy string    `json:"deliveredBy"`
	Payload     any       `json:"payload,omitempty"`
	Expires     time.Time `json:"expires"`
}

type stepUpVerifyResponse struct {
	Success       bool      `json:"success"`
	GrantedAction string    `json:"grantedAction,omitempty"`
	Expires       time.Time `json:"expires,omitempty"`
}

// ─── Helpers / conversions ────────────────────────────────────────────

// redactConnection produces a connectionDTO with no secret bytes. The
// underlying creds are presence-checked via dbadmin.ConnectionStore in
// the caller (we don't see them here).
func redactConnection(c dbadmin.Connection, hasPassword bool) connectionDTO {
	tags := make([]string, 0, len(c.Tags))
	for _, t := range c.Tags {
		tags = append(tags, string(t))
	}
	out := connectionDTO{
		ID:          string(c.ID),
		Name:        c.Name,
		Engine:      c.Engine.String(),
		Host:        c.Host,
		Port:        c.Port,
		Database:    c.Database,
		Username:    c.Username,
		HasPassword: hasPassword,
		Tags:        tags,
		PoolSize:    0,
		CreatedAt:   c.CreatedAt,
		UpdatedAt:   c.UpdatedAt,
	}
	if c.SSHTunnel != nil {
		out.SSHTunnel = &sshTunnelDTO{
			Host:          c.SSHTunnel.Host,
			Port:          c.SSHTunnel.Port,
			Username:      c.SSHTunnel.Username,
			HasPrivateKey: c.SSHTunnel.KeyPath != "",
			HasKnownHosts: c.SSHTunnel.KnownHostsPath != "",
		}
	}
	if c.UseSSL || c.SSLMode != "" {
		out.TLS = &tlsDTO{
			Mode: c.SSLMode,
		}
	}
	return out
}

// columnInfoToDTO converts a driver.ColumnInfo into its wire form.
func columnInfoToDTO(c driver.ColumnInfo) columnInfoDTO {
	return columnInfoDTO{
		Name:             c.Name,
		DatabaseTypeName: c.DatabaseTypeName,
		Nullable:         c.Nullable,
		PrimaryKey:       c.PrimaryKey,
	}
}

func columnInfosToDTO(cs []driver.ColumnInfo) []columnInfoDTO {
	out := make([]columnInfoDTO, len(cs))
	for i, c := range cs {
		out[i] = columnInfoToDTO(c)
	}
	return out
}

// tableToDTO converts a schema.Table to its wire form.
func tableToDTO(t *schema.Table) tableDTO {
	dto := tableDTO{
		Schema:     t.Schema,
		Name:       t.Name,
		Kind:       t.Kind.String(),
		Comment:    t.Comment,
		PrimaryKey: t.PrimaryKey,
		Extras:     t.Extras,
	}
	dto.Columns = make([]columnDTO, len(t.Columns))
	for i, c := range t.Columns {
		dto.Columns[i] = columnDTO{
			Name:            c.Name,
			Position:        c.Position,
			DataType:        c.DataType,
			Nullable:        c.Nullable,
			Default:         c.Default,
			Comment:         c.Comment,
			IsPrimaryKey:    c.IsPrimaryKey,
			IsAutoIncrement: c.IsAutoIncrement,
			IsGenerated:     c.IsGenerated,
			CharacterSet:    c.CharacterSet,
			Collation:       c.Collation,
		}
	}
	dto.Indexes = make([]indexDTO, len(t.Indexes))
	for i, idx := range t.Indexes {
		dto.Indexes[i] = indexDTO{
			Name:      idx.Name,
			Columns:   idx.Columns,
			Unique:    idx.Unique,
			Primary:   idx.Primary,
			Method:    idx.Method,
			Predicate: idx.Predicate,
			Comment:   idx.Comment,
		}
	}
	dto.ForeignKeys = make([]foreignKeyDTO, len(t.ForeignKeys))
	for i, fk := range t.ForeignKeys {
		dto.ForeignKeys[i] = foreignKeyDTO{
			Name:              fk.Name,
			Columns:           fk.Columns,
			ReferencedSchema:  fk.ReferencedSchema,
			ReferencedTable:   fk.ReferencedTable,
			ReferencedColumns: fk.ReferencedColumns,
			OnDelete:          fk.OnDelete,
			OnUpdate:          fk.OnUpdate,
		}
	}
	dto.Triggers = make([]triggerDTO, len(t.Triggers))
	for i, tr := range t.Triggers {
		dto.Triggers[i] = triggerDTO{
			Schema:     tr.Schema,
			Table:      tr.Table,
			Name:       tr.Name,
			Event:      tr.Event,
			Timing:     tr.Timing,
			Comment:    tr.Comment,
			Definition: tr.Definition,
		}
	}
	return dto
}

// historyEntryToDTO converts a history.Entry to its wire form. PR #7
// requires the engine field to be populated.
func historyEntryToDTO(e history.Entry) historyEntryDTO {
	tags := e.Tags
	if tags == nil {
		tags = []string{}
	}
	return historyEntryDTO{
		ID:           e.ID,
		UserID:       e.UserID,
		ConnectionID: string(e.ConnectionID),
		SQL:          e.SQL,
		Class:        e.Class.String(),
		Tags:         tags,
		Starred:      e.Starred,
		DurationMS:   e.DurationMS,
		RowsReturned: e.RowsReturned,
		Error:        e.Error,
		Executed:     e.Executed,
		Engine:       e.Engine.String(),
	}
}

// auditEventToDTO converts a dbadmin.Event to its wire form.
func auditEventToDTO(e dbadmin.Event) auditEventDTO {
	return auditEventDTO{
		EventID:            e.EventID,
		Timestamp:          e.Timestamp,
		UserID:             e.UserID,
		UserRoleAtTime:     e.UserRoleAtTime.String(),
		SourceIP:           e.SourceIP,
		UserAgentHash:      e.UserAgentHash,
		Action:             string(e.Action),
		Target:             e.Target.String(),
		Statement:          e.Statement,
		ParametersRedacted: e.ParametersRedacted,
		ResultRows:         e.ResultRows,
		DurationMS:         e.DurationMS,
		Error:              e.Error,
		StepUpJTI:          e.StepUpJTI,
		PrevEventHash:      e.PrevEventHash,
	}
}

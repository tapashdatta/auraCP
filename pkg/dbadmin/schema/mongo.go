// Package schema — MongoDB reader.
//
// Mongo is schema-less; the catalog surface that mysqlReader and
// postgresReader build from information_schema / pg_catalog has no
// direct analog. mongoReader produces a best-effort approximation by:
//
//   - mapping databases verbatim (ListDatabases, ListDatabaseNames);
//   - collapsing the SQL "schema" concept onto the single owning
//     database (ListSchemas returns []string{database}, mirroring
//     mysqlReader.ListSchemas);
//   - treating collections as tables (ListTables) with a sampled
//     row-count from collStats and a synthesized Column list inferred
//     from $sample-ed documents (GetTable);
//   - extracting indexes via the IndexView surface (db.collection.
//     indexes()).
//
// Functions / procedures / triggers do not exist as catalog objects in
// Mongo so those methods return (nil, nil) verbatim — same shape SQL
// callers already handle for engines that don't surface a particular
// object kind.
//
// Identifier validation uses ValidateMongoIdentifier (NOT
// ValidateIdentifier) — Mongo identifiers permit characters the SQL
// identifier regex rejects (notably '.' and '-'), and the relational
// validator's SQL-injection allowlist would refuse legitimate
// collection names.
//
// v0.3.2-F.
package schema

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	mongoopts "go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/driver"
)

// mongoConnAccessor is the contract schema/mongo.go expects from the
// driver.Conn it was handed. The driver.mongoConn type implements it;
// tests may provide a stub.
type mongoConnAccessor interface {
	Client() *mongo.Client
	Database() string
}

// mongoReader is the Reader implementation for MongoDB. It does NOT
// issue raw SQL via the driver.Conn — instead it accesses the
// underlying *mongo.Client via the mongoConnAccessor escape hatch and
// drives Mongo's native catalog APIs directly.
type mongoReader struct {
	client    *mongo.Client
	defaultDB string
	limits    driver.Limits
}

// newMongoReader builds a Reader from a driver.Conn that satisfies the
// mongoConnAccessor contract. Refuses anything else — the caller should
// have used schema.For with a Mongo-engine driver.Conn.
func newMongoReader(c driver.Conn, lim driver.Limits) (*mongoReader, error) {
	acc, ok := c.(mongoConnAccessor)
	if !ok {
		return nil, fmt.Errorf("schema/mongo: driver.Conn does not expose a mongo.Client (got %T)", c)
	}
	return &mongoReader{
		client:    acc.Client(),
		defaultDB: acc.Database(),
		limits:    lim,
	}, nil
}

func (r *mongoReader) Engine() dbadmin.EngineKind { return dbadmin.EngineMongo }

// rtimeout returns the per-call deadline for catalog reads. Mirrors
// mysqlReader/postgresReader.rlimits but applies as a context timeout
// rather than via driver.Limits (Mongo's APIs honour ctx natively).
func (r *mongoReader) rtimeout() time.Duration {
	if r.limits.Timeout > 0 {
		return r.limits.Timeout
	}
	return 30 * time.Second
}

// withTimeout derives a context with the reader's configured timeout.
func (r *mongoReader) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, r.rtimeout())
}

// ListDatabases returns the visible databases on the connected
// deployment, excluding the well-known system databases (admin,
// local, config) by default. Operators with elevated roles can still
// address those directly by name — this filter is just the default
// list view, matching the SQL readers' treatment of
// information_schema / pg_catalog.
func (r *mongoReader) ListDatabases(ctx context.Context) ([]string, error) {
	ctx, cancel := r.withTimeout(ctx)
	defer cancel()
	names, err := r.client.ListDatabaseNames(ctx, bson.D{
		{Key: "name", Value: bson.D{
			{Key: "$nin", Value: []string{"admin", "local", "config"}},
		}},
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

// ListSchemas returns a single-element slice [database] — Mongo has no
// schema concept distinct from database, so the API surface collapses
// onto the SQL "schema" by returning the database name. This mirrors
// mysqlReader.ListSchemas which does the same trick for MySQL.
func (r *mongoReader) ListSchemas(ctx context.Context, database string) ([]string, error) {
	if err := ValidateMongoIdentifier(database); err != nil {
		return nil, err
	}
	return []string{database}, nil
}

// ListTables returns the collections in a database (the "schema"
// argument is treated as the database name). View collections are
// folded in with Kind=KindView; standard collections get Kind=KindTable.
// RowsEstimate is best-effort via collStats — if the operator lacks
// the collStats privilege we fall back to zero rather than refusing
// the listing.
func (r *mongoReader) ListTables(ctx context.Context, schema string) ([]TableSummary, error) {
	if err := ValidateMongoIdentifier(schema); err != nil {
		return nil, err
	}
	ctx, cancel := r.withTimeout(ctx)
	defer cancel()

	db := r.client.Database(schema)
	specs, err := db.ListCollectionSpecifications(ctx, bson.D{})
	if err != nil {
		return nil, err
	}

	out := make([]TableSummary, 0, len(specs))
	for _, s := range specs {
		kind := KindTable
		if s.Type == "view" {
			kind = KindView
		}
		summary := TableSummary{
			Schema: schema,
			Name:   s.Name,
			Kind:   kind,
		}
		// Best-effort row estimate. We use collStats which is
		// available on most deployments; ignore errors so the listing
		// works even on restricted roles.
		if est := r.estimatedCount(ctx, db, s.Name); est >= 0 {
			summary.RowsEstimate = est
		}
		out = append(out, summary)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// estimatedCount returns a fast row estimate (collection.
// EstimatedDocumentCount). Returns -1 on error so the caller can
// distinguish "unknown" from "zero".
func (r *mongoReader) estimatedCount(ctx context.Context, db *mongo.Database, coll string) int64 {
	n, err := db.Collection(coll).EstimatedDocumentCount(ctx)
	if err != nil {
		return -1
	}
	return n
}

// GetTable returns synthesized table metadata for a collection.
// Columns: the union of top-level field names observed across a
// $sample of up to mongoSampleSize documents. The DataType is the
// first non-null BSON type observed for the field (string, int32,
// objectid, datetime, etc.).
// PrimaryKey: always []string{"_id"} — every Mongo collection has
// _id as its implicit PK; rows.UpdateByPK / DeleteByPK depend on this
// fixed mapping.
// Indexes: extracted via IndexView.List.
// ForeignKeys / Triggers: nil (Mongo has neither concept).
func (r *mongoReader) GetTable(ctx context.Context, schema, table string) (*Table, error) {
	if err := ValidateMongoIdentifier(schema); err != nil {
		return nil, err
	}
	if err := ValidateMongoIdentifier(table); err != nil {
		return nil, err
	}

	ctx, cancel := r.withTimeout(ctx)
	defer cancel()

	db := r.client.Database(schema)
	coll := db.Collection(table)

	// Confirm the collection exists; otherwise return ErrTableNotFound
	// for symmetry with the SQL readers.
	names, err := db.ListCollectionNames(ctx, bson.D{{Key: "name", Value: table}})
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, ErrTableNotFound
	}

	tbl := &Table{
		Schema:     schema,
		Name:       table,
		Kind:       KindTable,
		PrimaryKey: []string{"_id"},
		Extras:     map[string]string{},
	}

	// Inspect the collection type (view / timeseries / capped) via
	// listCollections specifications.
	specs, err := db.ListCollectionSpecifications(ctx, bson.D{{Key: "name", Value: table}})
	if err == nil && len(specs) > 0 {
		s := specs[0]
		if s.Type == "view" {
			tbl.Kind = KindView
			tbl.Extras["collectionType"] = "view"
		} else if s.Type != "" {
			tbl.Extras["collectionType"] = s.Type
		}
	}

	// Column inference from a $sample of documents.
	tbl.Columns = inferMongoColumns(ctx, coll)

	// Always present _id as PK column; ensure it's in the column list
	// even if no sampled doc had a non-null _id (every doc has _id
	// post-insert, but defensive).
	if !columnNamePresent(tbl.Columns, "_id") {
		tbl.Columns = append([]Column{{
			Name:         "_id",
			Position:     1,
			DataType:     "objectId",
			Nullable:     false,
			IsPrimaryKey: true,
		}}, tbl.Columns...)
	} else {
		for i := range tbl.Columns {
			if tbl.Columns[i].Name == "_id" {
				tbl.Columns[i].IsPrimaryKey = true
				tbl.Columns[i].Nullable = false
			}
		}
	}

	// Indexes via IndexView.
	tbl.Indexes = readMongoIndexes(ctx, coll)

	return tbl, nil
}

// mongoSampleSize is the document count $sample requests when inferring
// the synthesized column list. 100 is enough to surface the common
// fields without making GetTable expensive on huge collections.
const mongoSampleSize = 100

// inferMongoColumns runs an aggregation pipeline with $sample to
// collect up to mongoSampleSize documents and returns the union of
// top-level field names + their first observed BSON type.
//
// Errors short-circuit to an empty column list — the caller still
// returns a non-nil *Table with PrimaryKey populated, so the operator
// UI degrades gracefully when sampling is forbidden by role.
func inferMongoColumns(ctx context.Context, coll *mongo.Collection) []Column {
	pipeline := mongo.Pipeline{
		{{Key: "$sample", Value: bson.D{{Key: "size", Value: mongoSampleSize}}}},
	}
	cur, err := coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil
	}
	defer cur.Close(ctx)

	type colInfo struct {
		name     string
		dataType string
		nullable bool
		position int
	}
	seen := map[string]*colInfo{}
	var order []string

	for cur.Next(ctx) {
		var doc bson.D
		if err := cur.Decode(&doc); err != nil {
			continue
		}
		for _, e := range doc {
			info, ok := seen[e.Key]
			if !ok {
				info = &colInfo{
					name:     e.Key,
					position: len(order) + 1,
				}
				seen[e.Key] = info
				order = append(order, e.Key)
			}
			if e.Value == nil {
				info.nullable = true
				continue
			}
			if info.dataType == "" {
				info.dataType = bsonTypeName(e.Value)
			}
		}
	}

	out := make([]Column, 0, len(order))
	for _, name := range order {
		info := seen[name]
		dt := info.dataType
		if dt == "" {
			dt = "null"
		}
		out = append(out, Column{
			Name:     info.name,
			Position: info.position,
			DataType: dt,
			Nullable: info.nullable,
		})
	}
	return out
}

// bsonTypeName returns the canonical BSON type name for an observed
// document value. Used by the column-inference path to populate
// Column.DataType. Names follow the convention surfaced by the
// `typeof` operator on the BSON value (string, int, long, double,
// objectId, date, decimal, array, object, bool, binData).
func bsonTypeName(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "bool"
	case int32:
		return "int"
	case int64:
		return "long"
	case int:
		return "long"
	case float64:
		return "double"
	case float32:
		return "double"
	case bson.ObjectID:
		return "objectId"
	case bson.DateTime:
		return "date"
	case time.Time:
		return "date"
	case bson.Decimal128:
		return "decimal"
	case bson.D, bson.M:
		return "object"
	case bson.A, []any:
		return "array"
	case []byte:
		return "binData"
	default:
		_ = x
		return fmt.Sprintf("%T", v)
	}
}

// readMongoIndexes returns the index list for a collection. Each
// IndexView.List entry surfaces its keys map (column → direction) and
// options; we project that onto schema.Index, preserving the method
// hint (BTREE, HASHED, 2DSPHERE, TEXT) verbatim when present.
//
// Errors short-circuit to nil — operators without listIndexes
// privilege still get a usable *Table from GetTable.
func readMongoIndexes(ctx context.Context, coll *mongo.Collection) []Index {
	cur, err := coll.Indexes().List(ctx)
	if err != nil {
		return nil
	}
	defer cur.Close(ctx)

	var out []Index
	for cur.Next(ctx) {
		var spec bson.M
		if err := cur.Decode(&spec); err != nil {
			continue
		}
		idx := Index{}
		if name, ok := spec["name"].(string); ok {
			idx.Name = name
		}
		idx.Primary = idx.Name == "_id_"
		if uniq, ok := spec["unique"].(bool); ok {
			idx.Unique = uniq
		}
		if idx.Primary {
			idx.Unique = true
		}
		// Default method is BTREE for ordered indexes; preserve
		// HASHED / 2D / 2DSPHERE / TEXT when a key value advertises
		// it.
		method := "BTREE"
		if keys, ok := spec["key"].(bson.M); ok {
			cols := make([]string, 0, len(keys))
			for k, v := range keys {
				cols = append(cols, k)
				switch vv := v.(type) {
				case string:
					switch vv {
					case "hashed":
						method = "HASHED"
					case "2d":
						method = "2D"
					case "2dsphere":
						method = "2DSPHERE"
					case "text":
						method = "TEXT"
					}
				}
			}
			sort.Strings(cols)
			idx.Columns = cols
		}
		idx.Method = method
		out = append(out, idx)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ListViews returns Mongo's view collections in the supplied
// "schema" (database). The view's pipeline definition is rendered as
// a JSON-ish bson.D representation so the operator UI can show what
// the view computes.
func (r *mongoReader) ListViews(ctx context.Context, schema string) ([]ViewSummary, error) {
	if err := ValidateMongoIdentifier(schema); err != nil {
		return nil, err
	}
	ctx, cancel := r.withTimeout(ctx)
	defer cancel()

	db := r.client.Database(schema)
	specs, err := db.ListCollectionSpecifications(ctx, bson.D{
		{Key: "type", Value: "view"},
	})
	if err != nil {
		return nil, err
	}
	out := make([]ViewSummary, 0, len(specs))
	for _, s := range specs {
		v := ViewSummary{
			Schema: schema,
			Name:   s.Name,
		}
		// The view's source pipeline is in s.Options under
		// {viewOn, pipeline}; render as the JSON representation for
		// the operator UI.
		if s.Options != nil {
			v.Definition = string(s.Options)
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ListFunctions: Mongo has no stored-function concept. Return nil so
// the operator UI hides the section.
func (r *mongoReader) ListFunctions(ctx context.Context, schema string) ([]FunctionSummary, error) {
	if err := ValidateMongoIdentifier(schema); err != nil {
		return nil, err
	}
	return nil, nil
}

// ListProcedures: Mongo has no stored-procedure concept.
func (r *mongoReader) ListProcedures(ctx context.Context, schema string) ([]ProcedureSummary, error) {
	if err := ValidateMongoIdentifier(schema); err != nil {
		return nil, err
	}
	return nil, nil
}

// ListTriggers: Mongo has no trigger concept (change streams are not
// triggers in the SQL sense — they're consumer-driven, not catalog
// objects).
func (r *mongoReader) ListTriggers(ctx context.Context, schema string) ([]TriggerSummary, error) {
	if err := ValidateMongoIdentifier(schema); err != nil {
		return nil, err
	}
	return nil, nil
}

// columnNamePresent reports whether a column with the given name is
// already in cols (case-sensitive). Used by GetTable's _id-guarantee
// path.
func columnNamePresent(cols []Column, name string) bool {
	for _, c := range cols {
		if c.Name == name {
			return true
		}
	}
	return false
}

// Ensure the unused import for mongoopts is kept if future Mongo work
// reuses it (current implementation does not surface MongoDB
// options.* types).
var _ = mongoopts.Find

// Ensure errors is referenced so future error wrapping in Mongo paths
// has an import in place.
var _ = errors.New

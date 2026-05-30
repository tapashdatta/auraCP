// Package rows — MongoDB backend for Operator.
//
// The relational backends (build.go) translate ReadOpts / Predicate
// onto a parameterized SQL string + driver bind args. MongoDB has no
// SQL surface: filter / sort / projection are passed as BSON documents
// to mongo.Collection.Find / CountDocuments / UpdateOne / DeleteOne /
// InsertOne.
//
// The Operator public surface (Read, Count, UpdateByPK, DeleteByPK,
// Insert) stays identical for callers — Operator.Read dispatches here
// when o.engine == dbadmin.EngineMongo. Identifier validation switches
// from ValidateIdentifier (SQL allowlist) to ValidateMongoIdentifier
// (BSON-key allowlist) because Mongo names legitimately contain '.'
// and '-' that the SQL validator would reject.
//
// Predicate mapping:
//
//	OpEq/Neq/Lt/Lte/Gt/Gte → $eq/$ne/$lt/$lte/$gt/$gte
//	OpLike                 → $regex with %→.* and _→. translation
//	OpILike                → $regex with $options:"i" (case-insensitive)
//	OpIsNull / IsNotNull   → {col: nil} / {col: {$ne: nil}}
//	OpIn / OpNotIn         → $in / $nin
//
// LIKE / ILIKE divergence: the SQL wildcard set is {%, _}; the regex
// translation escapes every other regex meta-character so a literal
// dot or asterisk in the operator-supplied pattern matches itself, not
// every-char-or-zero-or-more. Anchors ^ and $ are added so the match
// is total (mirrors SQL LIKE which is full-string by definition).
//
// PK enforcement: the Mongo PK is always "_id". UpdateByPK +
// DeleteByPK preflight via the schema reader (which fixes
// PrimaryKey=["_id"]), so the same ErrPKMismatch / ErrNoPrimaryKey
// contracts apply.
//
// Insert: when opts.Values has no _id, MongoDB generates an ObjectID
// server-side. LastInsertID stays 0 (the result is not an integer);
// LastInsertKey carries the 24-char ObjectID hex so the UI can refresh
// the just-inserted row.
//
// v0.3.2-F.
package rows

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	mongoopts "go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/auracp/auracp/pkg/dbadmin/driver"
	"github.com/auracp/auracp/pkg/dbadmin/schema"
)

// mongoConnAccessor mirrors the contract in schema/mongo.go — the
// driver.mongoConn (from pkg/dbadmin/driver) implements it. We
// duplicate the interface declaration here because Go packages can't
// share unexported interfaces; both files have a structural copy
// that's satisfied by the same concrete type.
type mongoConnAccessor interface {
	Client() *mongo.Client
	Database() string
}

// mongoClient returns the *mongo.Client backing this Operator's
// driver.Conn. Returns an error if the Conn is not a Mongo-backed
// connection — callers should have routed via the engine dispatch
// before reaching this helper.
func (o *Operator) mongoClient() (*mongo.Client, error) {
	acc, ok := o.conn.(mongoConnAccessor)
	if !ok {
		return nil, fmt.Errorf("rows/mongo: driver.Conn does not expose a mongo.Client (got %T)", o.conn)
	}
	return acc.Client(), nil
}

// applyLimitsCtx returns a context with the Operator's query timeout
// applied. Mongo APIs honour ctx natively so we don't need to thread a
// separate driver.Limits through.
func (o *Operator) applyLimitsCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if o.limits.Timeout > 0 {
		return context.WithTimeout(ctx, o.limits.Timeout)
	}
	return ctx, func() {}
}

// ─── Predicate → BSON filter ─────────────────────────────────────────

// buildMongoFilter translates a slice of AND-combined Predicates onto
// a bson.D filter document. Returns ErrInvalidPredicate on any
// malformed clause (mirrors the SQL path's contract).
func buildMongoFilter(filter []Predicate) (bson.D, error) {
	out := bson.D{}
	for _, p := range filter {
		clause, err := predicateToBSON(p)
		if err != nil {
			return nil, err
		}
		out = append(out, clause)
	}
	return out, nil
}

// predicateToBSON returns a single {column: <expr>} bson.E for one
// Predicate. The shape varies by Op:
//
//	OpEq          → {col: val}
//	OpNeq..OpGte  → {col: {$ne|$lt|$lte|$gt|$gte: val}}
//	OpLike/ILike  → {col: {$regex: pattern, $options: "i"?}}
//	OpIsNull      → {col: nil}
//	OpIsNotNull   → {col: {$ne: nil}}
//	OpIn / OpNotIn → {col: {$in|$nin: [vals]}}
func predicateToBSON(p Predicate) (bson.E, error) {
	switch p.Op {
	case OpEq:
		return bson.E{Key: p.Column, Value: p.Value}, nil
	case OpNeq:
		return bson.E{Key: p.Column, Value: bson.D{{Key: "$ne", Value: p.Value}}}, nil
	case OpLt:
		return bson.E{Key: p.Column, Value: bson.D{{Key: "$lt", Value: p.Value}}}, nil
	case OpLte:
		return bson.E{Key: p.Column, Value: bson.D{{Key: "$lte", Value: p.Value}}}, nil
	case OpGt:
		return bson.E{Key: p.Column, Value: bson.D{{Key: "$gt", Value: p.Value}}}, nil
	case OpGte:
		return bson.E{Key: p.Column, Value: bson.D{{Key: "$gte", Value: p.Value}}}, nil

	case OpLike, OpILike:
		s, ok := p.Value.(string)
		if !ok {
			return bson.E{}, fmt.Errorf("%w: LIKE/ILIKE value must be a string (got %T)", ErrInvalidPredicate, p.Value)
		}
		pattern := likeToRegex(s)
		expr := bson.D{{Key: "$regex", Value: pattern}}
		if p.Op == OpILike {
			expr = append(expr, bson.E{Key: "$options", Value: "i"})
		}
		return bson.E{Key: p.Column, Value: expr}, nil

	case OpIsNull:
		return bson.E{Key: p.Column, Value: nil}, nil
	case OpIsNotNull:
		return bson.E{Key: p.Column, Value: bson.D{{Key: "$ne", Value: nil}}}, nil

	case OpIn, OpNotIn:
		values, err := flattenInValue(p.Value)
		if err != nil {
			return bson.E{}, fmt.Errorf("%w: %v", ErrInvalidPredicate, err)
		}
		if len(values) > maxInListSize {
			return bson.E{}, fmt.Errorf("%w: IN list has %d entries (max %d)",
				ErrInvalidPredicate, len(values), maxInListSize)
		}
		if len(values) == 0 {
			if p.Op == OpNotIn {
				return bson.E{}, fmt.Errorf("%w: NOT IN with empty list (would match every doc)",
					ErrInvalidPredicate)
			}
			// Empty IN: emit a filter that matches nothing.
			return bson.E{Key: p.Column, Value: bson.D{{Key: "$in", Value: []any{}}}}, nil
		}
		opKey := "$in"
		if p.Op == OpNotIn {
			opKey = "$nin"
		}
		return bson.E{Key: p.Column, Value: bson.D{{Key: opKey, Value: values}}}, nil
	}
	return bson.E{}, fmt.Errorf("%w: unknown op %q", ErrInvalidPredicate, p.Op)
}

// likeToRegex translates a SQL LIKE pattern into an anchored regex.
// `%` → `.*`, `_` → `.`. Every other regex meta-character is escaped.
// SQL backslash-escaping (`\%` etc.) is NOT supported in this initial
// cut — the relational engines treat escape semantics as collation-
// dependent and the rows API doesn't expose a LIKE ESCAPE clause yet.
func likeToRegex(pat string) string {
	var b strings.Builder
	b.WriteByte('^')
	for i := 0; i < len(pat); i++ {
		switch c := pat[i]; c {
		case '%':
			b.WriteString(".*")
		case '_':
			b.WriteString(".")
		// Regex meta-characters that must be escaped to be matched as
		// literals; the SQL LIKE wildcards % and _ are handled above.
		case '.', '\\', '+', '*', '?', '(', ')', '[', ']', '{', '}', '^', '$', '|', '/':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('$')
	return b.String()
}

// ─── Read ────────────────────────────────────────────────────────────

// mongoRead implements Operator.Read for the Mongo backend. The result
// columns come from the schema reader's GetTable (so the projection
// order matches what the operator already sees for the table in the
// inspector pane); rows are decoded as bson.D and projected into
// []any in column order.
func (o *Operator) mongoRead(ctx context.Context, opts ReadOpts) (*ReadResult, error) {
	if err := schema.ValidateMongoIdentifier(opts.Schema); err != nil {
		return nil, err
	}
	if err := schema.ValidateMongoIdentifier(opts.Table); err != nil {
		return nil, err
	}
	for _, c := range opts.Columns {
		if err := schema.ValidateMongoIdentifier(c); err != nil {
			return nil, err
		}
	}
	for _, s := range opts.Sort {
		if err := schema.ValidateMongoIdentifier(s.Column); err != nil {
			return nil, err
		}
	}
	for _, p := range opts.Filter {
		if err := schema.ValidateMongoIdentifier(p.Column); err != nil {
			return nil, err
		}
		if err := validateOp(p.Op); err != nil {
			return nil, err
		}
	}
	limit := opts.Limit
	if limit < 0 {
		return nil, fmt.Errorf("rows: Limit must be >= 0 (got %d)", limit)
	}
	if limit == 0 {
		limit = o.limits.MaxRows
	}
	if limit > o.limits.MaxRows {
		return nil, fmt.Errorf("%w: limit %d exceeds max %d", ErrRowCapExceeded, limit, o.limits.MaxRows)
	}
	if opts.Offset < 0 {
		return nil, fmt.Errorf("rows: offset must be >= 0 (got %d)", opts.Offset)
	}
	if opts.Offset > maxOffset {
		return nil, fmt.Errorf("rows: offset %d exceeds maxOffset %d (use a tighter Filter)", opts.Offset, maxOffset)
	}

	cli, err := o.mongoClient()
	if err != nil {
		return nil, err
	}

	// Resolve column list from the schema reader, mirroring the SQL
	// path's "no SELECT *" stance — the order must be stable across
	// reads even if a new field appears in some documents.
	cols := opts.Columns
	if len(cols) == 0 {
		t, err := o.schema.GetTable(ctx, opts.Schema, opts.Table)
		if err != nil {
			return nil, err
		}
		cols = make([]string, 0, len(t.Columns))
		for _, c := range t.Columns {
			cols = append(cols, c.Name)
		}
	}

	filter, err := buildMongoFilter(opts.Filter)
	if err != nil {
		return nil, err
	}

	findOpts := mongoopts.Find()
	// Request limit+1 so we can detect a capped result the same way
	// the SQL path does.
	sqlLimit := int64(limit + 1)
	findOpts = findOpts.SetLimit(sqlLimit)
	if opts.Offset > 0 {
		findOpts = findOpts.SetSkip(int64(opts.Offset))
	}
	if len(opts.Sort) > 0 {
		sortDoc := bson.D{}
		for _, sk := range opts.Sort {
			dir := 1
			if sk.Descending {
				dir = -1
			}
			sortDoc = append(sortDoc, bson.E{Key: sk.Column, Value: dir})
		}
		findOpts = findOpts.SetSort(sortDoc)
	}
	// Projection: include each requested column. Mongo's projection
	// is column-NAME based, not column-position based; we still emit
	// rows in `cols` order below regardless of how Mongo orders fields
	// in the returned document.
	if len(cols) > 0 {
		projection := bson.D{}
		for _, c := range cols {
			projection = append(projection, bson.E{Key: c, Value: 1})
		}
		findOpts = findOpts.SetProjection(projection)
	}

	ctx, cancel := o.applyLimitsCtx(ctx)
	defer cancel()

	coll := cli.Database(opts.Schema).Collection(opts.Table)
	cur, err := coll.Find(ctx, filter, findOpts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)

	// ColumnInfo: synthesize from the column list. DataType is left
	// as the BSON type observed for the first row (best-effort).
	res := &ReadResult{
		Columns: make([]driver.ColumnInfo, len(cols)),
	}
	for i, c := range cols {
		res.Columns[i] = driver.ColumnInfo{
			Name:       c,
			PrimaryKey: c == "_id",
		}
	}

	var rowCount int
	for cur.Next(ctx) {
		var doc bson.M
		if err := cur.Decode(&doc); err != nil {
			return nil, err
		}
		row := make([]any, len(cols))
		for i, c := range cols {
			row[i] = normalizeMongoValue(doc[c])
		}
		res.Rows = append(res.Rows, row)
		rowCount++
		if rowCount >= int(sqlLimit) {
			break
		}
	}
	if err := cur.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return nil, err
	}
	if len(res.Rows) > limit {
		res.Rows = res.Rows[:limit]
		res.Capped = true
	}
	return res, nil
}

// normalizeMongoValue maps a decoded BSON value onto the documented
// rows-package types (string, int64, float64, bool, []byte, time.Time,
// nil) — best-effort, matching driver.Rows's "everything else → string"
// fallback for unrecognized types.
func normalizeMongoValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case bson.ObjectID:
		return x.Hex()
	case bson.DateTime:
		return x.Time().UTC()
	case bson.Decimal128:
		return x.String()
	case int32:
		return int64(x)
	case int:
		return int64(x)
	case bson.D, bson.M, bson.A, []any:
		// Embedded documents / arrays: stringify via the BSON
		// extended-JSON form. The frontend's grid renders this as a
		// raw JSON cell.
		if data, err := bson.MarshalExtJSON(x, false, false); err == nil {
			return string(data)
		}
		return fmt.Sprintf("%v", x)
	default:
		return v
	}
}

// ─── Count ───────────────────────────────────────────────────────────

// mongoCount implements Operator.CountByOpts for the Mongo backend.
// Uses CountDocuments (an accurate aggregation-based count, not the
// fast collStats estimate — which is too stale for the grid footer).
func (o *Operator) mongoCount(ctx context.Context, opts CountOpts) (int64, error) {
	if err := schema.ValidateMongoIdentifier(opts.Schema); err != nil {
		return 0, err
	}
	if err := schema.ValidateMongoIdentifier(opts.Table); err != nil {
		return 0, err
	}
	for _, p := range opts.Filter {
		if err := schema.ValidateMongoIdentifier(p.Column); err != nil {
			return 0, err
		}
		if err := validateOp(p.Op); err != nil {
			return 0, err
		}
	}
	cli, err := o.mongoClient()
	if err != nil {
		return 0, err
	}
	filter, err := buildMongoFilter(opts.Filter)
	if err != nil {
		return 0, err
	}
	ctx, cancel := o.applyLimitsCtx(ctx)
	defer cancel()

	coll := cli.Database(opts.Schema).Collection(opts.Table)
	n, err := coll.CountDocuments(ctx, filter)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// ─── UpdateByPK ──────────────────────────────────────────────────────

// mongoUpdateByPK implements Operator.UpdateByPK. The Mongo PK is
// always "_id"; we still preflight via the schema reader so the
// caller's PK map structure (one key, "_id") matches the table's
// declared PK and gets the same ErrPKMismatch / ErrNoPrimaryKey
// surface as the SQL path.
//
// Optimistic concurrency: opts.Where is folded into the UpdateOne
// filter as additional {col: val} predicates. Mongo's update is
// single-document atomic — a snapshot mismatch results in zero
// modified rows and ErrConcurrentModification, mirroring the SQL
// path's edit-1 contract.
func (o *Operator) mongoUpdateByPK(ctx context.Context, opts UpdateByPKOpts) (*UpdateResult, error) {
	if err := schema.ValidateMongoIdentifier(opts.Schema); err != nil {
		return nil, err
	}
	if err := schema.ValidateMongoIdentifier(opts.Table); err != nil {
		return nil, err
	}
	if len(opts.Set) == 0 {
		return nil, ErrEmptyUpdate
	}
	for k := range opts.Set {
		if err := schema.ValidateMongoIdentifier(k); err != nil {
			return nil, err
		}
	}
	for k := range opts.PK {
		if err := schema.ValidateMongoIdentifier(k); err != nil {
			return nil, err
		}
	}
	for k := range opts.Where {
		if err := schema.ValidateMongoIdentifier(k); err != nil {
			return nil, err
		}
	}
	for k, v := range opts.Set {
		if err := o.checkValueSize(k, v); err != nil {
			return nil, err
		}
	}

	t, err := o.schema.GetTable(ctx, opts.Schema, opts.Table)
	if err != nil {
		return nil, err
	}
	if len(t.PrimaryKey) == 0 {
		return nil, fmt.Errorf("%w: %s.%s has no primary key", ErrNoPrimaryKey, opts.Schema, opts.Table)
	}
	if err := assertPKMatch(t.PrimaryKey, opts.PK); err != nil {
		return nil, err
	}
	pkSet := map[string]bool{}
	for _, c := range t.PrimaryKey {
		pkSet[c] = true
	}
	for k := range opts.Set {
		if pkSet[k] {
			return nil, fmt.Errorf("%w: field %q is part of the primary key", ErrPKMutation, k)
		}
	}

	cli, err := o.mongoClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := o.applyLimitsCtx(ctx)
	defer cancel()

	filter := bson.D{}
	for _, c := range t.PrimaryKey {
		v, ok := opts.PK[c]
		if !ok {
			return nil, fmt.Errorf("%w", ErrPKMismatch)
		}
		filter = append(filter, bson.E{Key: c, Value: coerceObjectID(c, v)})
	}
	for k, v := range opts.Where {
		if v == nil {
			filter = append(filter, bson.E{Key: k, Value: nil})
			continue
		}
		filter = append(filter, bson.E{Key: k, Value: v})
	}

	setDoc := bson.D{}
	for k, v := range opts.Set {
		setDoc = append(setDoc, bson.E{Key: k, Value: v})
	}
	update := bson.D{{Key: "$set", Value: setDoc}}

	coll := cli.Database(opts.Schema).Collection(opts.Table)
	res, err := coll.UpdateOne(ctx, filter, update)
	if err != nil {
		return nil, err
	}
	if len(opts.Where) > 0 && res.MatchedCount == 0 {
		return nil, ErrConcurrentModification
	}
	return &UpdateResult{RowsAffected: res.ModifiedCount}, nil
}

// coerceObjectID converts a hex-string PK back into a bson.ObjectID
// when the target field is "_id". The HTTP layer round-trips ObjectIDs
// as hex strings (the rows package surfaces them as such on Read), so
// the operator round-trips a string PK on Update — we need to flip it
// back to an ObjectID so MongoDB's equality match works.
//
// Non-_id fields and non-hex values pass through unchanged.
func coerceObjectID(col string, v any) any {
	if col != "_id" {
		return v
	}
	if s, ok := v.(string); ok {
		if id, err := bson.ObjectIDFromHex(s); err == nil {
			return id
		}
	}
	return v
}

// ─── DeleteByPK ──────────────────────────────────────────────────────

func (o *Operator) mongoDeleteByPK(ctx context.Context, opts DeleteByPKOpts) (*UpdateResult, error) {
	if err := schema.ValidateMongoIdentifier(opts.Schema); err != nil {
		return nil, err
	}
	if err := schema.ValidateMongoIdentifier(opts.Table); err != nil {
		return nil, err
	}
	for k := range opts.PK {
		if err := schema.ValidateMongoIdentifier(k); err != nil {
			return nil, err
		}
	}

	t, err := o.schema.GetTable(ctx, opts.Schema, opts.Table)
	if err != nil {
		return nil, err
	}
	if len(t.PrimaryKey) == 0 {
		return nil, fmt.Errorf("%w: %s.%s has no primary key", ErrNoPrimaryKey, opts.Schema, opts.Table)
	}
	if err := assertPKMatch(t.PrimaryKey, opts.PK); err != nil {
		return nil, err
	}

	cli, err := o.mongoClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := o.applyLimitsCtx(ctx)
	defer cancel()

	filter := bson.D{}
	for _, c := range t.PrimaryKey {
		v, ok := opts.PK[c]
		if !ok {
			return nil, fmt.Errorf("%w", ErrPKMismatch)
		}
		filter = append(filter, bson.E{Key: c, Value: coerceObjectID(c, v)})
	}

	coll := cli.Database(opts.Schema).Collection(opts.Table)
	res, err := coll.DeleteOne(ctx, filter)
	if err != nil {
		return nil, err
	}
	return &UpdateResult{RowsAffected: res.DeletedCount}, nil
}

// ─── Insert ──────────────────────────────────────────────────────────

// mongoInsert implements Operator.Insert for Mongo. When opts.Values
// contains no "_id", MongoDB generates an ObjectID server-side; we
// surface its hex via UpdateResult.LastInsertKey so the UI can refresh
// the just-inserted row. LastInsertID stays 0 because the auto-
// generated key is not integer-shaped.
func (o *Operator) mongoInsert(ctx context.Context, opts InsertOpts) (*UpdateResult, error) {
	if err := schema.ValidateMongoIdentifier(opts.Schema); err != nil {
		return nil, err
	}
	if err := schema.ValidateMongoIdentifier(opts.Table); err != nil {
		return nil, err
	}
	if len(opts.Values) == 0 {
		return nil, fmt.Errorf("rows: insert requires at least one field value")
	}
	for k := range opts.Values {
		if err := schema.ValidateMongoIdentifier(k); err != nil {
			return nil, err
		}
	}
	for k, v := range opts.Values {
		if err := o.checkValueSize(k, v); err != nil {
			return nil, err
		}
	}

	cli, err := o.mongoClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := o.applyLimitsCtx(ctx)
	defer cancel()

	doc := bson.D{}
	for k, v := range opts.Values {
		if k == "_id" {
			doc = append(doc, bson.E{Key: k, Value: coerceObjectID(k, v)})
			continue
		}
		doc = append(doc, bson.E{Key: k, Value: v})
	}

	coll := cli.Database(opts.Schema).Collection(opts.Table)
	res, err := coll.InsertOne(ctx, doc)
	if err != nil {
		return nil, err
	}
	out := &UpdateResult{RowsAffected: 1}
	switch id := res.InsertedID.(type) {
	case bson.ObjectID:
		out.LastInsertKey = id.Hex()
	case string:
		out.LastInsertKey = id
	case int32:
		out.LastInsertID = int64(id)
	case int64:
		out.LastInsertID = id
	case int:
		out.LastInsertID = int64(id)
	}
	return out, nil
}

// Ensure the regexp import stays anchored even when likeToRegex's
// implementation doesn't compile a regex at build time (regexp is
// kept available so test helpers and future predicate expansions can
// share the same import).
var _ = regexp.MustCompile

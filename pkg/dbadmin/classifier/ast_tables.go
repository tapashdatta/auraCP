package classifier

import "github.com/auracp/auracp/pkg/dbadmin"

// tableCollector is a small order-preserving set of dbadmin.Target.
// Per ast_tables.go contract: ConnectionID is left empty by the
// classifier; callers populate it from request context.
type tableCollector struct {
	seen  map[targetKey]struct{}
	order []dbadmin.Target
}

type targetKey struct {
	schema string
	object string
}

func newTableCollector() *tableCollector {
	return &tableCollector{seen: map[targetKey]struct{}{}}
}

// add records a (schema, object) pair. Duplicates are dropped silently;
// the first sighting wins for ordering. Empty object names (which can
// surface from partial pg_query AST nodes) are skipped — a Target with
// empty Object is not useful for per-table authorization.
func (c *tableCollector) add(schema, object string) {
	if object == "" {
		return
	}
	k := targetKey{schema: schema, object: object}
	if _, ok := c.seen[k]; ok {
		return
	}
	c.seen[k] = struct{}{}
	c.order = append(c.order, dbadmin.Target{
		// ConnectionID intentionally empty; caller fills it.
		Schema: schema,
		Object: object,
	})
}

// targets returns the accumulated tables, nil when none were added.
// The slice is fresh; mutating it does not affect the collector.
func (c *tableCollector) targets() []dbadmin.Target {
	if len(c.order) == 0 {
		return nil
	}
	out := make([]dbadmin.Target, len(c.order))
	copy(out, c.order)
	return out
}

package driver

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
)

// Limits carries the resource caps applied to a single Query call. The
// engine populates it from Config.Query before invoking the driver; the
// driver wraps Rows with a LimitedRows that enforces row + byte caps
// without the underlying database/sql.Rows or pgx Rows knowing.
//
// Zero values mean "no limit" — useful in tests, never in production.
// The engine refuses to invoke with a zero Limits in production builds.
type Limits struct {
	// Timeout is the maximum wall-clock duration for the query. The
	// driver wraps the caller's ctx with this deadline. Zero means
	// "use the caller's ctx as-is."
	Timeout time.Duration

	// MaxRows caps the rows returned via Rows.Next(). Hitting it
	// returns ErrCapped; the iterator is otherwise normal up to that
	// point. Zero means unlimited.
	MaxRows int

	// MaxBytes caps the cumulative byte size of all row values
	// returned. Each value's size is estimated via valueSize();
	// hitting the cap returns ErrCapped on the next Next().
	MaxBytes int64
}

// ApplyTimeout returns a derived context with the Limits' Timeout, or
// the original context unchanged when Limits.Timeout is zero. Caller
// MUST defer the returned cancel func to release timer resources.
func (l Limits) ApplyTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if l.Timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, l.Timeout)
}

// LimitedRows wraps a Rows iterator with row + byte caps. The driver
// hands operator code a *LimitedRows from Conn.Query, never the raw
// inner iterator — that's how the cap is structurally enforced.
//
// LimitedRows is NOT safe for concurrent Next calls. Callers must serialize
// access from a single goroutine (the engine's row-streaming handler
// does this naturally). Concurrent calls observe an internal guard that
// returns an error rather than racing.
type LimitedRows struct {
	Inner Rows
	L     Limits

	rows  atomic.Int64
	bytes atomic.Int64
	// active guards against concurrent Next() calls. Set to 1 by an
	// in-flight Next; CAS back to 0 on exit. Second concurrent Next
	// sees active==1 and returns ErrConcurrentNext.
	active atomic.Int32
}

// ErrConcurrentNext is returned by Rows.Next when called concurrently
// against the same LimitedRows. Callers should serialize.
var ErrConcurrentNext = errors.New("driver: concurrent Rows.Next not supported")

// Columns delegates to the underlying iterator.
func (lr *LimitedRows) Columns() []ColumnInfo {
	return lr.Inner.Columns()
}

// Next returns the next row, enforcing both caps.
//
// Cap semantics (post-review):
//   - Row cap: returns ErrCapped before the (MaxRows+1)-th row is read,
//     so the network round-trip is not spent.
//   - Byte cap: returns ErrCapped before the next row is read once the
//     cumulative byte count is >= MaxBytes. The row that PUSHED us over
//     the cap is still returned (cap + one row); the next call trips.
//     A truly oversized single row (MaxBytes < rowSize) is unavoidable
//     in this PR — limits.go cannot inspect a row's size pre-decode.
//     See docs/aura-db/KNOWN-ISSUES.md "per-cell streaming cap" for the
//     deferred PR #3.5 streaming-decode work.
func (lr *LimitedRows) Next(ctx context.Context) ([]any, error) {
	if !lr.active.CompareAndSwap(0, 1) {
		return nil, ErrConcurrentNext
	}
	defer lr.active.Store(0)

	// Row cap pre-check.
	if lr.L.MaxRows > 0 && lr.rows.Load() >= int64(lr.L.MaxRows) {
		return nil, ErrCapped
	}
	// Byte cap pre-check.
	if lr.L.MaxBytes > 0 && lr.bytes.Load() >= lr.L.MaxBytes {
		return nil, ErrCapped
	}

	vals, err := lr.Inner.Next(ctx)
	if err != nil {
		return nil, err
	}
	lr.rows.Add(1)
	if lr.L.MaxBytes > 0 {
		lr.bytes.Add(rowSize(vals))
	}
	return vals, nil
}

// Close delegates to the underlying iterator.
func (lr *LimitedRows) Close() error {
	return lr.Inner.Close()
}

// RowsCounted reports how many rows have been returned through the
// limit wrapper so far.
func (lr *LimitedRows) RowsCounted() int64 {
	return lr.rows.Load()
}

// BytesCounted reports the cumulative byte estimate.
func (lr *LimitedRows) BytesCounted() int64 {
	return lr.bytes.Load()
}

// rowSize returns a cheap byte-size estimate for a row.
func rowSize(vals []any) int64 {
	var sum int64
	for _, v := range vals {
		sum += valueSize(v)
	}
	return sum
}

// valueSize returns a byte estimate for one cell. Conservative: when
// unsure, prefer over-counting. The cap is a guardrail, not an audit
// of bytes-on-wire.
//
// Post-review expansion: pgx native types (map[string]any from JSONB,
// pgtype wrappers, fixed-size [N]byte UUIDs) used to fall into the
// "default 50" arm which made byte caps wildly under-count for typical
// Postgres workloads. We now handle those cases explicitly.
func valueSize(v any) int64 {
	switch x := v.(type) {
	case nil:
		return 4 // "null"
	case string:
		return int64(len(x)) + 2
	case []byte:
		return int64(len(x)) + 2
	case bool:
		return 5
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return 20
	case float32, float64:
		return 24
	case time.Time:
		return 30
	case [16]byte:
		// UUID; pgx returns this for the UUID type.
		return 38
	case []any:
		var sum int64
		for _, e := range x {
			sum += valueSize(e)
		}
		// Include array bracket + comma overhead.
		return sum + int64(2+len(x))
	case map[string]any:
		var sum int64
		for k, vv := range x {
			sum += int64(len(k)) + 3
			sum += valueSize(vv)
		}
		return sum + 2
	default:
		// Fallback: stringify via Sprintf to count bytes — slow path,
		// but only fires on unrecognized types and pessimistic
		// over-counting is acceptable.
		s := fmt.Sprintf("%v", x)
		if len(s) < 50 {
			return 50
		}
		return int64(len(s)) + 2
	}
}

// wrapCtxErr maps a driver-level error to one of this package's typed
// sentinels when the context itself died, and PRESERVES the underlying
// error chain via errors.Join — so callers can still errors.Unwrap to
// inspect pgx / mysql details.
//
// Post-review fix: previous implementation returned the bare sentinel
// (losing diagnostic info) and didn't map context.Canceled. Both fixed
// here.
func wrapCtxErr(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	// Check the context FIRST so a generic "i/o timeout" from the
	// driver doesn't lose the deadline-vs-cancel distinction.
	if ctxErr := ctx.Err(); ctxErr != nil {
		switch {
		case errors.Is(ctxErr, context.DeadlineExceeded):
			return errors.Join(ErrTimeout, err)
		case errors.Is(ctxErr, context.Canceled):
			return errors.Join(ErrCanceled, err)
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.Join(ErrTimeout, err)
	}
	if errors.Is(err, context.Canceled) {
		return errors.Join(ErrCanceled, err)
	}
	return err
}

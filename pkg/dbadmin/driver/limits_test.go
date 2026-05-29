package driver

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestLimits_ApplyTimeout_Zero(t *testing.T) {
	ctx := context.Background()
	out, cancel := Limits{}.ApplyTimeout(ctx)
	defer cancel()
	if out != ctx {
		t.Error("zero Timeout should return the same ctx")
	}
}

func TestLimits_ApplyTimeout_Honors(t *testing.T) {
	ctx, cancel := Limits{Timeout: 10 * time.Millisecond}.ApplyTimeout(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected a deadline")
	}
	delta := time.Until(deadline)
	if delta > 20*time.Millisecond || delta < -5*time.Millisecond {
		t.Errorf("deadline %v is off; want ~10ms in the future", delta)
	}
}

func TestLimitedRows_RowCap(t *testing.T) {
	cols := []ColumnInfo{{Name: "id"}}
	inner := &stubRows{
		rows: [][]any{{1}, {2}, {3}, {4}, {5}},
		cols: cols,
	}
	lr := &LimitedRows{Inner: inner, L: Limits{MaxRows: 3}}
	defer lr.Close()

	ctx := context.Background()
	count := 0
	for {
		_, err := lr.Next(ctx)
		if err == ErrCapped {
			break
		}
		if err == ErrEOF {
			t.Errorf("hit EOF before cap; counted %d rows", count)
			break
		}
		if err != nil {
			t.Fatalf("Next err = %v", err)
		}
		count++
	}
	if count != 3 {
		t.Errorf("got %d rows before cap, want 3", count)
	}
	if lr.RowsCounted() != 3 {
		t.Errorf("RowsCounted = %d, want 3", lr.RowsCounted())
	}
}

func TestLimitedRows_ByteCap(t *testing.T) {
	cols := []ColumnInfo{{Name: "msg"}}
	inner := &stubRows{
		rows: [][]any{
			{"hello world hello world"}, // ~25 bytes
			{"hello world hello world"},
			{"hello world hello world"},
		},
		cols: cols,
	}
	// Cap at 30 bytes — should return 1 row, then trip.
	lr := &LimitedRows{Inner: inner, L: Limits{MaxBytes: 30}}
	defer lr.Close()

	ctx := context.Background()
	rows := 0
	for {
		_, err := lr.Next(ctx)
		if err == ErrCapped {
			break
		}
		if err != nil {
			t.Fatalf("Next err = %v", err)
		}
		rows++
	}
	// Per the doc: byte cap fires on the NEXT call after the
	// cumulative byte count >= cap. Row 1 brings us to ~25 bytes
	// (< 30, no trip); row 2 brings us to ~50 bytes (>= 30); cap
	// fires on the row 3 pre-check. Net: 2 rows returned before
	// the cap.
	if rows != 2 {
		t.Errorf("got %d rows before byte cap, want 2 (one row over is the documented behavior)", rows)
	}
}

func TestLimitedRows_NoLimitsPassesThrough(t *testing.T) {
	cols := []ColumnInfo{{Name: "id"}}
	inner := &stubRows{
		rows: [][]any{{1}, {2}, {3}},
		cols: cols,
	}
	lr := &LimitedRows{Inner: inner, L: Limits{}}
	defer lr.Close()

	count := 0
	for {
		_, err := lr.Next(context.Background())
		if err == ErrEOF {
			break
		}
		if err != nil {
			t.Fatalf("Next err = %v", err)
		}
		count++
	}
	if count != 3 {
		t.Errorf("got %d rows, want 3", count)
	}
}

func TestLimitedRows_ContextCancellation(t *testing.T) {
	cols := []ColumnInfo{{Name: "id"}}
	inner := &stubRows{
		rows:  [][]any{{1}, {2}, {3}},
		cols:  cols,
		delay: 50 * time.Millisecond,
	}
	lr := &LimitedRows{Inner: inner, L: Limits{MaxRows: 10}}
	defer lr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := lr.Next(ctx)
	// wrapCtxErr now wraps the underlying error WITH ErrTimeout
	// via errors.Join, so errors.Is(err, ErrTimeout) is true AND
	// errors.Is(err, context.DeadlineExceeded) is also true. Both
	// invariants matter; assert ErrTimeout (the typed sentinel).
	if err == nil || !errors.Is(err, ErrTimeout) {
		t.Errorf("expected ErrTimeout, got %v", err)
	}
}

func TestLimitedRows_RefusesConcurrentNext(t *testing.T) {
	// Post-review fix: row/byte cap pre-check was racy under
	// concurrent Next. We now serialize via an atomic guard.
	cols := []ColumnInfo{{Name: "id"}}
	inner := &stubRows{
		rows:  [][]any{{1}, {2}, {3}, {4}, {5}},
		cols:  cols,
		delay: 30 * time.Millisecond,
	}
	lr := &LimitedRows{Inner: inner, L: Limits{MaxRows: 10}}
	defer lr.Close()

	// Run two Next() calls concurrently. One must succeed (or block
	// briefly); the other MUST get ErrConcurrentNext.
	type result struct {
		err error
	}
	ch := make(chan result, 2)
	go func() { _, err := lr.Next(context.Background()); ch <- result{err} }()
	time.Sleep(5 * time.Millisecond) // let first Next establish the guard
	_, err := lr.Next(context.Background())
	ch <- result{err}

	r1 := <-ch
	r2 := <-ch
	if !errors.Is(r1.err, ErrConcurrentNext) && !errors.Is(r2.err, ErrConcurrentNext) {
		t.Errorf("expected one of the concurrent Next() calls to return ErrConcurrentNext; got %v / %v", r1.err, r2.err)
	}
}

func TestWrapCtxErr_PreservesChain(t *testing.T) {
	// Post-review fix: wrapCtxErr now uses errors.Join so callers can
	// errors.Unwrap to inspect the underlying driver error.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	time.Sleep(1 * time.Millisecond)
	inner := errors.New("pgx: connection broke")
	out := wrapCtxErr(ctx, inner)
	if !errors.Is(out, ErrTimeout) {
		t.Errorf("wrapCtxErr lost ErrTimeout: %v", out)
	}
	// The underlying error must still be reachable.
	if !strings.Contains(out.Error(), "pgx: connection broke") {
		t.Errorf("wrapCtxErr dropped the underlying error message: %v", out)
	}
}

func TestWrapCtxErr_CanceledMaps(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	inner := errors.New("driver: read failed")
	out := wrapCtxErr(ctx, inner)
	if !errors.Is(out, ErrCanceled) {
		t.Errorf("wrapCtxErr(canceled) didn't map to ErrCanceled: %v", out)
	}
}

func TestValueSize_KnownTypes(t *testing.T) {
	cases := []struct {
		v    any
		want int64
	}{
		{nil, 4},
		{"hello", 7},
		{[]byte("hello"), 7},
		{true, 5},
		{int64(42), 20},
		{float64(3.14), 24},
		{time.Now(), 30},
		{struct{}{}, 50},
	}
	for _, c := range cases {
		if got := valueSize(c.v); got != c.want {
			t.Errorf("valueSize(%T) = %d, want %d", c.v, got, c.want)
		}
	}
}

func TestWrapCtxErr_DeadlineMapping(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	time.Sleep(1 * time.Millisecond)
	out := wrapCtxErr(ctx, ctx.Err())
	if !errors.Is(out, ErrTimeout) {
		t.Errorf("wrapCtxErr(deadline) = %v, want ErrTimeout", out)
	}
}

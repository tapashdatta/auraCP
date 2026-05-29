package driver

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/auracp/auracp/pkg/dbadmin"
)

func TestFor_KnownEngines(t *testing.T) {
	d, err := For(dbadmin.EngineMariaDB)
	if err != nil {
		t.Fatalf("For(MariaDB) err = %v", err)
	}
	if d.Engine() != dbadmin.EngineMariaDB {
		t.Errorf("MariaDB driver Engine = %v", d.Engine())
	}

	d, err = For(dbadmin.EnginePostgres)
	if err != nil {
		t.Fatalf("For(Postgres) err = %v", err)
	}
	if d.Engine() != dbadmin.EnginePostgres {
		t.Errorf("Postgres driver Engine = %v", d.Engine())
	}
}

func TestFor_UnknownEngine(t *testing.T) {
	_, err := For(dbadmin.EngineKind(99))
	if err == nil {
		t.Fatal("expected error for unknown engine")
	}
}

func TestErrors_DistinctSentinels(t *testing.T) {
	errs := []error{ErrEOF, ErrCapped, ErrTimeout, ErrAuth, ErrUnavailable,
		ErrSyntax, ErrPermission, ErrConflict, ErrPoolClosed}
	for i, e1 := range errs {
		for j, e2 := range errs {
			if i == j {
				continue
			}
			if errors.Is(e1, e2) {
				t.Errorf("errors.Is(%v, %v) = true, want false (sentinels must be distinct)", e1, e2)
			}
		}
	}
}

func TestErrWrap_PreservesIs(t *testing.T) {
	// errors built like fmt.Errorf("%w: backend msg", ErrAuth) must
	// still satisfy errors.Is.
	wrapped := errors.Join(ErrAuth, errors.New("login failed"))
	if !errors.Is(wrapped, ErrAuth) {
		t.Error("errors.Join lost ErrAuth identity")
	}
}

func TestNilCredentials_OpenRefuses(t *testing.T) {
	// MySQL driver Open with nil creds returns an error rather than
	// panicking. Same for Postgres.
	mysql := &mysqlDriverImpl{}
	_, err := mysql.Open(context.Background(), &dbadmin.Connection{Engine: dbadmin.EngineMariaDB}, nil, 4)
	if err == nil {
		t.Error("mysql.Open(nil creds) returned no error")
	}

	pg := &postgresDriverImpl{}
	_, err = pg.Open(context.Background(), &dbadmin.Connection{Engine: dbadmin.EnginePostgres}, nil, 4)
	if err == nil {
		t.Error("postgres.Open(nil creds) returned no error")
	}
}

func TestNilConnection_OpenRefuses(t *testing.T) {
	mysql := &mysqlDriverImpl{}
	_, err := mysql.Open(context.Background(), nil, &dbadmin.Credentials{}, 4)
	if err == nil {
		t.Error("mysql.Open(nil conn) returned no error")
	}

	pg := &postgresDriverImpl{}
	_, err = pg.Open(context.Background(), nil, &dbadmin.Credentials{}, 4)
	if err == nil {
		t.Error("postgres.Open(nil conn) returned no error")
	}
}

func TestPgTypeName_KnownOIDs(t *testing.T) {
	cases := []struct {
		oid  uint32
		want string
	}{
		{16, "BOOLEAN"},
		{20, "BIGINT"},
		{23, "INTEGER"},
		{25, "TEXT"},
		{114, "JSON"},
		{1043, "VARCHAR"},
		{1082, "DATE"},
		{1184, "TIMESTAMPTZ"},
		{1700, "NUMERIC"},
		{2950, "UUID"},
		{99999, "UNKNOWN"},
	}
	for _, c := range cases {
		if got := pgTypeName(c.oid); got != c.want {
			t.Errorf("pgTypeName(%d) = %q, want %q", c.oid, got, c.want)
		}
	}
}

// TestQuoteParam was removed — quoteParam is gone now that the Postgres
// driver builds its config via typed setters (pgxpool.ParseConfig("") +
// cfg.ConnConfig field assignment) rather than concatenating into a
// connection string. Post-review fix: keeping the password out of any
// string that pgx could echo back in an error.

func TestPgTypeName_UnknownIsAnonymous(t *testing.T) {
	// Post-review fix: unknown OIDs return "UNKNOWN" (not "oid:N")
	// so we don't leak Postgres catalog OIDs to the browser.
	if got := pgTypeName(99999); got != "UNKNOWN" {
		t.Errorf("pgTypeName(99999) = %q, want %q", got, "UNKNOWN")
	}
}

// PR #4.5: normalizePostgresValue should produce JSON-friendly forms for
// pgx's pgtype wrappers (Numeric / Interval / UUID / Range / etc.).
// Each subtest covers one wrapper and a NULL-valued case.

func TestNormalizePostgresValue_UUID(t *testing.T) {
	in := pgtype.UUID{
		Bytes: [16]byte{0xa0, 0xee, 0xbc, 0x99, 0x9c, 0x0b, 0x4e, 0xf8,
			0xbb, 0x6d, 0x6b, 0xb9, 0xbd, 0x38, 0x0a, 0x11},
		Valid: true,
	}
	want := "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"
	if got := normalizePostgresValue(in); got != want {
		t.Errorf("normalize(pgtype.UUID) = %v, want %q", got, want)
	}
	if got := normalizePostgresValue(pgtype.UUID{Valid: false}); got != nil {
		t.Errorf("normalize(invalid UUID) = %v, want nil", got)
	}
}

func TestNormalizePostgresValue_Numeric(t *testing.T) {
	// 12345.67 — Int=1234567, Exp=-2.
	n := pgtype.Numeric{Int: big.NewInt(1234567), Exp: -2, Valid: true}
	if got := normalizePostgresValue(n); got != "12345.67" {
		t.Errorf("normalize(12345.67) = %v", got)
	}
	// Negative number.
	n = pgtype.Numeric{Int: big.NewInt(-1234567), Exp: -2, Valid: true}
	if got := normalizePostgresValue(n); got != "-12345.67" {
		t.Errorf("normalize(-12345.67) = %v", got)
	}
	// Integer (Exp=0).
	n = pgtype.Numeric{Int: big.NewInt(42), Exp: 0, Valid: true}
	if got := normalizePostgresValue(n); got != "42" {
		t.Errorf("normalize(42) = %v", got)
	}
	// NaN.
	n = pgtype.Numeric{NaN: true, Valid: true}
	if got := normalizePostgresValue(n); got != "NaN" {
		t.Errorf("normalize(NaN) = %v", got)
	}
	// Invalid -> nil.
	if got := normalizePostgresValue(pgtype.Numeric{Valid: false}); got != nil {
		t.Errorf("normalize(invalid Numeric) = %v, want nil", got)
	}
}

func TestNormalizePostgresValue_Interval(t *testing.T) {
	// 1 year, 2 months, 3 days, 4h 5m 6.789s.
	iv := pgtype.Interval{
		Months:       14,
		Days:         3,
		Microseconds: 4*3_600_000_000 + 5*60_000_000 + 6*1_000_000 + 789_000,
		Valid:        true,
	}
	got := normalizePostgresValue(iv)
	// Expect "P1Y2M3DT4H5M6.789000S".
	want := "P1Y2M3DT4H5M6.789000S"
	if got != want {
		t.Errorf("normalize(interval) = %v, want %v", got, want)
	}
	if got := normalizePostgresValue(pgtype.Interval{Valid: false}); got != nil {
		t.Errorf("normalize(invalid interval) = %v, want nil", got)
	}
	// Zero interval -> "PT0S".
	if got := normalizePostgresValue(pgtype.Interval{Valid: true}); got != "PT0S" {
		t.Errorf("normalize(zero interval) = %v, want PT0S", got)
	}
}

func TestNormalizePostgresValue_Int4Range(t *testing.T) {
	in := pgtype.Range[pgtype.Int4]{
		Lower:     pgtype.Int4{Int32: 1, Valid: true},
		Upper:     pgtype.Int4{Int32: 5, Valid: true},
		LowerType: pgtype.Inclusive,
		UpperType: pgtype.Exclusive,
		Valid:     true,
	}
	if got := normalizePostgresValue(in); got != "[1,5)" {
		t.Errorf("normalize(int4range) = %v, want [1,5)", got)
	}
}

func TestNormalizePostgresValue_Primitives(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want any
	}{
		{"bool valid", pgtype.Bool{Bool: true, Valid: true}, true},
		{"bool invalid", pgtype.Bool{Valid: false}, nil},
		{"text valid", pgtype.Text{String: "hi", Valid: true}, "hi"},
		{"text invalid", pgtype.Text{Valid: false}, nil},
		{"int8 valid", pgtype.Int8{Int64: 42, Valid: true}, int64(42)},
		{"int8 invalid", pgtype.Int8{Valid: false}, nil},
		{"float8 valid", pgtype.Float8{Float64: 3.14, Valid: true}, 3.14},
		{"float8 invalid", pgtype.Float8{Valid: false}, nil},
	}
	for _, tc := range cases {
		got := normalizePostgresValue(tc.in)
		if got != tc.want {
			t.Errorf("%s: normalize = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestNormalizePostgresValue_TimePreserved(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	in := time.Date(2026, 5, 30, 12, 0, 0, 0, loc)
	got, ok := normalizePostgresValue(in).(time.Time)
	if !ok {
		t.Fatalf("normalize(time) returned %T", got)
	}
	if got.Location() != time.UTC {
		t.Errorf("normalize(time) location = %v, want UTC", got.Location())
	}
}

package driver

import (
	"context"
	"errors"
	"testing"

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

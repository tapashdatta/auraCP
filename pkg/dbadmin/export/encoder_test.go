package export

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

func TestCSVEncoder_BasicShape(t *testing.T) {
	var buf bytes.Buffer
	enc, err := NewEncoder(&buf, FormatCSV, Options{IncludeHeader: true})
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	if err := enc.WriteHeader([]string{"id", "name"}); err != nil {
		t.Fatal(err)
	}
	if err := enc.WriteRow([]any{int64(1), "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := enc.WriteRow([]any{int64(2), `bob "the builder"`}); err != nil {
		t.Fatal(err)
	}
	if err := enc.WriteRow([]any{nil, ""}); err != nil {
		t.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	want := "id,name\r\n1,alice\r\n2,\"bob \"\"the builder\"\"\"\r\n,\r\n"
	if got != want {
		t.Errorf("CSV mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestCSVEncoder_QuotingAndTypes(t *testing.T) {
	var buf bytes.Buffer
	enc, _ := NewEncoder(&buf, FormatCSV, Options{IncludeHeader: false})
	_ = enc.WriteHeader([]string{"v"})
	// String with comma, CR, LF — must quote.
	_ = enc.WriteRow([]any{"a,b"})
	_ = enc.WriteRow([]any{"line1\nline2"})
	_ = enc.WriteRow([]any{true})
	_ = enc.WriteRow([]any{false})
	_ = enc.WriteRow([]any{[]byte("hi")}) // base64 = "aGk="
	_ = enc.WriteRow([]any{time.Date(2030, 5, 1, 12, 0, 0, 0, time.UTC)})
	_ = enc.Close()
	out := buf.String()
	if !strings.Contains(out, `"a,b"`) {
		t.Errorf("expected quoted comma cell, got: %q", out)
	}
	if !strings.Contains(out, "true\r\n") || !strings.Contains(out, "false\r\n") {
		t.Errorf("expected true/false rows in: %q", out)
	}
	if !strings.Contains(out, "aGk=") {
		t.Errorf("expected base64 cell aGk= in: %q", out)
	}
	if !strings.Contains(out, "2030-05-01T12:00:00Z") {
		t.Errorf("expected RFC3339 timestamp in: %q", out)
	}
}

func TestCSVEncoder_OmitHeader(t *testing.T) {
	var buf bytes.Buffer
	enc, _ := NewEncoder(&buf, FormatCSV, Options{IncludeHeader: false})
	_ = enc.WriteHeader([]string{"a"})
	_ = enc.WriteRow([]any{int64(1)})
	_ = enc.Close()
	if strings.Contains(buf.String(), "a\r\n") {
		t.Errorf("header should be omitted, got: %q", buf.String())
	}
}

func TestNDJSONEncoder_OneObjectPerRow(t *testing.T) {
	var buf bytes.Buffer
	enc, _ := NewEncoder(&buf, FormatNDJSON, Options{})
	_ = enc.WriteHeader([]string{"id", "name", "active"})
	_ = enc.WriteRow([]any{int64(1), "alice", true})
	_ = enc.WriteRow([]any{int64(2), nil, false})
	_ = enc.Close()
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 NDJSON lines, got %d: %q", len(lines), buf.String())
	}
	var r0 map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &r0); err != nil {
		t.Fatalf("invalid JSON line 0: %v", err)
	}
	if r0["id"].(float64) != 1 || r0["name"] != "alice" || r0["active"] != true {
		t.Errorf("row 0 mismatch: %#v", r0)
	}
	var r1 map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &r1); err != nil {
		t.Fatalf("invalid JSON line 1: %v", err)
	}
	if r1["name"] != nil {
		t.Errorf("row 1 name should be null, got: %#v", r1["name"])
	}
}

func TestNDJSONEncoder_PreservesKeyOrder(t *testing.T) {
	var buf bytes.Buffer
	enc, _ := NewEncoder(&buf, FormatNDJSON, Options{})
	_ = enc.WriteHeader([]string{"z", "a", "m"})
	_ = enc.WriteRow([]any{1, 2, 3})
	_ = enc.Close()
	line := strings.TrimRight(buf.String(), "\n")
	if !strings.HasPrefix(line, `{"z":`) {
		t.Errorf("expected line to start with \"z\" key, got: %q", line)
	}
	zi := strings.Index(line, `"z"`)
	ai := strings.Index(line, `"a"`)
	mi := strings.Index(line, `"m"`)
	if !(zi < ai && ai < mi) {
		t.Errorf("expected key order z,a,m in: %q", line)
	}
}

func TestSQLEncoder_MariaDBQuoting(t *testing.T) {
	var buf bytes.Buffer
	enc, err := NewEncoder(&buf, FormatSQL, Options{
		Engine:     dbadmin.EngineMariaDB,
		SchemaName: "myapp",
		TableName:  "users",
		NowFunc:    func() string { return "2030-01-01T00:00:00Z" },
	})
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	_ = enc.WriteHeader([]string{"id", "name", "active"})
	_ = enc.WriteRow([]any{int64(1), "o'brien", true})
	_ = enc.WriteRow([]any{int64(2), nil, false})
	_ = enc.Close()
	got := buf.String()
	if !strings.Contains(got, "-- Aura DB export") {
		t.Errorf("missing header comment in: %q", got)
	}
	if !strings.Contains(got, "NO_BACKSLASH_ESCAPES") {
		t.Errorf("missing MariaDB pragma in: %q", got)
	}
	if !strings.Contains(got, "INSERT INTO `myapp`.`users` (`id`, `name`, `active`) VALUES (1, 'o''brien', 1);") {
		t.Errorf("missing/wrong MariaDB INSERT in: %q", got)
	}
	if !strings.Contains(got, "VALUES (2, NULL, 0);") {
		t.Errorf("missing/wrong NULL INSERT: %q", got)
	}
	if !strings.Contains(got, "-- end: 2 rows") {
		t.Errorf("missing trailing comment in: %q", got)
	}
}

func TestSQLEncoder_PostgresQuoting(t *testing.T) {
	var buf bytes.Buffer
	enc, _ := NewEncoder(&buf, FormatSQL, Options{
		Engine:     dbadmin.EnginePostgres,
		SchemaName: "public",
		TableName:  "events",
		NowFunc:    func() string { return "2030-01-01T00:00:00Z" },
	})
	_ = enc.WriteHeader([]string{"id", "active", "blob"})
	_ = enc.WriteRow([]any{int64(7), true, []byte{0xCA, 0xFE}})
	_ = enc.Close()
	got := buf.String()
	if !strings.Contains(got, `INSERT INTO "public"."events" ("id", "active", "blob") VALUES (7, TRUE, '\xcafe'::bytea);`) {
		t.Errorf("postgres INSERT shape wrong: %q", got)
	}
	if strings.Contains(got, "NO_BACKSLASH_ESCAPES") {
		t.Errorf("postgres should not emit MariaDB pragma: %q", got)
	}
}

func TestSQLEncoder_RejectsBadOptions(t *testing.T) {
	var buf bytes.Buffer
	if _, err := NewEncoder(&buf, FormatSQL, Options{}); err == nil {
		t.Error("expected error for missing Engine + Schema + Table")
	}
	if _, err := NewEncoder(&buf, FormatSQL, Options{Engine: dbadmin.EngineMariaDB}); err == nil {
		t.Error("expected error for missing Schema + Table")
	}
}

func TestEncoder_RowArityCheck(t *testing.T) {
	var buf bytes.Buffer
	enc, _ := NewEncoder(&buf, FormatCSV, Options{IncludeHeader: true})
	_ = enc.WriteHeader([]string{"a", "b"})
	if err := enc.WriteRow([]any{1}); err == nil {
		t.Error("expected arity mismatch error")
	}
}

func TestFormat_Validity(t *testing.T) {
	for _, f := range []Format{FormatCSV, FormatNDJSON, FormatSQL} {
		if !f.IsValid() {
			t.Errorf("%q should be valid", f)
		}
		if f.ContentType() == "" {
			t.Errorf("%q has empty content type", f)
		}
		if f.FileExt() == "" {
			t.Errorf("%q has empty file ext", f)
		}
	}
	if Format("xml").IsValid() {
		t.Error("xml should not be a valid Format")
	}
}

// SEC-1 (PR #16): CSV formula injection — csvCell on a string that
// begins with a formula-trigger character (=, +, -, @, \t, \r) MUST be
// prefixed with a single apostrophe per OWASP guidance.
func TestCSVCell_FormulaInjectionGuard(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"=2+3", "'=2+3"},
		{"+SUM(A1)", "'+SUM(A1)"},
		{"-cmd|'/c calc'", "'-cmd|'/c calc'"},
		{"@SUM(1)", "'@SUM(1)"},
		{"\tinjected", "'\tinjected"},
		{"\rinjected", "'\rinjected"},
		{"regular text", "regular text"},
		{"", ""},
		{nil, ""},
		{int64(42), "42"},
		{true, "true"},
	}
	for _, c := range cases {
		got := csvCell(c.in)
		if got != c.want {
			t.Errorf("csvCell(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// C6 (PR #16.5): CSV float convention — NaN / +Inf / -Inf must render
// as "" / "Infinity" / "-Infinity" rather than the literal "NaN" /
// "+Inf" / "-Inf" that strconv.FormatFloat emits.
func TestCSVCell_NonFiniteFloats(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{math.NaN(), ""},
		{math.Inf(1), "Infinity"},
		{math.Inf(-1), "-Infinity"},
		{1.5, "1.5"},
		{float32(math.NaN()), ""},
		{float32(math.Inf(1)), "Infinity"},
		{float32(math.Inf(-1)), "-Infinity"},
	}
	for _, c := range cases {
		got := csvCellRaw(c.in)
		if got != c.want {
			t.Errorf("csvCellRaw(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// C8 (PR #16.5): Postgres SQL preamble must include
// standard_conforming_strings = on for legacy-target portability.
func TestSQLEncoder_PostgresStandardConformingStrings(t *testing.T) {
	var buf bytes.Buffer
	enc, _ := NewEncoder(&buf, FormatSQL, Options{
		Engine:     dbadmin.EnginePostgres,
		SchemaName: "public",
		TableName:  "t",
		NowFunc:    func() string { return "2030-01-01T00:00:00Z" },
	})
	_ = enc.WriteHeader([]string{"id"})
	_ = enc.Close()
	if !strings.Contains(buf.String(), "SET standard_conforming_strings = on;") {
		t.Errorf("missing standard_conforming_strings pragma in: %q", buf.String())
	}
}

// C10 (PR #16.5): SQL trailer order — "-- truncated" precedes
// "-- end" so consumers can rely on "-- end" as the terminal token.
func TestSQLEncoder_TruncationMarkerOrder(t *testing.T) {
	var buf bytes.Buffer
	enc, _ := NewEncoder(&buf, FormatSQL, Options{
		Engine:     dbadmin.EngineMariaDB,
		SchemaName: "s",
		TableName:  "t",
		NowFunc:    func() string { return "2030-01-01T00:00:00Z" },
	})
	_ = enc.WriteHeader([]string{"id"})
	_ = enc.WriteRow([]any{int64(1)})
	// Mark truncated before Close (mirrors the handler flow).
	if mt, ok := enc.(interface{ MarkTruncated() }); ok {
		mt.MarkTruncated()
	} else {
		t.Fatal("sqlEncoder does not expose MarkTruncated")
	}
	_ = enc.Close()
	out := buf.String()
	ti := strings.Index(out, "-- truncated at")
	ei := strings.Index(out, "-- end:")
	if ti < 0 || ei < 0 {
		t.Fatalf("missing markers in: %q", out)
	}
	if ti > ei {
		t.Errorf("expected -- truncated BEFORE -- end; got truncated@%d, end@%d in:\n%s", ti, ei, out)
	}
}

// SEC-6 (PR #16.5): JSON columns surfaced as []byte should pass through
// as nested JSON when the byte slice is valid JSON. Non-JSON []byte
// still falls through to the base64 path.
func TestNDJSONEncoder_JSONColumnPassthrough(t *testing.T) {
	var buf bytes.Buffer
	enc, _ := NewEncoder(&buf, FormatNDJSON, Options{})
	_ = enc.WriteHeader([]string{"j", "b"})
	_ = enc.WriteRow([]any{
		[]byte(`{"hello":"world","n":3}`), // valid JSON → passthrough.
		[]byte{0xCA, 0xFE, 0xBA, 0xBE},    // not JSON → base64.
	})
	_ = enc.Close()
	line := strings.TrimRight(buf.String(), "\n")
	if !strings.Contains(line, `"j":{"hello":"world","n":3}`) {
		t.Errorf("expected JSON passthrough for column j; got: %q", line)
	}
	// Base64 of {0xCA, 0xFE, 0xBA, 0xBE} = "yv66vg==".
	if !strings.Contains(line, `"b":"yv66vg=="`) {
		t.Errorf("expected base64 fallback for column b; got: %q", line)
	}
}

func TestNDJSONEncoder_RawMessagePassthrough(t *testing.T) {
	var buf bytes.Buffer
	enc, _ := NewEncoder(&buf, FormatNDJSON, Options{})
	_ = enc.WriteHeader([]string{"j"})
	_ = enc.WriteRow([]any{json.RawMessage(`{"k":1}`)})
	_ = enc.Close()
	line := strings.TrimRight(buf.String(), "\n")
	if !strings.Contains(line, `"j":{"k":1}`) {
		t.Errorf("expected json.RawMessage passthrough; got: %q", line)
	}
}

func TestLooksLikeJSON(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{`{"a":1}`, true},
		{`  [1,2]`, true},
		{`"str"`, true},
		{`true`, true},
		{`null`, true},
		{`-3.14`, true},
		{``, false},
		{`hello`, false},
		{"\xCA\xFE", false},
	}
	for _, c := range cases {
		if got := looksLikeJSON([]byte(c.in)); got != c.want {
			t.Errorf("looksLikeJSON(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestSanitizeFilename(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"users.csv", "users.csv"},
		{"my table 2030.csv", "my_table_2030.csv"},
		{"../../etc/passwd", "etcpasswd"},
		{`weird "name".csv`, "weird_name.csv"},
		{"", "export"},
		{"   ", "export"},
		{strings.Repeat("a", 300), strings.Repeat("a", 200)},
	} {
		if got := SanitizeFilename(tc.in); got != tc.want {
			t.Errorf("SanitizeFilename(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

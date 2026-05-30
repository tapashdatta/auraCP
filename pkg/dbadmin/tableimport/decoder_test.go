package tableimport

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestFormatFromString(t *testing.T) {
	cases := []struct {
		in   string
		want Format
		ok   bool
	}{
		{"csv", FormatCSV, true},
		{"CSV", FormatCSV, true},
		{" ndjson ", FormatNDJSON, true},
		{"sql", "", false},
		{"", "", false},
		{"json", "", false},
	}
	for _, c := range cases {
		got, ok := FormatFromString(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("FormatFromString(%q) = (%q, %v); want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestOnConflictFromString(t *testing.T) {
	cases := []struct {
		in   string
		want OnConflict
		ok   bool
	}{
		{"", OnConflictError, true},
		{"error", OnConflictError, true},
		{"skip", OnConflictSkip, true},
		{"update", OnConflictUpdate, true},
		{"UPDATE", OnConflictUpdate, true},
		{"ignore", "", false},
	}
	for _, c := range cases {
		got, ok := OnConflictFromString(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("OnConflictFromString(%q) = (%q, %v); want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestCSVDecoder_HeaderAndRows(t *testing.T) {
	body := "id,name,age\r\n1,alice,30\r\n2,bob,\r\n3,\"o'reilly\",42\r\n"
	d, err := NewDecoder(strings.NewReader(body), FormatCSV, Options{})
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	defer d.Close()
	cols, err := d.ReadHeader()
	if err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	if !reflect.DeepEqual(cols, []string{"id", "name", "age"}) {
		t.Fatalf("header = %v", cols)
	}

	type row = map[string]any
	wantRows := []row{
		{"id": "1", "name": "alice", "age": "30"},
		{"id": "2", "name": "bob", "age": nil},
		{"id": "3", "name": "o'reilly", "age": "42"},
	}
	for i, want := range wantRows {
		got, err := d.ReadRow()
		if err != nil {
			t.Fatalf("ReadRow %d: %v", i, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("row %d = %v; want %v", i, got, want)
		}
	}
	if _, err := d.ReadRow(); !errors.Is(err, io.EOF) {
		t.Errorf("expected EOF, got %v", err)
	}
}

func TestCSVDecoder_StripsFormulaApostrophe(t *testing.T) {
	// The export-side prefixes cells starting with =, +, -, @ with a
	// single apostrophe. The decoder reverses this so round-trip
	// identity holds.
	body := "v\r\n'=SUM(A1)\r\n'-1\r\n'twas\r\n"
	d, _ := NewDecoder(strings.NewReader(body), FormatCSV, Options{})
	defer d.Close()
	_, _ = d.ReadHeader()
	r1, _ := d.ReadRow()
	if r1["v"] != "=SUM(A1)" {
		t.Errorf("row1 = %v; want =SUM(A1)", r1["v"])
	}
	r2, _ := d.ReadRow()
	if r2["v"] != "-1" {
		t.Errorf("row2 = %v; want -1", r2["v"])
	}
	r3, _ := d.ReadRow()
	if r3["v"] != "'twas" {
		t.Errorf("row3 = %v; want 'twas (apostrophe NOT stripped)", r3["v"])
	}
}

func TestCSVDecoder_ArityMismatch(t *testing.T) {
	body := "a,b\r\n1,2\r\n3,4,5\r\n"
	d, _ := NewDecoder(strings.NewReader(body), FormatCSV, Options{})
	defer d.Close()
	_, _ = d.ReadHeader()
	if _, err := d.ReadRow(); err != nil {
		t.Fatalf("first row should succeed: %v", err)
	}
	// csv.Reader surfaces ErrFieldCount when FieldsPerRecord != -1; we
	// configured -1 so the csv layer accepts the row, then ReadRow
	// rejects on arity mismatch.
	if _, err := d.ReadRow(); err == nil {
		t.Errorf("expected arity-mismatch error")
	}
}

func TestCSVDecoder_EmptyInput(t *testing.T) {
	d, _ := NewDecoder(strings.NewReader(""), FormatCSV, Options{})
	defer d.Close()
	if _, err := d.ReadHeader(); !errors.Is(err, io.EOF) {
		t.Errorf("empty CSV header: got %v, want EOF", err)
	}
}

func TestCSVDecoder_BOMStripped(t *testing.T) {
	// UTF-8 BOM bytes 0xEF 0xBB 0xBF prepended to the first cell.
	body := "\xef\xbb\xbfid,name\r\n1,alice\r\n"
	d, _ := NewDecoder(strings.NewReader(body), FormatCSV, Options{})
	defer d.Close()
	cols, _ := d.ReadHeader()
	if cols[0] != "id" {
		t.Errorf("BOM not stripped: cols[0] = %q", cols[0])
	}
}

func TestNDJSONDecoder_HeaderAndRows(t *testing.T) {
	body := `{"id":1,"name":"alice","age":30}
{"id":2,"name":"bob","age":null}

{"id":3,"name":"o'reilly","age":42}
`
	d, _ := NewDecoder(strings.NewReader(body), FormatNDJSON, Options{})
	defer d.Close()
	cols, err := d.ReadHeader()
	if err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	if !reflect.DeepEqual(cols, []string{"id", "name", "age"}) {
		t.Fatalf("header = %v", cols)
	}
	got, err := d.ReadRow()
	if err != nil {
		t.Fatalf("ReadRow 1: %v", err)
	}
	if v, ok := got["id"].(int64); !ok || v != 1 {
		t.Errorf("row1.id = %v (%T); want int64(1)", got["id"], got["id"])
	}
	if got["name"] != "alice" {
		t.Errorf("row1.name = %v", got["name"])
	}
	got2, _ := d.ReadRow()
	if got2["age"] != nil {
		t.Errorf("row2.age = %v; want nil", got2["age"])
	}
	got3, _ := d.ReadRow()
	if got3["name"] != "o'reilly" {
		t.Errorf("row3.name = %v", got3["name"])
	}
	if _, err := d.ReadRow(); !errors.Is(err, io.EOF) {
		t.Errorf("expected EOF, got %v", err)
	}
}

func TestNDJSONDecoder_FloatPromotion(t *testing.T) {
	body := `{"x":1.5,"y":2}
{"x":3,"y":4.5}
`
	d, _ := NewDecoder(strings.NewReader(body), FormatNDJSON, Options{})
	defer d.Close()
	_, _ = d.ReadHeader()
	r1, _ := d.ReadRow()
	if _, ok := r1["x"].(float64); !ok {
		t.Errorf("row1.x type %T; want float64", r1["x"])
	}
	if v, ok := r1["y"].(int64); !ok || v != 2 {
		t.Errorf("row1.y = %v (%T); want int64(2)", r1["y"], r1["y"])
	}
	r2, _ := d.ReadRow()
	if v, ok := r2["x"].(int64); !ok || v != 3 {
		t.Errorf("row2.x = %v (%T); want int64(3)", r2["x"], r2["x"])
	}
	if _, ok := r2["y"].(float64); !ok {
		t.Errorf("row2.y type %T; want float64", r2["y"])
	}
}

func TestNDJSONDecoder_EmptyInput(t *testing.T) {
	d, _ := NewDecoder(strings.NewReader(""), FormatNDJSON, Options{})
	defer d.Close()
	if _, err := d.ReadHeader(); !errors.Is(err, io.EOF) {
		t.Errorf("empty NDJSON header: got %v, want EOF", err)
	}
}

func TestNDJSONDecoder_KeyOrderPreserved(t *testing.T) {
	body := `{"z":1,"a":2,"m":3}
`
	d, _ := NewDecoder(strings.NewReader(body), FormatNDJSON, Options{})
	defer d.Close()
	cols, _ := d.ReadHeader()
	if !reflect.DeepEqual(cols, []string{"z", "a", "m"}) {
		t.Errorf("header = %v; want [z a m]", cols)
	}
}

func TestNewDecoder_RejectsUnknownFormat(t *testing.T) {
	if _, err := NewDecoder(strings.NewReader(""), Format("sql"), Options{}); err == nil {
		t.Errorf("expected error for sql format")
	}
	if _, err := NewDecoder(strings.NewReader(""), Format(""), Options{}); err == nil {
		t.Errorf("expected error for empty format")
	}
}

func TestNewDecoder_NilReader(t *testing.T) {
	if _, err := NewDecoder(nil, FormatCSV, Options{}); err == nil {
		t.Errorf("expected error for nil reader")
	}
}

func TestCSVDecoder_MaxCellBytes(t *testing.T) {
	body := "v\r\n" + strings.Repeat("a", 10) + "\r\n"
	d, _ := NewDecoder(strings.NewReader(body), FormatCSV, Options{MaxCellBytes: 5})
	defer d.Close()
	_, _ = d.ReadHeader()
	if _, err := d.ReadRow(); err == nil {
		t.Errorf("expected cell-too-large error")
	}
}

func TestNDJSONDecoder_MaxCellBytes(t *testing.T) {
	body := `{"v":"` + strings.Repeat("a", 10) + `"}
`
	d, _ := NewDecoder(strings.NewReader(body), FormatNDJSON, Options{MaxCellBytes: 5})
	defer d.Close()
	if _, err := d.ReadHeader(); err == nil {
		t.Errorf("expected cell-too-large error on header decode")
	}
}

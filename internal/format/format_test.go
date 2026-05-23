package format

import (
	"bytes"
	"strings"
	"testing"
)

func TestJSONWritesArray(t *testing.T) {
	var out bytes.Buffer

	err := JSON(&out, []map[string]any{
		{"name": "demo"},
	})
	if err != nil {
		t.Fatalf("JSON() error = %v", err)
	}

	want := "[\n  {\n    \"name\": \"demo\"\n  }\n]\n"
	if got := out.String(); got != want {
		t.Fatalf("JSON() = %q, want %q", got, want)
	}
}

func TestYAMLMultiWritesDocuments(t *testing.T) {
	var out bytes.Buffer

	err := YAMLMulti(&out, []any{
		map[string]any{"name": "one"},
		map[string]any{"name": "two"},
	})
	if err != nil {
		t.Fatalf("YAMLMulti() error = %v", err)
	}

	want := "---\nname: one\n---\nname: two\n"
	if got := out.String(); got != want {
		t.Fatalf("YAMLMulti() = %q, want %q", got, want)
	}
}

func TestNameWritesSortedNames(t *testing.T) {
	var out bytes.Buffer

	if err := Name(&out, []string{"beta", "alpha"}); err != nil {
		t.Fatalf("Name() error = %v", err)
	}

	if got, want := out.String(), "alpha\nbeta\n"; got != want {
		t.Fatalf("Name() = %q, want %q", got, want)
	}
}

func TestTableWritesHeadersAndSortedRows(t *testing.T) {
	var out bytes.Buffer

	err := Table(&out, []Column{
		{Header: "NAME", Key: "name"},
		{Header: "PROJECT", Key: "project"},
	}, []map[string]string{
		{"name": "beta", "project": "default"},
		{"name": "alpha", "project": "platform"},
	})
	if err != nil {
		t.Fatalf("Table() error = %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("Table() lines = %#v, want 3 lines", lines)
	}
	if !strings.Contains(lines[0], "NAME") || !strings.Contains(lines[0], "PROJECT") {
		t.Fatalf("header = %q, want NAME and PROJECT", lines[0])
	}
	if !strings.Contains(lines[1], "alpha") || !strings.Contains(lines[2], "beta") {
		t.Fatalf("rows = %#v, want sorted alpha before beta", lines[1:])
	}
}

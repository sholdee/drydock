package format

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
	sigsyaml "sigs.k8s.io/yaml"
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

// TestYAMLEncodesListNestedBlockScalarsReadably pins the indentation of YAML
// output. Strings that start with a newline (Helm's `expr: |` followed by
// nindent) or with spaces get an explicit block scalar indentation indicator;
// at yaml.v3's default 4-space indent, inside a sequence item that indicator
// disagreed with the column the content was written at, and yaml.v3 and
// sigs.k8s.io/yaml (kubectl, Argo CD) rejected the output or read it back
// without the leading spaces.
func TestYAMLEncodesListNestedBlockScalarsReadably(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value any
	}{
		{
			name:  "leading newline under nested sequences",
			value: map[string]any{"spec": map[string]any{"groups": []any{map[string]any{"rules": []any{map[string]any{"expr": "\nfoo != 1\n"}}}}}},
		},
		{
			name:  "leading spaces in a sequence item",
			value: map[string]any{"env": []any{map[string]any{"name": "BANNER", "value": "  boxed\n  text"}}},
		},
		{
			name:  "leading newline as a sequence item",
			value: map[string]any{"args": []any{"\necho hello\n"}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := YAML(&out, tt.value); err != nil {
				t.Fatalf("YAML() error = %v", err)
			}

			var v3 any
			if err := yaml.Unmarshal(out.Bytes(), &v3); err != nil {
				t.Fatalf("yaml.v3 cannot read YAML() output: %v\n%s", err, out.String())
			}
			if !reflect.DeepEqual(v3, tt.value) {
				t.Fatalf("yaml.v3 read back %#v, want %#v\n%s", v3, tt.value, out.String())
			}
			var sigs any
			if err := sigsyaml.Unmarshal(out.Bytes(), &sigs); err != nil {
				t.Fatalf("sigs.k8s.io/yaml cannot read YAML() output: %v\n%s", err, out.String())
			}
			if !reflect.DeepEqual(sigs, tt.value) {
				t.Fatalf("sigs.k8s.io/yaml read back %#v, want %#v\n%s", sigs, tt.value, out.String())
			}
		})
	}
}

func TestYAMLIndentsByTwoSpaces(t *testing.T) {
	value := map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": "demo"}}, "items": []any{map[string]any{"name": "one"}}}
	want := "items:\n  - name: one\nmetadata:\n  labels:\n    app: demo\n"

	var out bytes.Buffer
	if err := YAML(&out, value); err != nil {
		t.Fatalf("YAML() error = %v", err)
	}
	if got := out.String(); got != want {
		t.Fatalf("YAML() = %q, want %q", got, want)
	}
	data, err := MarshalYAML(value)
	if err != nil {
		t.Fatalf("MarshalYAML() error = %v", err)
	}
	if got := string(data); got != want {
		t.Fatalf("MarshalYAML() = %q, want %q", got, want)
	}
}

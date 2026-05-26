package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseDiffOutputRejectsName(t *testing.T) {
	for _, command := range []string{"diff apps", "diff app"} {
		t.Run(command, func(t *testing.T) {
			_, err := parseDiffOutput("name", command)
			if err == nil {
				t.Fatal("parseDiffOutput() error = nil, want name rejection")
			}
			if !strings.Contains(err.Error(), "name output is not supported for "+command) {
				t.Fatalf("parseDiffOutput() error = %v, want %s name rejection", err, command)
			}
		})
	}
}

func TestParseTestOutputSupportsStructuredFormats(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{value: "", want: "text"},
		{value: "json", want: "json"},
		{value: "yaml", want: "yaml"},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got, err := parseTestOutput(tt.value)
			if err != nil {
				t.Fatalf("parseTestOutput(%q) error = %v", tt.value, err)
			}
			if got != tt.want {
				t.Fatalf("parseTestOutput(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestParseDiagOutputDefaultsDiffToText(t *testing.T) {
	for _, value := range []string{"", "diff", "text"} {
		t.Run(value, func(t *testing.T) {
			got, err := parseDiagOutput(value)
			if err != nil {
				t.Fatalf("parseDiagOutput(%q) error = %v", value, err)
			}
			if got != "text" {
				t.Fatalf("parseDiagOutput(%q) = %q, want text", value, got)
			}
		})
	}
}

func TestWriteStructuredOutputWritesJSONAndYAML(t *testing.T) {
	value := map[string]string{"kind": "ConfigMap"}

	var jsonOut bytes.Buffer
	if err := writeStructuredOutput(&jsonOut, "json", value); err != nil {
		t.Fatalf("writeStructuredOutput(json) error = %v", err)
	}
	if !strings.Contains(jsonOut.String(), `"kind": "ConfigMap"`) {
		t.Fatalf("json output = %q, want kind field", jsonOut.String())
	}

	var yamlOut bytes.Buffer
	if err := writeStructuredOutput(&yamlOut, "yaml", value); err != nil {
		t.Fatalf("writeStructuredOutput(yaml) error = %v", err)
	}
	if !strings.Contains(yamlOut.String(), "kind: ConfigMap") {
		t.Fatalf("yaml output = %q, want kind field", yamlOut.String())
	}

	if err := writeStructuredOutput(&bytes.Buffer{}, "text", value); err == nil {
		t.Fatal("writeStructuredOutput(text) error = nil, want unsupported output")
	}
}

package appset

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
)

// requireGeneratorFailed checks that generation scoped a generator failure
// to the ApplicationSet, as Argo CD does with its ErrorOccurred condition:
// no error, no Applications, and an appset.generator-failed warning whose
// message contains want.
func requireGeneratorFailed(t *testing.T, apps []GeneratedApplication, diags []diagnostic.Diagnostic, err error, want string) {
	t.Helper()
	if err != nil {
		t.Fatalf("generation error = %v, want the failure scoped to the ApplicationSet", err)
	}
	if len(apps) != 0 {
		t.Fatalf("generated names = %#v, want none", generatedNames(apps))
	}
	for _, diag := range diags {
		if diagnostic.StableCode(diag) == diagnostic.CodeAppSetGeneratorFailed && strings.Contains(diag.Message, want) {
			return
		}
	}
	t.Fatalf("diagnostics = %#v, want an %s warning that contains %q", diags, diagnostic.CodeAppSetGeneratorFailed, want)
}

func generatedNames(apps []GeneratedApplication) []string {
	names := make([]string, 0, len(apps))
	for _, app := range apps {
		names = append(names, app.Application.Name)
	}
	return names
}
func writeAppsetTestFile(t *testing.T, filePath, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

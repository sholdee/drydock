package appset

import (
	"os"
	"path/filepath"
	"testing"
)

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

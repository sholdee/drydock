package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildAppsRendersManifests(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"build", "apps", "--path", filepath.Join("..", "..", "testdata", "applications", "e2e")})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	got := out.String()
	for _, want := range []string{"---\n", "kind: ConfigMap", "name: demo", "version: v1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("build apps output missing %q:\n%s", want, got)
		}
	}
}

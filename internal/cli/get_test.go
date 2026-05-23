package cli

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestGetAppsPrintsApplicationNames(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"get", "apps", "--path", filepath.Join("..", "..", "testdata", "applications", "e2e")})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got, want := out.String(), "demo\n"; got != want {
		t.Fatalf("get apps output = %q, want %q", got, want)
	}
}

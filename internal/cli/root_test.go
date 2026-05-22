package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootHelp(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{
		Version:      "test",
		Commit:       "none",
		ArgoCDModule: "github.com/argoproj/argo-cd/v3",
	})
	cmd.SetArgs([]string{"--help"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	got := out.String()
	for _, want := range []string{"argocd-local", "diff", "build", "get", "diag", "version"} {
		if !strings.Contains(got, want) {
			t.Fatalf("help output missing %q:\n%s", want, got)
		}
	}
}

func TestVersionCommand(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{
		Version:      "test",
		Commit:       "abc123",
		ArgoCDModule: "github.com/argoproj/argo-cd/v3",
	})
	cmd.SetArgs([]string{"version"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	got := out.String()
	for _, want := range []string{"version: test", "commit: abc123", "argocdModule: github.com/argoproj/argo-cd/v3"} {
		if !strings.Contains(got, want) {
			t.Fatalf("version output missing %q:\n%s", want, got)
		}
	}
}

func TestVersionCommandRejectsOperands(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{
		Version:      "test",
		Commit:       "abc123",
		ArgoCDModule: "github.com/argoproj/argo-cd/v3",
	})
	cmd.SetArgs([]string{"version", "unexpected"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err == nil {
		t.Fatalf("Execute() error = nil, want error")
	}
}

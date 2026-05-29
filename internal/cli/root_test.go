package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootHelp(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{
		Version: "test",
		Commit:  "none",
	})
	cmd.SetArgs([]string{"--help"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	got := out.String()
	for _, want := range []string{"drydock", "diff", "build", "get", "cache", "diag", "version"} {
		if !strings.Contains(got, want) {
			t.Fatalf("help output missing %q:\n%s", want, got)
		}
	}
}

func TestVersionCommand(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{
		Version:            "test",
		Commit:             "abc123",
		ArgoCDModule:       "github.com/argoproj/argo-cd/v3@test-version",
		GitOpsEngineModule: "github.com/argoproj/argo-cd/gitops-engine@test-version",
		HelmModule:         "helm.sh/helm/v4@test-version",
		KustomizeModule:    "sigs.k8s.io/kustomize/api@test-version",
		JsonnetModule:      "github.com/google/go-jsonnet@test-version",
		KubernetesModule:   "k8s.io/apimachinery@test-version",
	})
	cmd.SetArgs([]string{"version"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"version: test",
		"commit: abc123",
		"argocdModule: github.com/argoproj/argo-cd/v3@test-version",
		"gitopsEngineModule: github.com/argoproj/argo-cd/gitops-engine@test-version",
		"helmModule: helm.sh/helm/v4@test-version",
		"kustomizeModule: sigs.k8s.io/kustomize/api@test-version",
		"jsonnetModule: github.com/google/go-jsonnet@test-version",
		"kubernetesModule: k8s.io/apimachinery@test-version",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("version output missing %q:\n%s", want, got)
		}
	}
}

func TestVersionCommandOmitsEmptyModuleFields(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{
		Version: "test",
		Commit:  "abc123",
	})
	cmd.SetArgs([]string{"version"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	got := out.String()
	for _, unwanted := range []string{"argocdModule:", "gitopsEngineModule:", "helmModule:", "kustomizeModule:", "jsonnetModule:", "kubernetesModule:"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("version output included empty module field %q:\n%s", unwanted, got)
		}
	}
}

func TestVersionCommandRejectsOperands(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{
		Version: "test",
		Commit:  "abc123",
	})
	cmd.SetArgs([]string{"version", "unexpected"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err == nil {
		t.Fatalf("Execute() error = nil, want error")
	}
}

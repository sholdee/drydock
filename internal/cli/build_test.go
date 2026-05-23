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

func TestBuildAppsPrintsUnsupportedApplicationSetDiagnosticToStderr(t *testing.T) {
	root := t.TempDir()
	writeCLITestFile(t, filepath.Join(root, "direct.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: direct
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests/direct
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
	writeCLITestFile(t, filepath.Join(root, "manifests", "direct", "configmap.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: direct
  namespace: default
data:
  key: value
`)
	writeCLITestFile(t, filepath.Join(root, "unsupported-appset.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: unsupported
  namespace: argocd
spec:
  generators:
    - list:
        elements:
          - name: generated
  template:
    metadata:
      name: '{{name}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: manifests/generated
        targetRevision: main
      destination:
        name: in-cluster
        namespace: default
`)

	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"build", "apps", "--path", root})
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	got := out.String()
	for _, want := range []string{"---\n", "kind: ConfigMap", "name: direct", "key: value"} {
		if !strings.Contains(got, want) {
			t.Fatalf("build apps stdout missing %q:\n%s", want, got)
		}
	}
	wantStderr := "warning appset: only one git directories generator is supported in the MVP (path: unsupported-appset.yaml, pointer: spec.generators)\n"
	if got := stderr.String(); got != wantStderr {
		t.Fatalf("build apps stderr = %q, want %q", got, wantStderr)
	}
}

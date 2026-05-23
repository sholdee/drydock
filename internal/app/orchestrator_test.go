package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/home-operations/argocd-local/internal/diagnostic"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestOrchestratorDiscoversGeneratesAndRenders(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "applications", "e2e")

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if len(result.Applications) != 1 {
		t.Fatalf("len(Applications) = %d, want 1", len(result.Applications))
	}
	if result.Applications[0].Name != "demo" {
		t.Fatalf("Application name = %q, want demo", result.Applications[0].Name)
	}

	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	manifest := result.Manifests[0]
	if manifest.Object.GetKind() != "ConfigMap" || manifest.Object.GetName() != "demo" {
		t.Fatalf("rendered object = %s/%s, want ConfigMap/demo", manifest.Object.GetKind(), manifest.Object.GetName())
	}
	version, found, err := unstructured.NestedString(manifest.Object.Object, "data", "version")
	if err != nil || !found || version != "v1" {
		t.Fatalf("data.version = %q, found %v, err %v; want v1", version, found, err)
	}
}

func TestOrchestratorBuildRendersPlainDirectorySource(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "argocd", "plain-app.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: plain
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: manifests/plain
  destination:
    name: in-cluster
    namespace: plain
`)
	writeTestFile(t, filepath.Join(root, "manifests", "plain", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: plain
data:
  source: directory
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if len(result.Applications) != 1 {
		t.Fatalf("len(Applications) = %d, want 1", len(result.Applications))
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	manifest := result.Manifests[0]
	if manifest.Object.GetKind() != "ConfigMap" || manifest.Object.GetName() != "plain" {
		t.Fatalf("rendered object = %s/%s, want ConfigMap/plain", manifest.Object.GetKind(), manifest.Object.GetName())
	}
	value, found, err := unstructured.NestedString(manifest.Object.Object, "data", "source")
	if err != nil || !found || value != "directory" {
		t.Fatalf("data.source = %q, found %v, err %v; want directory", value, found, err)
	}
}

func TestOrchestratorBuildRendersLocalHelmChartSource(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "argocd", "helm-app.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: helm-local
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: charts/demo
    helm:
      values: |
        value: from-values
  destination:
    name: in-cluster
    namespace: helm-ns
`)
	writeTestFile(t, filepath.Join(root, "charts", "demo", "Chart.yaml"), `apiVersion: v2
name: demo
version: 0.1.0
`)
	writeTestFile(t, filepath.Join(root, "charts", "demo", "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}
data:
  value: {{ .Values.value | quote }}
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if len(result.Applications) != 1 {
		t.Fatalf("len(Applications) = %d, want 1", len(result.Applications))
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	manifest := result.Manifests[0]
	if manifest.Object.GetKind() != "ConfigMap" || manifest.Object.GetName() != "helm-local" {
		t.Fatalf("rendered object = %s/%s, want ConfigMap/helm-local", manifest.Object.GetKind(), manifest.Object.GetName())
	}
	value, found, err := unstructured.NestedString(manifest.Object.Object, "data", "value")
	if err != nil || !found || value != "from-values" {
		t.Fatalf("data.value = %q, found %v, err %v; want from-values", value, found, err)
	}
	if namespace := manifest.Object.GetNamespace(); namespace != "helm-ns" {
		t.Fatalf("namespace = %q, want helm-ns", namespace)
	}
}

func TestOrchestratorBuildRejectsChartOnlySource(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "argocd", "chart-app.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: chart-only
  namespace: argocd
spec:
  source:
    repoURL: https://charts.example.test
    targetRevision: 1.2.3
    chart: demo
  destination:
    name: in-cluster
    namespace: chart-only
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err == nil {
		t.Fatalf("Build() error = nil, want chart-only error")
	}
	if len(result.Manifests) != 0 {
		t.Fatalf("len(Manifests) = %d, want 0", len(result.Manifests))
	}
	for _, want := range []string{`chart="demo"`, "local chart path", "remote chart"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Build() error = %q, want %q", err.Error(), want)
		}
	}
}

func TestOrchestratorBuildPreservesListAndRenderDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeUnsupportedApplicationSetFixture(t, root)
	writeDuplicateConfigMaps(t, filepath.Join(root, "manifests", "direct"))

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	if len(result.Diagnostics) != 2 {
		t.Fatalf("len(Diagnostics) = %d, want 2: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	categories := map[string]bool{}
	for _, diag := range result.Diagnostics {
		if diag.Severity != diagnostic.SeverityWarning {
			t.Fatalf("diagnostic severity = %s, want warning: %#v", diag.Severity, diag)
		}
		categories[diag.Category] = true
	}
	for _, want := range []string{"appset", "repeated-resource"} {
		if !categories[want] {
			t.Fatalf("diagnostic categories = %#v, want %q", categories, want)
		}
	}
}

func TestOrchestratorBuildStrictFailsOnRenderDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "direct.yaml"), directApplicationYAML())
	writeDuplicateConfigMaps(t, filepath.Join(root, "manifests", "direct"))

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, Strict: true})
	if err == nil {
		t.Fatalf("Build() error = nil, want strict diagnostic error")
	}
	if len(result.Diagnostics) != 1 {
		t.Fatalf("len(Diagnostics) = %d, want 1: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	if result.Diagnostics[0].Severity != diagnostic.SeverityError {
		t.Fatalf("diagnostic severity = %s, want error", result.Diagnostics[0].Severity)
	}
	if result.Diagnostics[0].Category != "repeated-resource" {
		t.Fatalf("diagnostic category = %q, want repeated-resource", result.Diagnostics[0].Category)
	}
	if !strings.Contains(err.Error(), "repeated-resource") {
		t.Fatalf("Build() error = %q, want repeated-resource", err.Error())
	}
}

func TestOrchestratorListApplicationsSkipsUnsupportedApplicationSetInNonStrictMode(t *testing.T) {
	root := t.TempDir()
	writeUnsupportedApplicationSetFixture(t, root)

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}

	if len(result.Applications) != 1 {
		t.Fatalf("len(Applications) = %d, want 1", len(result.Applications))
	}
	if result.Applications[0].Name != "direct" {
		t.Fatalf("Application name = %q, want direct", result.Applications[0].Name)
	}
	if len(result.Diagnostics) != 1 {
		t.Fatalf("len(Diagnostics) = %d, want 1: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	diag := result.Diagnostics[0]
	if diag.Severity != diagnostic.SeverityWarning {
		t.Fatalf("diagnostic severity = %s, want warning", diag.Severity)
	}
	if diag.Category != "appset" {
		t.Fatalf("diagnostic category = %q, want appset", diag.Category)
	}
}

func TestOrchestratorListApplicationsFailsUnsupportedApplicationSetInStrictMode(t *testing.T) {
	root := t.TempDir()
	writeUnsupportedApplicationSetFixture(t, root)

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root, Strict: true})
	if err == nil {
		t.Fatalf("ListApplications() error = nil, want unsupported ApplicationSet error")
	}
	if len(result.Diagnostics) != 1 {
		t.Fatalf("len(Diagnostics) = %d, want 1: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	if result.Diagnostics[0].Severity != diagnostic.SeverityError {
		t.Fatalf("diagnostic severity = %s, want error", result.Diagnostics[0].Severity)
	}
	if !strings.Contains(err.Error(), "unsupported ApplicationSet generator") {
		t.Fatalf("ListApplications() error = %q, want unsupported ApplicationSet generator", err.Error())
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func writeUnsupportedApplicationSetFixture(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "direct.yaml"), directApplicationYAML())
	writeTestFile(t, filepath.Join(root, "apps", "unsupported-appset.yaml"), `apiVersion: argoproj.io/v1alpha1
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
}

func directApplicationYAML() string {
	return `apiVersion: argoproj.io/v1alpha1
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
`
}

func writeDuplicateConfigMaps(t *testing.T, dir string) {
	t.Helper()
	writeTestFile(t, filepath.Join(dir, "first.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: direct
data:
  value: first
`)
	writeTestFile(t, filepath.Join(dir, "second.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: direct
data:
  value: second
`)
}

package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func BenchmarkOrchestratorBuildManyLocalApplications(b *testing.B) {
	root := b.TempDir()
	for i := range 100 {
		name := fmt.Sprintf("demo-%03d", i)
		writeBenchmarkApplication(b, root, name)
		writeBenchmarkFile(b, filepath.Join(root, "manifests", name, "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
		writeBenchmarkFile(b, filepath.Join(root, "manifests", name, "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: `+name+`
data:
  value: benchmark
`)
	}
	orchestrator := Orchestrator{}
	request := BuildRequest{Path: root}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		result, err := orchestrator.Build(context.Background(), request)
		if err != nil {
			b.Fatalf("Build() error = %v", err)
		}
		if len(result.ApplicationManifests) != 100 {
			b.Fatalf("ApplicationManifests = %d, want 100", len(result.ApplicationManifests))
		}
	}
}

func BenchmarkOrchestratorBuildManyLocalApplicationsParallel(b *testing.B) {
	root := b.TempDir()
	for i := range 100 {
		name := fmt.Sprintf("demo-%03d", i)
		writeBenchmarkApplication(b, root, name)
		writeBenchmarkFile(b, filepath.Join(root, "manifests", name, "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
		writeBenchmarkFile(b, filepath.Join(root, "manifests", name, "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: `+name+`
data:
  value: benchmark
`)
	}
	orchestrator := Orchestrator{}
	request := BuildRequest{Path: root}
	request.Parallelism = 8

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		result, err := orchestrator.Build(context.Background(), request)
		if err != nil {
			b.Fatalf("Build() error = %v", err)
		}
		if len(result.ApplicationManifests) != 100 {
			b.Fatalf("ApplicationManifests = %d, want 100", len(result.ApplicationManifests))
		}
	}
}

func BenchmarkOrchestratorExpandApplicationSetList(b *testing.B) {
	root := b.TempDir()
	writeBenchmarkApplicationSetList(b, root, 250)
	orchestrator := Orchestrator{}
	request := BuildRequest{Path: root}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		result, err := orchestrator.ListApplications(context.Background(), request)
		if err != nil {
			b.Fatalf("ListApplications() error = %v", err)
		}
		if len(result.Applications) != 250 {
			b.Fatalf("Applications = %d, want 250", len(result.Applications))
		}
	}
}

func writeBenchmarkApplication(tb testing.TB, root string, name string) {
	tb.Helper()
	writeBenchmarkFile(tb, filepath.Join(root, "apps", name+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+name+`
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/`+name+`
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
}

func writeBenchmarkApplicationSetList(tb testing.TB, root string, count int) {
	tb.Helper()
	var elements strings.Builder
	for i := range count {
		name := fmt.Sprintf("appset-%03d", i)
		fmt.Fprintf(&elements, "          - name: %s\n            namespace: ns-%03d\n", name, i)
	}
	writeBenchmarkFile(tb, filepath.Join(root, "appsets", "list.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: many
  namespace: argocd
spec:
  goTemplate: true
  generators:
    - list:
        elements:
`+elements.String()+`  template:
    metadata:
      name: '{{.name}}'
      namespace: argocd
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: 'manifests/{{.name}}'
        targetRevision: main
      destination:
        name: in-cluster
        namespace: '{{.namespace}}'
`)
}

func writeBenchmarkFile(tb testing.TB, path string, content string) {
	tb.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		tb.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		tb.Fatalf("WriteFile() error = %v", err)
	}
}

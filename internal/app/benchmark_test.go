package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/sholdee/drydock/internal/render"
	"github.com/sholdee/drydock/internal/rendercache"
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
	request := BuildRequest{
		Path:        root,
		Parallelism: 8,
	}

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

func BenchmarkOrchestratorBuildPersistentCacheCold(b *testing.B) {
	root := b.TempDir()
	writePersistentCacheBenchmarkFleet(b, root, 50)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		cacheDir := b.TempDir()
		b.StartTimer()
		result, err := (Orchestrator{}).Build(context.Background(), persistentCacheBenchmarkRequest(root, cacheDir))
		if err != nil {
			b.Fatalf("Build() error = %v", err)
		}
		if len(result.ApplicationManifests) != 50 {
			b.Fatalf("ApplicationManifests = %d, want 50", len(result.ApplicationManifests))
		}
	}
}

func BenchmarkOrchestratorBuildPersistentCacheWarm(b *testing.B) {
	root := b.TempDir()
	writePersistentCacheBenchmarkFleet(b, root, 50)
	cacheDir := b.TempDir()
	if _, err := (Orchestrator{}).Build(context.Background(), persistentCacheBenchmarkRequest(root, cacheDir)); err != nil {
		b.Fatalf("cold prime Build() error = %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var renders atomic.Int64
		orchestrator := Orchestrator{}
		orchestrator.renderObserver = func(render.ResolvedSource) { renders.Add(1) }
		result, err := orchestrator.Build(context.Background(), persistentCacheBenchmarkRequest(root, cacheDir))
		if err != nil {
			b.Fatalf("Build() error = %v", err)
		}
		if len(result.ApplicationManifests) != 50 {
			b.Fatalf("ApplicationManifests = %d, want 50", len(result.ApplicationManifests))
		}
		if got := renders.Load(); got != 0 {
			b.Fatalf("warm render-engine invocations = %d, want 0", got)
		}
	}
}

func writePersistentCacheBenchmarkFleet(b *testing.B, root string, count int) {
	b.Helper()
	for i := range count {
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
	gitCommitAllBench(b, root)
}

func gitCommitAllBench(b *testing.B, root string) {
	b.Helper()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		b.Fatalf("PlainInit() error = %v", err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		b.Fatalf("Worktree() error = %v", err)
	}
	if err := worktree.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		b.Fatalf("AddWithOptions() error = %v", err)
	}
	signature := &object.Signature{Name: "Bench", Email: "bench@example.invalid", When: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	if _, err := worktree.Commit("benchmark fixture", &git.CommitOptions{Author: signature, Committer: signature}); err != nil {
		b.Fatalf("Commit() error = %v", err)
	}
}

func persistentCacheBenchmarkRequest(root, cacheDir string) BuildRequest {
	request := BuildRequest{
		Path:               root,
		RenderCacheEnabled: true,
		RenderCacheDir:     cacheDir,
		EngineFingerprint: rendercache.EngineFingerprint{
			Version:            "bench",
			Commit:             "0123456789abcdef0123456789abcdef01234567",
			ArgoCDModule:       "github.com/argoproj/argo-cd/v3@bench",
			GitOpsEngineModule: "github.com/argoproj/argo-cd/gitops-engine@bench",
			HelmModule:         "helm.sh/helm/v4@bench",
			KustomizeModule:    "sigs.k8s.io/kustomize/api@bench",
			JsonnetModule:      "github.com/google/go-jsonnet@bench",
			KubernetesModule:   "k8s.io/apimachinery@bench",
		},
	}
	return request
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

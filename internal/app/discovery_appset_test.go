package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/appset"
	"github.com/sholdee/drydock/internal/discovery"
	"go.yaml.in/yaml/v3"
)

func TestGenerateApplicationSetCachedReturnsIndependentApplications(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "apps", "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	appSetFile := discovery.ApplicationSetFile{
		Path:           "appset.yaml",
		DocumentIndex:  1,
		ApplicationSet: testApplicationSet(t),
	}
	memo := &sync.Map{}

	first, _, err := generateApplicationSetCached(memo, root, appSetFile, appset.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 {
		t.Fatalf("len(first) = %d, want 1", len(first))
	}
	first[0].Application.Name = "mutated"
	first[0].Application.Spec.Source.Path = "mutated"
	first[0].SourcePaths[0] = "mutated"

	second, _, err := generateApplicationSetCached(memo, root, appSetFile, appset.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := second[0].Application.Name, "api"; got != want {
		t.Fatalf("cached application name = %q, want %q", got, want)
	}
	if got, want := second[0].Application.Spec.GetSource().Path, "apps/api"; got != want {
		t.Fatalf("cached application path = %q, want %q", got, want)
	}
	if got, want := second[0].SourcePaths[0], "apps/api"; got != want {
		t.Fatalf("cached source path = %q, want %q", got, want)
	}
}

func TestApplicationSetGenerationMemoKeyIncludesProviderOptions(t *testing.T) {
	appSetFile := discovery.ApplicationSetFile{
		Path:           "appset.yaml",
		DocumentIndex:  1,
		ApplicationSet: testApplicationSet(t),
	}
	left, err := appsetGenerationMemoKey(appSetFile, appset.Options{
		Provider: appset.ProviderOptions{Data: appset.ProviderData{Clusters: []appset.ClusterInput{{Name: "dev"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	right, err := appsetGenerationMemoKey(appSetFile, appset.Options{
		Provider: appset.ProviderOptions{Data: appset.ProviderData{Clusters: []appset.ClusterInput{{Name: "prod"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if left == right {
		t.Fatalf("memo keys should differ when provider options differ: %q", left)
	}
}

func testApplicationSet(t *testing.T) argoappv1.ApplicationSet {
	t.Helper()
	data := []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: apps
  namespace: argocd
spec:
  goTemplate: true
  generators:
    - git:
        directories:
          - path: apps/*
  template:
    metadata:
      name: "{{.path.basename}}"
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        targetRevision: HEAD
        path: "{{.path.path}}"
      destination:
        server: https://kubernetes.default.svc
        namespace: default
`)
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	normalized, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var appSet argoappv1.ApplicationSet
	if err := json.Unmarshal(normalized, &appSet); err != nil {
		t.Fatal(err)
	}
	return appSet
}

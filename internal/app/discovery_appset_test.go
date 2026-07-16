package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/appset"
	"github.com/sholdee/drydock/internal/diagnostic"
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

	assertFailingApplicationSetRoundTripsThroughMemo(t, memo)
}

func assertFailingApplicationSetRoundTripsThroughMemo(t *testing.T, memo *sync.Map) {
	t.Helper()
	failingRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(failingRoot, "pipelines"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(failingRoot, "pipelines", "pipelines.yaml"), []byte("# none yet\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	failingAppSetFile := discovery.ApplicationSetFile{
		Path:           "failing-appset.yaml",
		DocumentIndex:  1,
		ApplicationSet: testFailingApplicationSet(t),
	}
	missApps, missDiags, err := generateApplicationSetCached(memo, failingRoot, failingAppSetFile, appset.Options{})
	if err != nil {
		t.Fatalf("miss error = %v, want appset-scoped warning", err)
	}
	hitApps, hitDiags, err := generateApplicationSetCached(memo, failingRoot, failingAppSetFile, appset.Options{})
	if err != nil {
		t.Fatalf("hit error = %v, want appset-scoped warning", err)
	}
	if missApps != nil || hitApps != nil {
		t.Fatalf("failing appset apps = miss %#v hit %#v, want nil on both", missApps, hitApps)
	}
	if len(missDiags) != 1 || len(hitDiags) != 1 {
		t.Fatalf("failing appset diags = miss %#v hit %#v, want one warning on both", missDiags, hitDiags)
	}
	if missDiags[0].Severity != diagnostic.SeverityWarning || !strings.Contains(missDiags[0].Message, "template render failed") {
		t.Fatalf("miss diagnostic = %#v, want template render failed warning", missDiags[0])
	}
	if !reflect.DeepEqual(missDiags, hitDiags) {
		t.Fatalf("cached diagnostics differ: miss %#v hit %#v", missDiags, hitDiags)
	}
}

func testFailingApplicationSet(t *testing.T) argoappv1.ApplicationSet {
	t.Helper()
	return applicationSetFromYAML(t, []byte(`
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: failing
  namespace: argocd
spec:
  goTemplate: true
  goTemplateOptions: ["missingkey=error"]
  generators:
    - git:
        files:
          - path: pipelines/*.yaml
  template:
    metadata:
      name: "kargo-{{.name}}"
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        targetRevision: HEAD
        path: "apps/{{.path.filename}}"
      destination:
        server: https://kubernetes.default.svc
        namespace: default
`))
}

func TestListApplicationsScopesTemplateRenderFailureToOneApplicationSet(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "configs", "healthy.yaml"), "name: alpha\n")
	writeTestFile(t, filepath.Join(root, "pipelines", "pipelines.yaml"), "# none yet\n")
	writeTestFile(t, filepath.Join(root, "healthy-appset.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: healthy
  namespace: argocd
spec:
  goTemplate: true
  goTemplateOptions: ["missingkey=error"]
  generators:
    - git:
        files:
          - path: configs/*.yaml
  template:
    metadata:
      name: "{{.name}}"
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        targetRevision: main
        path: "apps/{{.name}}"
      destination:
        name: in-cluster
        namespace: default
`)
	writeTestFile(t, filepath.Join(root, "failing-appset.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: failing
  namespace: argocd
spec:
  goTemplate: true
  goTemplateOptions: ["missingkey=error"]
  generators:
    - git:
        files:
          - path: pipelines/*.yaml
  template:
    metadata:
      name: "kargo-{{.name}}"
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        targetRevision: main
        path: "apps/{{.name}}"
      destination:
        name: in-cluster
        namespace: default
`)

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("ListApplications() error = %v, want appset-scoped warning", err)
	}
	names := make([]string, 0, len(result.Applications))
	for _, app := range result.Applications {
		names = append(names, app.Name)
	}
	if !slices.Equal(names, []string{"alpha"}) {
		t.Fatalf("applications = %#v, want only the healthy ApplicationSet output", names)
	}
	var renderFailures int
	for _, diag := range result.Diagnostics {
		if strings.Contains(diag.Message, "generated zero Applications") {
			t.Fatalf("diagnostics = %#v, want no empty-ApplicationSet diagnostic for the failing appset", result.Diagnostics)
		}
		if !strings.Contains(diag.Message, "template render failed") {
			continue
		}
		renderFailures++
		if diag.Severity != diagnostic.SeverityWarning {
			t.Fatalf("diagnostic severity = %s, want warning", diag.Severity)
		}
		if !strings.Contains(diag.Message, "pipelines/pipelines.yaml") {
			t.Fatalf("diagnostic message = %q, want failing source path", diag.Message)
		}
	}
	if renderFailures != 1 {
		t.Fatalf("template render failed diagnostics = %d, want 1: %#v", renderFailures, result.Diagnostics)
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
	return applicationSetFromYAML(t, []byte(`
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
`))
}

func applicationSetFromYAML(t *testing.T, data []byte) argoappv1.ApplicationSet {
	t.Helper()
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

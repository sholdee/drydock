package drydock

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
)

func TestRenderApplications(t *testing.T) {
	result, err := Render(context.Background(), Config{Path: filepath.Join("..", "..", "testdata", "applications", "e2e")})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(result.Applications) != 1 {
		t.Fatalf("Applications = %d, want 1", len(result.Applications))
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("Manifests = %d, want 1", len(result.Manifests))
	}
}
func TestPublicRenderParallelismPreservesManifestOrder(t *testing.T) {
	root := t.TempDir()
	writeAPIPluginAppTreeNamed(t, root, "app-a")
	writeAPIPluginAppTreeNamed(t, root, "app-b")
	writeAPIPluginAppTreeNamed(t, root, "app-c")
	renderer := newControlledPublicPluginRenderer([]string{"app-a", "app-b", "app-c"})

	resultCh := make(chan struct {
		result RenderResult
		err    error
	}, 1)
	go func() {
		result, err := Render(context.Background(), Config{
			Path:           root,
			Parallelism:    3,
			PluginRenderer: renderer,
		})
		resultCh <- struct {
			result RenderResult
			err    error
		}{result: result, err: err}
	}()

	renderer.waitStarted(t, "app-a", "app-b", "app-c")
	renderer.release("app-c")
	renderer.release("app-b")
	renderer.release("app-a")

	out := <-resultCh
	if out.err != nil {
		t.Fatalf("Render() error = %v", out.err)
	}
	names := make([]string, 0, len(out.result.Manifests))
	for _, manifest := range out.result.Manifests {
		metadata, ok := manifest.Object["metadata"].(map[string]any)
		if !ok {
			t.Fatalf("manifest metadata = %#v, want object", manifest.Object["metadata"])
		}
		name, ok := metadata["name"].(string)
		if !ok {
			t.Fatalf("manifest metadata.name = %#v, want string", metadata["name"])
		}
		names = append(names, name)
	}
	if !slices.Equal(names, []string{"app-a", "app-b", "app-c"}) {
		t.Fatalf("manifest names = %#v, want selected Application order", names)
	}
}
func TestPublicConfigParallelismWiresRequests(t *testing.T) {
	client := NewClient(Config{Parallelism: 5})

	if got := client.buildRequest().Parallelism; got != 5 {
		t.Fatalf("build request Parallelism = %d, want 5", got)
	}
	if got := client.diffRequest().Parallelism; got != 5 {
		t.Fatalf("diff request Parallelism = %d, want 5", got)
	}
}
func TestPublicConfigUnifiedDefaultsToThree(t *testing.T) {
	client := NewClient(Config{})

	if got := client.diffRequest().Unified; got != 3 {
		t.Fatalf("diff request Unified = %d, want default 3", got)
	}
}
func TestRenderAppliesResourceFilters(t *testing.T) {
	root := t.TempDir()
	writeAPIAppTree(t, root, "demo", configMapBody("demo", "v1"))

	result, err := Render(context.Background(), Config{
		Path:      root,
		SkipKinds: []string{"ConfigMap"},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(result.Applications) != 1 {
		t.Fatalf("Applications = %d, want 1", len(result.Applications))
	}
	if len(result.Manifests) != 0 {
		t.Fatalf("Manifests = %d, want 0 after ConfigMap filter", len(result.Manifests))
	}
	if !hasStatus(result.Statuses, "demo", "PASS") {
		t.Fatalf("Statuses = %#v, want PASS status for filtered render", result.Statuses)
	}
}
func TestRenderReportsAdvancedSettingsDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeAPIAppTree(t, root, "demo", configMapBody("demo", "v1"))
	writeAPIFile(t, filepath.Join(root, "settings", "argocd-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.customizations.ignoreResourceUpdates.apps_Deployment: |
    jsonPointers:
      - /status
  resource.customizations.health.apps_Deployment: |
    return { status = "Healthy" }
  resource.customizations.useOpenLibs.apps_Deployment: "true"
  resource.customizations.actions.apps_Deployment: |
    definitions:
      - name: restart
        action.lua: |
          return obj
`)

	result, err := Render(context.Background(), Config{Path: root})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !hasDiagnostic(result.Diagnostics, "settings", "ignoreResourceUpdates are parsed but not applied") {
		t.Fatalf("Diagnostics = %#v, want ignoreResourceUpdates warning", result.Diagnostics)
	}
	if !hasDiagnostic(result.Diagnostics, "settings", "health Lua is parsed as metadata only") {
		t.Fatalf("Diagnostics = %#v, want health warning", result.Diagnostics)
	}
	if !hasDiagnostic(result.Diagnostics, "settings", "useOpenLibs is parsed as metadata only") {
		t.Fatalf("Diagnostics = %#v, want useOpenLibs warning", result.Diagnostics)
	}
	if !hasDiagnostic(result.Diagnostics, "settings", "actions are parsed as metadata only") {
		t.Fatalf("Diagnostics = %#v, want actions warning", result.Diagnostics)
	}
}
func TestRenderReportsProjectValidationDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeAPIAppTreeWithProject(t, root, "demo", "platform", "https://github.com/example/denied")
	writeAPIFile(t, filepath.Join(root, "projects", "platform.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: platform
spec:
  sourceRepos:
    - https://github.com/example/allowed
  destinations:
    - server: https://kubernetes.default.svc
      namespace: default
`)

	result, err := Render(context.Background(), Config{Path: root})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !hasDiagnostic(result.Diagnostics, "project", "source repository") {
		t.Fatalf("Diagnostics = %#v, want project source warning", result.Diagnostics)
	}
}
func TestRenderReturnsDiagnosticCodes(t *testing.T) {
	root := t.TempDir()
	writeAPIFile(t, filepath.Join(root, "appset.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: unsupported
spec:
  generators:
    - scmProvider: {}
  template:
    metadata:
      name: generated
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: manifests/generated
      destination:
        name: in-cluster
        namespace: default
`)

	result, err := Render(context.Background(), Config{Path: root})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !hasDiagnosticCode(result.Diagnostics, "appset.unsupported-generator") {
		t.Fatalf("Diagnostics = %#v, want stable appset diagnostic code", result.Diagnostics)
	}
}
func TestPublicConfigRoutesProviderFixtureDataWithoutInternalTypes(t *testing.T) {
	client := NewClient(Config{
		Path:                           "/tmp/repo",
		ApplicationSetProviderFixtures: []string{"clusters.yaml"},
		ApplicationSetProviderData: ApplicationSetProviderData{
			Clusters: []ApplicationSetProviderCluster{{
				Name:        "prod",
				Server:      "https://prod.example.invalid",
				Project:     "platform",
				Labels:      map[string]string{"environment": "prod"},
				Annotations: map[string]string{"owner": "platform"},
				Values:      map[string]string{"region": "home"},
			}},
		},
	})

	buildRequest := client.buildRequest()
	if !slices.Equal(buildRequest.ApplicationSetProviderFixtures, []string{"clusters.yaml"}) {
		t.Fatalf("ApplicationSetProviderFixtures = %#v, want clusters.yaml", buildRequest.ApplicationSetProviderFixtures)
	}
	if got := buildRequest.ApplicationSetProviderData.Clusters; len(got) != 1 || got[0].Name != "prod" || got[0].Labels["environment"] != "prod" {
		t.Fatalf("ApplicationSetProviderData.Clusters = %#v, want public data converted to internal request data", got)
	}

	diffRequest := client.diffRequest()
	if !slices.Equal(diffRequest.ApplicationSetProviderFixtures, []string{"clusters.yaml"}) {
		t.Fatalf("diff ApplicationSetProviderFixtures = %#v, want clusters.yaml", diffRequest.ApplicationSetProviderFixtures)
	}
	if got := diffRequest.ApplicationSetProviderData.Clusters; len(got) != 1 || got[0].Name != "prod" {
		t.Fatalf("diff ApplicationSetProviderData.Clusters = %#v, want public data converted to internal request data", got)
	}
}
func TestListApplications(t *testing.T) {
	result, err := ListApplications(context.Background(), Config{Path: filepath.Join("..", "..", "testdata", "applications", "e2e")})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}
	if len(result.Applications) != 1 || result.Applications[0].Name != "demo" {
		t.Fatalf("Applications = %#v, want demo", result.Applications)
	}
}

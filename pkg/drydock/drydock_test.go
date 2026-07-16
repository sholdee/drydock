package drydock

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"slices"
	"strings"
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

func TestRenderDiscoverIgnoresExcludeUndecodableCandidates(t *testing.T) {
	root := t.TempDir()
	writeAPIAppTree(t, root, "demo", configMapBody("demo", "v1"))
	// Scaffolding template that passes discovery's content sniff but fails
	// typed decoding: requeueAfterSeconds expects int64.
	writeAPIFile(t, filepath.Join(root, "templates", "scaffold.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: scaffold
spec:
  generators:
    - pullRequest:
        requeueAfterSeconds: $PR_REQUEUE
`)

	_, err := Render(context.Background(), Config{Path: root})
	if err == nil {
		t.Fatal("Render() error = nil, want undecodable candidate error")
	}
	if want := "(use --discover-ignore to exclude non-deployable manifests from discovery)"; !strings.Contains(err.Error(), want) {
		t.Fatalf("Render() error = %v, want remediation hint %q", err, want)
	}

	result, err := Render(context.Background(), Config{Path: root, DiscoverIgnores: []string{"templates/**"}})
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

func TestRenderAVPCompatibilityReplacesPlaceholders(t *testing.T) {
	root := t.TempDir()
	writeAPIAppTree(t, root, "demo", `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  domain: argocd.<path:vaults/Kubernetes/items/cluster#domain>
`)

	result, err := Render(context.Background(), Config{Path: root, EnableAVPCompat: true})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("Manifests = %d, want 1", len(result.Manifests))
	}
	data, ok := result.Manifests[0].Object["data"].(map[string]any)
	if !ok {
		t.Fatalf("data = %#v, want map", result.Manifests[0].Object["data"])
	}
	value, ok := data["domain"].(string)
	if !ok || !strings.HasPrefix(value, "argocd.drydock-redacted-") {
		t.Fatalf("data.domain = %#v, want redacted AVP value", data["domain"])
	}
	if !hasDiagnosticCode(result.Diagnostics, "plugin.avp-compat-substituted") {
		t.Fatalf("Diagnostics = %#v, want AVP compatibility diagnostic", result.Diagnostics)
	}
}

func TestRenderKSOPSCompatibilityReplacesGeneratorWithPlaceholder(t *testing.T) {
	root := t.TempDir()

	// Application pointing to a Kustomize source with a KSOPS generator.
	writeAPIFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: manifests/demo
  destination:
    name: in-cluster
    namespace: default
`)
	// Kustomize entrypoint with a KSOPS generator reference.
	writeAPIFile(t, filepath.Join(root, "manifests", "demo", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
generators:
  - ./secret-generator.yaml
`)
	// KSOPS generator manifest (apiVersion: viaduct.ai/v1, kind: ksops).
	writeAPIFile(t, filepath.Join(root, "manifests", "demo", "secret-generator.yaml"), `apiVersion: viaduct.ai/v1
kind: ksops
metadata:
  name: demo-secret-generator
files:
  - ./demo-secret.sops.yaml
`)
	// SOPS-encrypted Secret fixture with plaintext structure and ENC[...] values.
	writeAPIFile(t, filepath.Join(root, "manifests", "demo", "demo-secret.sops.yaml"), `apiVersion: v1
kind: Secret
metadata:
  name: demo-secret
data:
  TOKEN: ENC[AES256_GCM,data:NYI8Q9o3original,iv:aksv1mXYiMja9Guq9SCT6wPjrXTo2MHX6JMGGyYmIo8=,tag:IBjPwh9GgBEIClIjaXyyVQ==,type:str]
sops:
  encrypted_regex: ^(data|stringData)$
  version: 3.10.2
`)

	result, err := Render(context.Background(), Config{Path: root, EnableKSOPSCompat: true})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !hasDiagnosticCode(result.Diagnostics, "kustomize.ksops-compat-substituted") {
		t.Fatalf("Diagnostics = %#v, want kustomize.ksops-compat-substituted diagnostic", result.Diagnostics)
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("Manifests = %d, want 1", len(result.Manifests))
	}
	data, ok := result.Manifests[0].Object["data"].(map[string]any)
	if !ok {
		t.Fatalf("Object[data] = %#v, want map", result.Manifests[0].Object["data"])
	}
	token, _ := data["TOKEN"].(string)
	if strings.HasPrefix(token, "ENC[") {
		t.Fatalf("data.TOKEN = %q, still contains the encrypted value", token)
	}
	decoded, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("data.TOKEN = %q, want valid base64: %v", token, err)
	}
	if !strings.HasPrefix(string(decoded), "drydock-ksops-redacted-") {
		t.Fatalf("data.TOKEN decodes to %q, want drydock-ksops-redacted- placeholder", decoded)
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
			DiscoveryMode:  "static",
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

func TestPublicConfigMaxDiscoveryDepthDistinguishesUnsetFromExplicitZero(t *testing.T) {
	client := NewClient(Config{})
	request := client.buildRequest()
	if request.MaxDiscoveryDepth != 4 || request.MaxDiscoveryDepthSet {
		t.Fatalf("default max discovery depth = %d set=%t, want 4 set=false", request.MaxDiscoveryDepth, request.MaxDiscoveryDepthSet)
	}

	zero := 0
	client = NewClient(Config{MaxDiscoveryDepth: &zero})
	request = client.buildRequest()
	if request.MaxDiscoveryDepth != 0 || !request.MaxDiscoveryDepthSet {
		t.Fatalf("explicit max discovery depth = %d set=%t, want 0 set=true", request.MaxDiscoveryDepth, request.MaxDiscoveryDepthSet)
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
	if hasDiagnostic(result.Diagnostics, "settings", "health Lua is parsed as metadata only") {
		t.Fatalf("Diagnostics = %#v, want no health metadata-only warning", result.Diagnostics)
	}
	if hasDiagnostic(result.Diagnostics, "settings", "useOpenLibs is parsed as metadata only") {
		t.Fatalf("Diagnostics = %#v, want no useOpenLibs metadata-only warning", result.Diagnostics)
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

func TestRenderProjectDiagnosticsDefaultHidesDeferredDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeAPIDeferredResourcePolicyTree(t, root)

	result, err := Render(context.Background(), Config{
		Path:   root,
		Strict: true,
	})
	if err != nil {
		t.Fatalf("Render() error = %v; diagnostics = %#v", err, result.Diagnostics)
	}
	if hasDiagnosticCode(result.Diagnostics, "project.resource-scope-deferred") {
		t.Fatalf("Diagnostics = %#v, want deferred project diagnostic hidden by default", result.Diagnostics)
	}
	if !hasStatus(result.Statuses, "demo", "PASS") {
		t.Fatalf("Statuses = %#v, want PASS", result.Statuses)
	}
}

func TestRenderProjectDiagnosticsAllRestoresDeferredStrictFailure(t *testing.T) {
	root := t.TempDir()
	writeAPIDeferredResourcePolicyTree(t, root)

	result, err := Render(context.Background(), Config{
		Path:                   root,
		Strict:                 true,
		ProjectDiagnosticsMode: ProjectDiagnosticsModeAll,
	})
	if err == nil {
		t.Fatal("Render() error = nil, want strict deferred project diagnostic failure")
	}
	if !hasDiagnosticCode(result.Diagnostics, "project.resource-scope-deferred") {
		t.Fatalf("Diagnostics = %#v, want deferred project diagnostic in all mode", result.Diagnostics)
	}
	if !hasStatus(result.Statuses, "demo", "FAIL") {
		t.Fatalf("Statuses = %#v, want FAIL", result.Statuses)
	}
}

func TestRenderProjectDiagnosticsOffHidesActionableDiagnostics(t *testing.T) {
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

	result, err := Render(context.Background(), Config{
		Path:                   root,
		Strict:                 true,
		ProjectDiagnosticsMode: ProjectDiagnosticsModeOff,
	})
	if err != nil {
		t.Fatalf("Render() error = %v; diagnostics = %#v", err, result.Diagnostics)
	}
	if hasDiagnostic(result.Diagnostics, "project", "source repository") {
		t.Fatalf("Diagnostics = %#v, want actionable project diagnostic hidden in off mode", result.Diagnostics)
	}
}

func TestRenderRejectsInvalidPublicProjectDiagnosticsModeAtOperationTime(t *testing.T) {
	root := t.TempDir()
	writeAPIAppTree(t, root, "demo", configMapBody("demo", "v1"))
	client := NewClient(Config{
		Path:                   root,
		ProjectDiagnosticsMode: ProjectDiagnosticsMode("verbose"),
	})

	_, err := client.Render(context.Background())
	if err == nil {
		t.Fatal("Render() error = nil, want invalid project diagnostics mode error")
	}
	if !strings.Contains(err.Error(), `project diagnostics mode`) {
		t.Fatalf("Render() error = %v, want project diagnostics mode validation", err)
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

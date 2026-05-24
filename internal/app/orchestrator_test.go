package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/appset"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/remote"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func TestOrchestratorDiagIncludesSettings(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "demo", "demo")
	writeTestFile(t, filepath.Join(root, "settings", "argocd-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.customizations.health.apps_Deployment: |
    return { status = "Healthy" }
`)

	result, err := Orchestrator{}.Diag(context.Background(), DiagRequest{Path: root})
	if err != nil {
		t.Fatalf("Diag() error = %v", err)
	}
	customization := result.Settings.ResourceCustomizations["apps/Deployment"]
	if !customization.HasHealthLua {
		t.Fatalf("Settings = %#v, want health Lua metadata", result.Settings)
	}
	if customization.HealthLuaSHA256 == "" {
		t.Fatalf("HealthLuaSHA256 = empty, want settings carried through")
	}
}

func TestOrchestratorBuildFiltersRenderedResources(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "demo", "demo")
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "secret.yaml"), `apiVersion: v1
kind: Secret
metadata:
  name: demo
stringData:
  password: secret
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:        root,
		SkipSecrets: true,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	if len(result.ApplicationManifests) != 1 {
		t.Fatalf("len(ApplicationManifests) = %d, want 1", len(result.ApplicationManifests))
	}
	if got := result.Manifests[0].Object.GetKind(); got != "ConfigMap" {
		t.Fatalf("filtered manifest kind = %q, want ConfigMap", got)
	}
}

func TestOrchestratorBuildAppliesConfigMapResourceExclusions(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "demo", "demo")
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "deployment.yaml"), `apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
spec:
  selector:
    matchLabels:
      app: demo
  template:
    metadata:
      labels:
        app: demo
    spec:
      containers:
        - name: demo
          image: example/demo:v1
`)
	writeTestFile(t, filepath.Join(root, "settings", "argocd-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.exclusions: |
    - apiGroups: [""]
      kinds: ["ConfigMap"]
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	if got := result.Manifests[0].Object.GetKind(); got != "Deployment" {
		t.Fatalf("rendered kind = %q, want Deployment", got)
	}
	if len(result.Settings.ResourceExclusions) != 1 {
		t.Fatalf("ResourceExclusions = %#v", result.Settings.ResourceExclusions)
	}
}

func TestOrchestratorBuildAppliesHelmValuesResourceExclusions(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "demo", "demo")
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "deployment.yaml"), `apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
spec:
  selector:
    matchLabels:
      app: demo
  template:
    metadata:
      labels:
        app: demo
    spec:
      containers:
        - name: demo
          image: example/demo:v1
`)
	writeTestFile(t, filepath.Join(root, "settings", "values.yaml"), `configs:
  cm:
    resource.exclusions: |
      - apiGroups: [""]
        kinds: ["ConfigMap"]
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	if got := result.Manifests[0].Object.GetKind(); got != "Deployment" {
		t.Fatalf("rendered kind = %q, want Deployment", got)
	}
}

func TestOrchestratorReportsLiveOnlyResourceCustomizationSettings(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "demo", "demo")
	writeTestFile(t, filepath.Join(root, "settings", "argocd-cm.yaml"), `apiVersion: v1
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

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !hasDiagnosticMessage(result.Diagnostics, "ignoreResourceUpdates are parsed but not applied") {
		t.Fatalf("Diagnostics = %#v, want ignoreResourceUpdates warning", result.Diagnostics)
	}
	if !hasDiagnosticMessage(result.Diagnostics, "health Lua is parsed as metadata only") {
		t.Fatalf("Diagnostics = %#v, want health Lua warning", result.Diagnostics)
	}
	if !hasDiagnosticMessage(result.Diagnostics, "useOpenLibs is parsed as metadata only") {
		t.Fatalf("Diagnostics = %#v, want useOpenLibs warning", result.Diagnostics)
	}
	if !hasDiagnosticMessage(result.Diagnostics, "actions are parsed as metadata only") {
		t.Fatalf("Diagnostics = %#v, want actions warning", result.Diagnostics)
	}
}

func TestOrchestratorBuildAppliesClusterScopedResourceInclusions(t *testing.T) {
	root := t.TempDir()
	writeBuildApplicationWithDestination(t, root, "prod", "prod-cm", "prod-west")
	writeBuildApplicationWithDestination(t, root, "dev", "dev-cm", "dev")
	writeTestFile(t, filepath.Join(root, "settings", "argocd-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.inclusions: |
    - apiGroups: ["apps"]
      kinds: ["Deployment"]
      clusters: ["prod-*"]
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	if got := result.Manifests[0].Object.GetName(); got != "dev-cm" {
		t.Fatalf("rendered name = %q, want dev-cm", got)
	}
}

func TestOrchestratorBuildStrictFailsOnInvalidSettings(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "demo", "demo")
	writeTestFile(t, filepath.Join(root, "settings", "argocd-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.exclusions: |
    - apiGroups: [
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, Strict: true})
	if err == nil {
		t.Fatal("Build() error = nil, want settings diagnostic failure")
	}
	diag, ok := diagnosticByCategory(result.Diagnostics, "settings")
	if !ok {
		t.Fatalf("Diagnostics = %#v, want settings diagnostic", result.Diagnostics)
	}
	if diag.Severity != diagnostic.SeverityError {
		t.Fatalf("settings diagnostic severity = %s, want error", diag.Severity)
	}
}

func TestOrchestratorReportsProjectValidationDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeBuildApplicationWithProject(t, root, "demo", "demo", "platform", "https://github.com/example/denied", "forbidden")
	writeTestFile(t, filepath.Join(root, "projects", "platform.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: platform
spec:
  sourceRepos:
    - https://github.com/example/allowed
  destinations:
    - server: https://kubernetes.default.svc
      namespace: workloads
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !hasDiagnosticMessage(result.Diagnostics, "source repository") {
		t.Fatalf("Diagnostics = %#v, want source policy warning", result.Diagnostics)
	}
	if !hasDiagnosticMessage(result.Diagnostics, "destination") {
		t.Fatalf("Diagnostics = %#v, want destination policy warning", result.Diagnostics)
	}
}

func TestOrchestratorProjectValidationStrictModeFails(t *testing.T) {
	root := t.TempDir()
	writeBuildApplicationWithProject(t, root, "demo", "demo", "platform", "https://github.com/example/denied", "workloads")
	writeTestFile(t, filepath.Join(root, "projects", "platform.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: platform
spec:
  sourceRepos:
    - https://github.com/example/allowed
  destinations:
    - server: https://kubernetes.default.svc
      namespace: workloads
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, Strict: true})
	if err == nil {
		t.Fatal("Build() error = nil, want strict project validation failure")
	}
	if len(result.Statuses) == 0 || result.Statuses[0].Status != ApplicationStatusSkipped {
		t.Fatalf("Statuses = %#v, want skipped status from strict project validation", result.Statuses)
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

func TestOrchestratorBuildFailsClosedForPluginSourceWithoutRenderer(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
	}{
		{
			name: "plain directory",
			files: map[string]string{
				filepath.Join("sources", "plugin", "plain.yaml"): `apiVersion: v1
kind: ConfigMap
metadata:
  name: should-not-render-as-directory
data:
  value: wrong
`,
			},
		},
		{
			name: "Kustomize-shaped directory",
			files: map[string]string{
				filepath.Join("sources", "plugin", "kustomization.yaml"): `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`,
				filepath.Join("sources", "plugin", "cm.yaml"): `apiVersion: v1
kind: ConfigMap
metadata:
  name: should-not-render-as-kustomize
data:
  value: wrong
`,
			},
		},
		{
			name: "Helm-shaped directory",
			files: map[string]string{
				filepath.Join("sources", "plugin", "Chart.yaml"): `apiVersion: v2
name: plugin
version: 0.1.0
`,
				filepath.Join("sources", "plugin", "templates", "cm.yaml"): `apiVersion: v1
kind: ConfigMap
metadata:
  name: should-not-render-as-helm
data:
  value: wrong
`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "apps", "plugin.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: plugin-app
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: sources/plugin
    plugin:
      name: cue
      env:
        - name: FEATURE
          value: enabled
  destination:
    name: in-cluster
    namespace: default
`)
			for path, content := range tt.files {
				writeTestFile(t, filepath.Join(root, path), content)
			}

			result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{Path: root})
			if err == nil {
				t.Fatalf("Build() error = nil, want unsupported plugin error")
			}
			if len(result.Manifests) != 0 {
				t.Fatalf("len(Manifests) = %d, want 0", len(result.Manifests))
			}
			if len(result.Statuses) != 1 || result.Statuses[0].Status != ApplicationStatusFail {
				t.Fatalf("statuses = %#v, want one FAIL", result.Statuses)
			}
			if !strings.Contains(result.Statuses[0].Message, "config management plugin cue is not supported without an injected plugin renderer") {
				t.Fatalf("status message = %q, want unsupported plugin renderer", result.Statuses[0].Message)
			}
			foundPluginDiagnostic := false
			for _, diag := range result.Diagnostics {
				if diag.Category == "plugin" {
					foundPluginDiagnostic = true
				}
			}
			if !foundPluginDiagnostic {
				t.Fatalf("diagnostics = %#v, want plugin diagnostic", result.Diagnostics)
			}
		})
	}
}

func TestOrchestratorPluginTimeoutReturnsPartialStatus(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "ok", "ok")
	writePluginBuildApplication(t, root, "plugin", "cue")

	result, err := (Orchestrator{PluginRenderer: blockingInternalPluginRenderer{}}).Build(context.Background(), BuildRequest{
		Path:          root,
		PluginTimeout: time.Nanosecond,
	})
	if err == nil {
		t.Fatal("Build() error = nil, want plugin timeout")
	}
	if _, ok := manifestByName(result.Manifests, "ok"); !ok {
		t.Fatalf("Manifests = %#v, want successful non-plugin manifest", result.Manifests)
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "ok", Status: ApplicationStatusPass},
		{Namespace: "argocd", Name: "plugin", Status: ApplicationStatusFail},
	})
	if !hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginFailed) {
		t.Fatalf("Diagnostics = %#v, want plugin.failed", result.Diagnostics)
	}
}

func TestOrchestratorInjectedPluginRendererErrorPreservesDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "ok", "ok")
	writePluginBuildApplication(t, root, "plugin", "cue")

	result, err := (Orchestrator{PluginRenderer: failingInternalPluginRenderer{}}).Build(context.Background(), BuildRequest{Path: root})
	if err == nil {
		t.Fatal("Build() error = nil, want plugin renderer error")
	}
	if _, ok := manifestByName(result.Manifests, "ok"); !ok {
		t.Fatalf("Manifests = %#v, want successful non-plugin manifest", result.Manifests)
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "ok", Status: ApplicationStatusPass},
		{Namespace: "argocd", Name: "plugin", Status: ApplicationStatusFail},
	})
	if !hasDiagnosticCode(result.Diagnostics, "plugin.custom") {
		t.Fatalf("Diagnostics = %#v, want renderer diagnostic", result.Diagnostics)
	}
	if !hasDiagnosticCode(result.Diagnostics, diagnostic.CodePluginFailed) {
		t.Fatalf("Diagnostics = %#v, want plugin.failed", result.Diagnostics)
	}
}

func TestBuildParallelismPreservesApplicationOrder(t *testing.T) {
	root := t.TempDir()
	chartRoot := t.TempDir()
	writeTestChart(t, chartRoot, "first")
	writeTestChart(t, chartRoot, "middle")
	writeTestChart(t, chartRoot, "last")
	acquirer := newControlledChartAcquirer(chartRoot, []string{"first", "middle", "last"})

	resultCh := make(chan struct {
		result BuildResult
		err    error
	}, 1)
	go func() {
		result, err := (Orchestrator{ChartAcquirer: acquirer}).Build(context.Background(), BuildRequest{
			Path:        root,
			Parallelism: 3,
			Applications: []argoappv1.Application{
				chartOnlyApplication("first", "first", "1.0.0"),
				chartOnlyApplication("middle", "middle", "1.0.0"),
				chartOnlyApplication("last", "last", "1.0.0"),
			},
		})
		resultCh <- struct {
			result BuildResult
			err    error
		}{result: result, err: err}
	}()

	acquirer.waitStarted(t, "first", "middle", "last")
	acquirer.release("last")
	acquirer.release("middle")
	acquirer.release("first")

	out := <-resultCh
	if out.err != nil {
		t.Fatalf("Build() error = %v", out.err)
	}
	assertManifestNames(t, out.result.Manifests, []string{"first", "middle", "last"})
	assertApplicationStatusOrder(t, out.result.Statuses, []string{
		"argocd/first:PASS",
		"argocd/middle:PASS",
		"argocd/last:PASS",
	})
}

func TestBuildParallelismPreservesPartialFailureStatuses(t *testing.T) {
	root := t.TempDir()
	chartRoot := t.TempDir()
	writeTestChart(t, chartRoot, "first")
	writeTestChart(t, chartRoot, "middle")
	writeTestChart(t, chartRoot, "last")
	acquirer := newControlledChartAcquirer(chartRoot, []string{"first", "middle", "last"})
	acquirer.fail["middle"] = errors.New("planned chart failure")

	resultCh := make(chan struct {
		result BuildResult
		err    error
	}, 1)
	go func() {
		result, err := (Orchestrator{ChartAcquirer: acquirer}).Build(context.Background(), BuildRequest{
			Path:        root,
			Parallelism: 3,
			Applications: []argoappv1.Application{
				chartOnlyApplication("first", "first", "1.0.0"),
				chartOnlyApplication("middle", "middle", "1.0.0"),
				chartOnlyApplication("last", "last", "1.0.0"),
			},
		})
		resultCh <- struct {
			result BuildResult
			err    error
		}{result: result, err: err}
	}()

	acquirer.waitStarted(t, "first", "middle", "last")
	acquirer.release("last")
	acquirer.release("middle")
	acquirer.release("first")

	out := <-resultCh
	if out.err == nil {
		t.Fatal("Build() error = nil, want partial failure")
	}
	if !strings.Contains(out.err.Error(), "1 Application failed: argocd/middle:") ||
		!strings.Contains(out.err.Error(), "acquire chart middle: planned chart failure") {
		t.Fatalf("Build() error = %q, want stable middle failure", out.err.Error())
	}
	assertApplicationStatusOrder(t, out.result.Statuses, []string{
		"argocd/first:PASS",
		"argocd/middle:FAIL",
		"argocd/last:PASS",
	})
	assertManifestNames(t, out.result.Manifests, []string{"first", "last"})
	if len(out.result.Diagnostics) == 0 || !strings.Contains(out.result.Diagnostics[0].Message, "middle") {
		t.Fatalf("Diagnostics = %#v, want middle render diagnostic in order", out.result.Diagnostics)
	}
}

func TestBuildParallelismPreservesCacheEventOrder(t *testing.T) {
	root := t.TempDir()
	chartRoot := t.TempDir()
	writeTestChart(t, chartRoot, "first")
	writeTestChart(t, chartRoot, "middle")
	writeTestChart(t, chartRoot, "last")
	acquirer := newControlledChartAcquirer(chartRoot, []string{"first", "middle", "last"})

	resultCh := make(chan struct {
		result BuildResult
		err    error
	}, 1)
	go func() {
		result, err := (Orchestrator{ChartAcquirer: acquirer}).Build(context.Background(), BuildRequest{
			Path:              root,
			Parallelism:       3,
			RecordCacheEvents: true,
			Applications: []argoappv1.Application{
				chartOnlyApplication("first", "first", "1.0.0"),
				chartOnlyApplication("middle", "middle", "2.0.0"),
				chartOnlyApplication("last", "last", "3.0.0"),
			},
		})
		resultCh <- struct {
			result BuildResult
			err    error
		}{result: result, err: err}
	}()

	acquirer.waitStarted(t, "first", "middle", "last")
	acquirer.release("last")
	acquirer.release("middle")
	acquirer.release("first")

	out := <-resultCh
	if out.err != nil {
		t.Fatalf("Build() error = %v", out.err)
	}
	var revisions []string
	for _, event := range out.result.CacheEvents {
		revisions = append(revisions, event.Revision)
	}
	if !slices.Equal(revisions, []string{"1.0.0", "2.0.0", "3.0.0"}) {
		t.Fatalf("cache event revisions = %#v, want selected Application order", revisions)
	}
}

func TestBuildParallelismSerializesSameCacheTargetAcquisition(t *testing.T) {
	root := t.TempDir()
	chartRoot := t.TempDir()
	writeTestChart(t, chartRoot, "shared")
	acquirer := newControlledChartAcquirer(chartRoot, []string{"shared"})

	resultCh := make(chan struct {
		result BuildResult
		err    error
	}, 1)
	go func() {
		result, err := (Orchestrator{ChartAcquirer: acquirer}).Build(context.Background(), BuildRequest{
			Path:        root,
			Parallelism: 2,
			Applications: []argoappv1.Application{
				chartOnlyApplication("one", "shared", "1.0.0"),
				chartOnlyApplication("two", "shared", "1.0.0"),
			},
		})
		resultCh <- struct {
			result BuildResult
			err    error
		}{result: result, err: err}
	}()

	acquirer.waitStarted(t, "shared")
	select {
	case name := <-acquirer.started:
		t.Fatalf("second acquisition for %s started before first shared target was released", name)
	case <-time.After(50 * time.Millisecond):
	}
	acquirer.release("shared")
	out := <-resultCh
	if out.err != nil {
		t.Fatalf("Build() error = %v", out.err)
	}
	if got := acquirer.maxActive(); got != 1 {
		t.Fatalf("max concurrent acquisitions = %d, want 1", got)
	}
}

func TestBuildParallelismSerializesSameCacheTargetAcrossConcurrentBuilds(t *testing.T) {
	chartRoot := t.TempDir()
	cacheDir := t.TempDir()
	writeTestChart(t, chartRoot, "shared")
	acquirer := newControlledChartAcquirer(chartRoot, []string{"shared"})

	resultCh := make(chan struct {
		result BuildResult
		err    error
	}, 2)
	roots := map[string]string{
		"one": t.TempDir(),
		"two": t.TempDir(),
	}
	for _, name := range []string{"one", "two"} {
		name := name
		root := roots[name]
		go func() {
			result, err := (Orchestrator{ChartAcquirer: acquirer}).Build(context.Background(), BuildRequest{
				Path:          root,
				ChartCacheDir: cacheDir,
				Parallelism:   2,
				Applications: []argoappv1.Application{
					chartOnlyApplication(name, "shared", "1.0.0"),
				},
			})
			resultCh <- struct {
				result BuildResult
				err    error
			}{result: result, err: err}
		}()
	}

	acquirer.waitStarted(t, "shared")
	select {
	case name := <-acquirer.started:
		t.Fatalf("second build acquisition for %s started before first shared target was released", name)
	case <-time.After(50 * time.Millisecond):
	}
	acquirer.release("shared")
	for range 2 {
		out := <-resultCh
		if out.err != nil {
			t.Fatalf("Build() error = %v", out.err)
		}
	}
	if got := acquirer.maxActive(); got != 1 {
		t.Fatalf("max concurrent acquisitions = %d, want 1 across concurrent builds", got)
	}
}

func TestBuildParallelismProtectsSameCacheTargetDuringRenderRead(t *testing.T) {
	root := t.TempDir()
	cacheRoot := t.TempDir()
	writeTestFile(t, filepath.Join(cacheRoot, "manifests", "snapshot", ".keep"), "")
	writeTestFile(t, filepath.Join(cacheRoot, "marker.txt"), "before")

	renderStarted := make(chan string, 1)
	releaseRender := make(chan struct{})
	renderer := internalPluginRendererFunc(func(_ context.Context, request render.PluginRequest) ([]render.Manifest, []diagnostic.Diagnostic, error) {
		renderStarted <- request.Source.RepoRoot
		<-releaseRender
		value, err := os.ReadFile(filepath.Join(request.Source.RepoRoot, "marker.txt"))
		if err != nil {
			return nil, nil, err
		}
		return []render.Manifest{{Object: cm("snapshot", string(value))}}, nil, nil
	})

	resultCh := make(chan struct {
		result BuildResult
		err    error
	}, 1)
	go func() {
		result, err := (Orchestrator{
			GitAcquirer:    staticGitAcquirer{path: cacheRoot},
			PluginRenderer: renderer,
		}).Build(context.Background(), BuildRequest{
			Path:         root,
			AllowNetwork: true,
			Parallelism:  2,
			Applications: []argoappv1.Application{
				pluginApplication("snapshot"),
			},
		})
		resultCh <- struct {
			result BuildResult
			err    error
		}{result: result, err: err}
	}()

	snapshotRoot := <-renderStarted
	if snapshotRoot == cacheRoot {
		t.Fatalf("plugin rendered from mutable cache root %q, want snapshot", snapshotRoot)
	}
	writeTestFile(t, filepath.Join(cacheRoot, "marker.txt"), "after")
	close(releaseRender)

	out := <-resultCh
	if out.err != nil {
		t.Fatalf("Build() error = %v", out.err)
	}
	value, _, _ := unstructured.NestedString(out.result.Manifests[0].Object.Object, "data", "value")
	if value != "before" {
		t.Fatalf("rendered value = %q, want snapshot value before cache mutation", value)
	}
}

func TestBuildParallelismRejectsNegativeValue(t *testing.T) {
	result, err := (Orchestrator{}).Build(context.Background(), BuildRequest{
		Path:        t.TempDir(),
		Parallelism: -1,
	})
	if err == nil {
		t.Fatal("Build() error = nil, want parallelism validation error")
	}
	if len(result.Statuses) != 0 {
		t.Fatalf("Statuses = %#v, want validation before rendering", result.Statuses)
	}
}

func TestBuildParallelismHonorsCallerCancellation(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "manifests", "cancelled", ".keep"), "")
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	renderer := internalPluginRendererFunc(func(ctx context.Context, _ render.PluginRequest) ([]render.Manifest, []diagnostic.Diagnostic, error) {
		close(started)
		<-ctx.Done()
		return nil, nil, ctx.Err()
	})

	resultCh := make(chan struct {
		result BuildResult
		err    error
	}, 1)
	go func() {
		result, err := (Orchestrator{PluginRenderer: renderer}).Build(ctx, BuildRequest{
			Path:        root,
			Parallelism: 2,
			Applications: []argoappv1.Application{
				pluginApplication("cancelled"),
			},
		})
		resultCh <- struct {
			result BuildResult
			err    error
		}{result: result, err: err}
	}()

	<-started
	cancel()
	out := <-resultCh
	if !errors.Is(out.err, context.Canceled) {
		t.Fatalf("Build() error = %v, want context canceled", out.err)
	}
	assertApplicationStatusOrder(t, out.result.Statuses, []string{"argocd/cancelled:FAIL"})
}

func TestDiffRequestPropagatesParallelism(t *testing.T) {
	request := DiffRequest{Parallelism: 4}

	left := request.buildRequest("left", []string{"left", "right"})
	right := request.buildRequest("right", []string{"left", "right"})

	if left.Parallelism != 4 || right.Parallelism != 4 {
		t.Fatalf("Parallelism left/right = %d/%d, want 4/4", left.Parallelism, right.Parallelism)
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

func TestOrchestratorBuildRendersRepoMappedPathSource(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	writeExternalPathApplication(t, root, "https://github.com/example/external", "manifests/external")
	writeTestFile(t, filepath.Join(external, "manifests", "external", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: external
data:
  source: repo-map
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path: root,
		RepoMaps: []sourcepkg.RepoMap{{
			URL:  "https://github.com/example/external.git",
			Path: external,
		}},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	value, found, err := unstructured.NestedString(result.Manifests[0].Object.Object, "data", "source")
	if err != nil || !found || value != "repo-map" {
		t.Fatalf("data.source = %q, found %v, err %v; want repo-map", value, found, err)
	}
}

func TestOrchestratorBuildErrorsForMissingUnmappedPathSource(t *testing.T) {
	root := t.TempDir()
	writeExternalPathApplication(t, root, "https://github.com/example/external", "manifests/external")

	_, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err == nil {
		t.Fatal("Build() error = nil, want missing unmapped source error")
	}
	for _, want := range []string{"manifests/external", "--repo-map", "--allow-network"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Build() error = %q, want %q", err.Error(), want)
		}
	}
}

func TestOrchestratorBuildFetchesMissingPathSourceWhenNetworkAllowed(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	cacheDir := t.TempDir()
	writeExternalPathApplication(t, root, "https://github.com/example/external", "manifests/external")
	writeTestFile(t, filepath.Join(external, "manifests", "external", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: external
data:
  source: fetched
`)
	acquirer := &recordingGitAcquirer{path: external, revision: "abc123"}

	result, err := (Orchestrator{GitAcquirer: acquirer}).Build(context.Background(), BuildRequest{
		Path:         root,
		AllowNetwork: true,
		GitCacheDir:  cacheDir,
		RefreshGit:   true,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(acquirer.requests) != 1 {
		t.Fatalf("git acquire calls = %d, want 1", len(acquirer.requests))
	}
	if acquirer.requests[0] != (sourcepkg.GitRequest{URL: "https://github.com/example/external", Revision: "main"}) {
		t.Fatalf("git request = %#v", acquirer.requests[0])
	}
	if acquirer.options[0] != (sourcepkg.GitOptions{AllowNetwork: true, CacheDir: cacheDir, Refresh: true}) {
		t.Fatalf("git options = %#v", acquirer.options[0])
	}
	value, found, err := unstructured.NestedString(result.Manifests[0].Object.Object, "data", "source")
	if err != nil || !found || value != "fetched" {
		t.Fatalf("data.source = %q, found %v, err %v; want fetched", value, found, err)
	}
}

func TestOrchestratorBuildRejectsOfflineWithGitNetwork(t *testing.T) {
	root := t.TempDir()
	writeExternalPathApplication(t, root, "https://github.com/example/external", "manifests/external")

	_, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:         root,
		Offline:      true,
		AllowNetwork: true,
	})
	if err == nil {
		t.Fatal("Build() error = nil, want offline allow-network error")
	}
	if !strings.Contains(err.Error(), "--offline cannot be combined with --allow-network") {
		t.Fatalf("Build() error = %q, want offline allow-network message", err.Error())
	}
}

func TestOrchestratorBuildRejectsDefaultGitCacheInsideRepoRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, ".cache"))
	writeExternalPathApplication(t, root, "https://github.com/example/external", "manifests/external")

	_, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:         root,
		AllowNetwork: true,
	})
	if err == nil {
		t.Fatal("Build() error = nil, want git cache location error")
	}
	if !strings.Contains(err.Error(), "git cache dir") || !strings.Contains(err.Error(), "must not be inside repository root") {
		t.Fatalf("Build() error = %q, want git cache location error", err.Error())
	}
}

func TestOrchestratorBuildRejectsGitCacheInsideRepoMapRoot(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	writeExternalPathApplication(t, root, "https://github.com/example/external", "manifests/external")

	_, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:         root,
		AllowNetwork: true,
		GitCacheDir:  filepath.Join(external, ".drydock", "git"),
		RepoMaps: []sourcepkg.RepoMap{{
			URL:  "https://github.com/example/external.git",
			Path: external,
		}},
	})
	if err == nil {
		t.Fatal("Build() error = nil, want git cache location error")
	}
	if !strings.Contains(err.Error(), "git cache dir") || !strings.Contains(err.Error(), "must not be inside repository root") {
		t.Fatalf("Build() error = %q, want git cache location error", err.Error())
	}
}

func TestOrchestratorBuildUsesRepoMappedHelmValueRef(t *testing.T) {
	root := t.TempDir()
	valuesRoot := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "helm-ref.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: helm-ref
  namespace: argocd
spec:
  sources:
    - repoURL: https://github.com/example/values
      targetRevision: main
      ref: values
    - repoURL: https://github.com/example/repo
      targetRevision: main
      path: charts/demo
      helm:
        valueFiles:
          - $values/values.yaml
  destination:
    name: in-cluster
    namespace: default
`)
	writeAppTestValueChart(t, filepath.Join(root, "charts", "demo"))
	writeTestFile(t, filepath.Join(valuesRoot, "values.yaml"), `value: from-mapped-ref
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path: root,
		RepoMaps: []sourcepkg.RepoMap{{
			URL:  "https://github.com/example/values.git",
			Path: valuesRoot,
		}},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	value, found, err := unstructured.NestedString(result.Manifests[0].Object.Object, "data", "value")
	if err != nil || !found || value != "from-mapped-ref" {
		t.Fatalf("data.value = %q, found %v, err %v; want from-mapped-ref", value, found, err)
	}
}

func TestOrchestratorBuildUsesRepoMappedHelmValueRefFromRepoRootWhenRefHasPath(t *testing.T) {
	root := t.TempDir()
	valuesRoot := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "helm-ref-path.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: helm-ref-path
  namespace: argocd
spec:
  sources:
    - repoURL: https://github.com/example/values
      targetRevision: main
      ref: values
      path: value-manifests
    - repoURL: https://github.com/example/repo
      targetRevision: main
      path: charts/demo
      helm:
        valueFiles:
          - $values/root-values.yaml
  destination:
    name: in-cluster
    namespace: default
`)
	writeAppTestValueChart(t, filepath.Join(root, "charts", "demo"))
	writeTestFile(t, filepath.Join(valuesRoot, "root-values.yaml"), `value: from-root-ref
`)
	writeTestFile(t, filepath.Join(valuesRoot, "value-manifests", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: ref-source
data:
  source: ref-path
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path: root,
		RepoMaps: []sourcepkg.RepoMap{{
			URL:  "https://github.com/example/values.git",
			Path: valuesRoot,
		}},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(result.Manifests) != 2 {
		t.Fatalf("len(Manifests) = %d, want 2", len(result.Manifests))
	}

	helmManifest, ok := manifestByName(result.Manifests, "demo")
	if !ok {
		t.Fatalf("missing Helm manifest demo: %#v", result.Manifests)
	}
	value, found, err := unstructured.NestedString(helmManifest.Object.Object, "data", "value")
	if err != nil || !found || value != "from-root-ref" {
		t.Fatalf("data.value = %q, found %v, err %v; want from-root-ref", value, found, err)
	}

	refManifest, ok := manifestByName(result.Manifests, "ref-source")
	if !ok {
		t.Fatalf("missing ref source manifest ref-source: %#v", result.Manifests)
	}
	source, found, err := unstructured.NestedString(refManifest.Object.Object, "data", "source")
	if err != nil || !found || source != "ref-path" {
		t.Fatalf("data.source = %q, found %v, err %v; want ref-path", source, found, err)
	}
}

func TestOrchestratorBuildUsesFetchedSourceRootForSameRepoHelmValueRef(t *testing.T) {
	root := t.TempDir()
	fetchedRoot := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "helm-ref-same-repo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: helm-ref-same-repo
  namespace: argocd
spec:
  sources:
    - repoURL: https://github.com/example/repo
      targetRevision: main
      ref: values
    - repoURL: https://github.com/example/repo
      targetRevision: main
      path: charts/demo
      helm:
        valueFiles:
          - $values/root-values.yaml
  destination:
    name: in-cluster
    namespace: default
`)
	writeAppTestValueChart(t, filepath.Join(fetchedRoot, "charts", "demo"))
	writeTestFile(t, filepath.Join(fetchedRoot, "root-values.yaml"), `value: from-fetched-root
`)
	acquirer := &recordingGitAcquirer{path: fetchedRoot, revision: "abc123"}

	result, err := (Orchestrator{GitAcquirer: acquirer}).Build(context.Background(), BuildRequest{
		Path:         root,
		AllowNetwork: true,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(acquirer.requests) != 1 {
		t.Fatalf("git acquire calls = %d, want 1", len(acquirer.requests))
	}
	value, found, err := unstructured.NestedString(result.Manifests[0].Object.Object, "data", "value")
	if err != nil || !found || value != "from-fetched-root" {
		t.Fatalf("data.value = %q, found %v, err %v; want from-fetched-root", value, found, err)
	}
}

func TestOrchestratorBuildResolvesSameRepoHelmValueRefWithDifferentRevision(t *testing.T) {
	root := t.TempDir()
	chartRoot := t.TempDir()
	valuesRoot := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "helm-ref-same-repo-revision.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: helm-ref-same-repo-revision
  namespace: argocd
spec:
  sources:
    - repoURL: https://github.com/example/repo
      targetRevision: values-revision
      ref: values
    - repoURL: https://github.com/example/repo
      targetRevision: chart-revision
      path: charts/demo
      helm:
        valueFiles:
          - $values/root-values.yaml
  destination:
    name: in-cluster
    namespace: default
`)
	writeAppTestValueChart(t, filepath.Join(chartRoot, "charts", "demo"))
	writeTestFile(t, filepath.Join(valuesRoot, "root-values.yaml"), `value: from-values-revision
`)
	acquirer := &recordingGitAcquirer{
		paths: map[string]string{
			"chart-revision":  chartRoot,
			"values-revision": valuesRoot,
		},
		revisions: map[string]string{
			"chart-revision":  "chart-sha",
			"values-revision": "values-sha",
		},
	}

	result, err := (Orchestrator{GitAcquirer: acquirer}).Build(context.Background(), BuildRequest{
		Path:         root,
		AllowNetwork: true,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(acquirer.requests) != 2 {
		t.Fatalf("git acquire calls = %d, want 2: %#v", len(acquirer.requests), acquirer.requests)
	}
	wantRevisions := []string{"chart-revision", "values-revision"}
	for i, want := range wantRevisions {
		if got := acquirer.requests[i].Revision; got != want {
			t.Fatalf("git request[%d].Revision = %q, want %q; requests %#v", i, got, want, acquirer.requests)
		}
	}
	value, found, err := unstructured.NestedString(result.Manifests[0].Object.Object, "data", "value")
	if err != nil || !found || value != "from-values-revision" {
		t.Fatalf("data.value = %q, found %v, err %v; want from-values-revision", value, found, err)
	}
}

func TestOrchestratorBuildRejectsUnmappedCrossRepoHelmValueRefEvenWhenLocalValueFileExists(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "helm-ref-unmapped.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: helm-ref-unmapped
  namespace: argocd
spec:
  sources:
    - repoURL: https://github.com/example/values
      targetRevision: main
      ref: values
    - repoURL: https://github.com/example/repo
      targetRevision: main
      path: charts/demo
      helm:
        valueFiles:
          - $values/leaked-values.yaml
  destination:
    name: in-cluster
    namespace: default
`)
	writeAppTestValueChart(t, filepath.Join(root, "charts", "demo"))
	writeTestFile(t, filepath.Join(root, "leaked-values.yaml"), `value: from-current-repo
`)

	_, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err == nil {
		t.Fatal("Build() error = nil, want unmapped ref repository error")
	}
	for _, want := range []string{"ref root $values", "--repo-map", "--allow-network"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Build() error = %q, want %q", err.Error(), want)
		}
	}
}

func TestOrchestratorBuildPreservesPartialResults(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "valid", "valid")
	writeExternalPathApplicationNamed(t, root, "invalid", "https://github.com/example/missing", "manifests/missing")

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err == nil {
		t.Fatal("Build() error = nil, want aggregate render error")
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	if result.Manifests[0].Object.GetName() != "valid" {
		t.Fatalf("manifest name = %q, want valid", result.Manifests[0].Object.GetName())
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "valid", Status: ApplicationStatusPass},
		{Namespace: "argocd", Name: "invalid", Status: ApplicationStatusFail},
	})
	diag, ok := diagnosticByCategory(result.Diagnostics, "render")
	if !ok {
		t.Fatalf("Diagnostics = %#v, want render diagnostic", result.Diagnostics)
	}
	if diag.Severity != diagnostic.SeverityError {
		t.Fatalf("render diagnostic severity = %s, want error", diag.Severity)
	}
	if !strings.Contains(diag.Message, "invalid") || !strings.Contains(diag.Message, "--repo-map") {
		t.Fatalf("render diagnostic message = %q, want app context and render error", diag.Message)
	}
	if !strings.Contains(err.Error(), "1 Application failed") {
		t.Fatalf("Build() error = %q, want aggregate failure count", err.Error())
	}
}

func TestOrchestratorBuildStatusesIncludePassAndFailForPlanningErrors(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "valid", "valid")
	writeTestFile(t, filepath.Join(root, "apps", "bad-ref.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: bad-ref
  namespace: argocd
spec:
  sources:
    - repoURL: https://github.com/example/repo
      targetRevision: main
      ref: "bad/ref"
  destination:
    name: in-cluster
    namespace: default
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err == nil {
		t.Fatal("Build() error = nil, want planning error")
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "valid", Status: ApplicationStatusPass},
		{Namespace: "argocd", Name: "bad-ref", Status: ApplicationStatusFail},
	})
}

func TestOrchestratorBuildMarksApplicationsSkippedWhenPreconditionFails(t *testing.T) {
	root := t.TempDir()
	writeUnsupportedApplicationSetFixture(t, root)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, Strict: true})
	if err == nil {
		t.Fatal("Build() error = nil, want strict ApplicationSet precondition error")
	}
	if len(result.Manifests) != 0 {
		t.Fatalf("len(Manifests) = %d, want 0", len(result.Manifests))
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "direct", Status: ApplicationStatusSkipped},
	})
	if !strings.Contains(result.Statuses[0].Message, "unsupported ApplicationSet generator") {
		t.Fatalf("skipped message = %q, want precondition error", result.Statuses[0].Message)
	}
}

func TestOrchestratorBuildReturnsChartAcquireErrorForChartOnlySource(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "argocd", "chart-app.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: chart-only
  namespace: argocd
spec:
  source:
    repoURL: https://repo-user:repo-secret@charts.example.test?token=repo-token#repo-frag
    targetRevision: 1.2.3
    chart: demo
  destination:
    name: in-cluster
    namespace: chart-only
`)

	acquirer := &recordingChartAcquirer{
		acquireErr: errors.New("fetch https://repo-user:repo-secret@charts.example.test?token=repo-token#repo-frag failed"),
	}
	result, err := (Orchestrator{ChartAcquirer: acquirer}).Build(context.Background(), BuildRequest{Path: root})
	if err == nil {
		t.Fatalf("Build() error = nil, want chart acquire error")
	}
	if len(result.Manifests) != 0 {
		t.Fatalf("len(Manifests) = %d, want 0", len(result.Manifests))
	}
	for _, want := range []string{`chart="demo"`, "acquire chart demo", "https://charts.example.test"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Build() error = %q, want %q", err.Error(), want)
		}
	}
	for _, leaked := range []string{"repo-user", "repo-secret", "repo-token", "repo-frag"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("Build() error = %q, leaked %q", err.Error(), leaked)
		}
	}
}

func TestOrchestratorBuildRendersChartOnlyApplication(t *testing.T) {
	root := t.TempDir()
	chartDir := filepath.Join(root, "cache", "demo")
	cacheDir := filepath.Join(root, "chart-cache")
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
    helm:
      valueFiles:
        - values-extra.yaml
      values: |
        value: from-inline
  destination:
    name: in-cluster
    namespace: chart-ns
`)
	writeTestFile(t, filepath.Join(chartDir, "Chart.yaml"), `apiVersion: v2
name: demo
version: 1.2.3
`)
	writeTestFile(t, filepath.Join(chartDir, "values-extra.yaml"), `value: from-file
`)
	writeTestFile(t, filepath.Join(chartDir, "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}
data:
  value: {{ .Values.value | quote }}
`)
	acquirer := &recordingChartAcquirer{chartDir: chartDir}

	result, err := (Orchestrator{ChartAcquirer: acquirer}).Build(context.Background(), BuildRequest{
		Path:          root,
		ChartCacheDir: cacheDir,
		Offline:       true,
		RefreshCharts: true,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if len(acquirer.requests) != 1 {
		t.Fatalf("chart acquire calls = %d, want 1", len(acquirer.requests))
	}
	if got, want := acquirer.requests[0], (chart.Request{
		Repository: "https://charts.example.test",
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       chart.RepositoryHTTP,
	}); got != want {
		t.Fatalf("chart request = %#v, want %#v", got, want)
	}
	if got, want := acquirer.options[0], (chart.Options{
		CacheDir: cacheDir,
		Offline:  true,
		Refresh:  true,
	}); got != want {
		t.Fatalf("chart options = %#v, want %#v", got, want)
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	manifest := result.Manifests[0]
	if manifest.Object.GetKind() != "ConfigMap" || manifest.Object.GetName() != "chart-only" {
		t.Fatalf("rendered object = %s/%s, want ConfigMap/chart-only", manifest.Object.GetKind(), manifest.Object.GetName())
	}
	if namespace := manifest.Object.GetNamespace(); namespace != "chart-ns" {
		t.Fatalf("namespace = %q, want chart-ns", namespace)
	}
	value, found, err := unstructured.NestedString(manifest.Object.Object, "data", "value")
	if err != nil || !found || value != "from-inline" {
		t.Fatalf("data.value = %q, found %v, err %v; want from-inline", value, found, err)
	}
}

func TestOrchestratorRecordsChartCacheEvents(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "argocd", "charted.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: charted
  namespace: argocd
spec:
  source:
    repoURL: https://charts.example.test
    targetRevision: 1.2.3
    chart: demo
  destination:
    name: in-cluster
    namespace: charted
`)
	chartDir := filepath.Join(t.TempDir(), "demo")
	writeTestFile(t, filepath.Join(chartDir, "Chart.yaml"), `apiVersion: v2
name: demo
version: 1.2.3
`)
	writeTestFile(t, filepath.Join(chartDir, "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: charted
`)

	result, err := Orchestrator{ChartAcquirer: &recordingChartAcquirer{chartDir: chartDir, fromCache: true}}.Build(context.Background(), BuildRequest{
		Path:              root,
		RecordCacheEvents: true,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !hasCacheEvent(result.CacheEvents, "chart", "hit", "https://charts.example.test") {
		t.Fatalf("CacheEvents = %#v, want chart cache hit", result.CacheEvents)
	}
}

func TestOrchestratorRecordsRedactedGitCacheErrors(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "external.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: external
  namespace: argocd
spec:
  source:
    repoURL: https://user:secret@example.test/repo.git?token=abc#frag
    path: missing
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)

	result, err := Orchestrator{GitAcquirer: &recordingGitAcquirer{err: errors.New("offline cache miss for https://user:secret@example.test/repo.git?token=abc#frag")}}.Build(context.Background(), BuildRequest{
		Path:              root,
		AllowNetwork:      true,
		RecordCacheEvents: true,
	})
	if err == nil {
		t.Fatal("Build() error = nil, want failing Git acquire")
	}
	if len(result.CacheEvents) == 0 {
		t.Fatalf("CacheEvents = %#v, want Git cache event", result.CacheEvents)
	}
	text := fmt.Sprintf("err=%v diagnostics=%#v statuses=%#v events=%#v", err, result.Diagnostics, result.Statuses, result.CacheEvents)
	for _, leaked := range []string{"user", "secret", "token", "abc", "frag"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("result leaked %q: %s", leaked, text)
		}
	}
}

func TestLocalProviderClassifiesChartOnlyOCIRepository(t *testing.T) {
	root := t.TempDir()
	chartDir := filepath.Join(root, "cache", "demo")
	writeAppTestValueChart(t, chartDir)
	acquirer := &recordingChartAcquirer{chartDir: chartDir}

	_, _, err := (localProvider{
		repoRoot:      root,
		chartAcquirer: acquirer,
	}).RenderSource(context.Background(), render.ResolvedSource{
		Chart:          "demo",
		RepoURL:        " oci://registry.example.test/charts ",
		TargetRevision: "2.0.0",
	}, render.RenderOptions{AppName: "demo"})
	if err != nil {
		t.Fatalf("RenderSource() error = %v", err)
	}
	if len(acquirer.requests) != 1 {
		t.Fatalf("chart acquire calls = %d, want 1", len(acquirer.requests))
	}
	if got := acquirer.requests[0].Kind; got != chart.RepositoryOCI {
		t.Fatalf("request kind = %q, want %q", got, chart.RepositoryOCI)
	}
}

func TestOrchestratorPassesChartCredentialsToKustomizeHelmCharts(t *testing.T) {
	root := t.TempDir()
	chartDir := filepath.Join(root, "cache", "demo")
	writeAppTestValueChart(t, chartDir)
	writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests/demo
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
helmCharts:
  - name: chart
    repo: https://charts.example.test
    version: 0.1.0
    releaseName: demo
`)
	credentials := chart.ChartCredentials{
		Username:       "helm-user",
		Password:       "helm-pass",
		BearerToken:    "helm-token",
		RegistryConfig: filepath.Join(root, "registry.json"),
	}
	acquirer := &recordingChartAcquirer{chartDir: chartDir}

	if _, err := (Orchestrator{ChartAcquirer: acquirer}).Build(context.Background(), BuildRequest{
		Path:             root,
		ChartCredentials: credentials,
	}); err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(acquirer.options) != 1 {
		t.Fatalf("chart options = %d, want 1", len(acquirer.options))
	}
	if got := acquirer.options[0].Credentials; got != credentials {
		t.Fatalf("chart credentials = %#v, want %#v", got, credentials)
	}
}

func TestOrchestratorPassesRemoteCredentialsToKustomizeRenderer(t *testing.T) {
	root := t.TempDir()
	remoteFile := filepath.Join(t.TempDir(), "remote.yaml")
	writeTestFile(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests/demo
    targetRevision: main
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://raw.githubusercontent.com/example/repo/main/remote.yaml
`)
	writeTestFile(t, remoteFile, `apiVersion: v1
kind: ConfigMap
metadata:
  name: remote
`)
	remoteCredentials := remote.Credentials{
		Username:    "remote-user",
		Password:    "remote-pass",
		BearerToken: "remote-token",
	}
	remoteGitCredentials := remote.GitCredentials{
		Username:          "git-user",
		Password:          "git-pass",
		BearerToken:       "git-token",
		SSHPrivateKeyPath: filepath.Join(root, "id_ed25519"),
		SSHPassphrase:     "git-phrase",
		SSHKnownHostsPath: filepath.Join(root, "known_hosts"),
	}
	acquirer := &recordingRemoteAcquirer{path: remoteFile}

	if _, err := (Orchestrator{RemoteResourceAcquirer: acquirer}).Build(context.Background(), BuildRequest{
		Path:                         root,
		Offline:                      true,
		RefreshRemoteResources:       true,
		RemoteResourceCacheDir:       t.TempDir(),
		RemoteResourceCredentials:    remoteCredentials,
		RemoteResourceGitCredentials: remoteGitCredentials,
	}); err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(acquirer.options) != 1 {
		t.Fatalf("remote options = %d, want 1", len(acquirer.options))
	}
	if got := acquirer.options[0].Credentials; got != remoteCredentials {
		t.Fatalf("remote credentials = %#v, want %#v", got, remoteCredentials)
	}
	if got := acquirer.options[0].GitCredentials; got != remoteGitCredentials {
		t.Fatalf("remote git credentials = %#v, want %#v", got, remoteGitCredentials)
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

func TestOrchestratorBuildAppRendersOnlyNamedApplication(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "first", "one")
	writeBuildApplication(t, root, "second", "two")

	result, err := Orchestrator{}.BuildApp(context.Background(), BuildAppRequest{
		Name: "second",
		BuildRequest: BuildRequest{
			Path: root,
		},
	})
	if err != nil {
		t.Fatalf("BuildApp() error = %v", err)
	}
	if len(result.Applications) != 1 || result.Applications[0].Name != "second" {
		t.Fatalf("Applications = %#v, want only second", result.Applications)
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	if result.Manifests[0].Object.GetName() != "two" {
		t.Fatalf("rendered ConfigMap name = %q, want two", result.Manifests[0].Object.GetName())
	}
}

func TestOrchestratorBuildAppPreservesSelectedApplicationInputs(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "first", "one")
	writeBuildApplication(t, root, "second", "two")

	result, err := Orchestrator{}.BuildApp(context.Background(), BuildAppRequest{
		Name: "second",
		BuildRequest: BuildRequest{
			Path: root,
		},
	})
	if err != nil {
		t.Fatalf("BuildApp() error = %v", err)
	}

	if len(result.ApplicationInputs) != 1 {
		t.Fatalf("len(ApplicationInputs) = %d, want 1: %#v", len(result.ApplicationInputs), result.ApplicationInputs)
	}
	input := result.ApplicationInputs[0]
	if input.Application.Name != "second" {
		t.Fatalf("ApplicationInputs[0].Application.Name = %q, want second", input.Application.Name)
	}
	wantPath := filepath.ToSlash(filepath.Join("apps", "second.yaml"))
	if len(input.Paths) != 1 || input.Paths[0] != wantPath {
		t.Fatalf("ApplicationInputs[0].Paths = %#v, want [%q]", input.Paths, wantPath)
	}
	if strings.Contains(strings.Join(input.Paths, ","), "first") {
		t.Fatalf("ApplicationInputs[0].Paths = %#v, want no first app paths", input.Paths)
	}
}

func TestOrchestratorBuildAppStrictValidatesOnlySelectedApplication(t *testing.T) {
	root := t.TempDir()
	writeBuildApplicationWithProject(t, root, "selected", "selected", "platform", "https://github.com/example/allowed", "workloads")
	writeBuildApplicationWithProject(t, root, "unrelated", "unrelated", "platform", "https://github.com/example/denied", "workloads")
	writeTestFile(t, filepath.Join(root, "settings", "allowed-repo.yaml"), `apiVersion: v1
kind: Secret
metadata:
  name: allowed-repo
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  url: https://github.com/example/allowed
  project: platform
`)
	writeTestFile(t, filepath.Join(root, "projects", "platform.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: platform
spec:
  sourceRepos:
    - https://github.com/example/allowed
  destinations:
    - server: https://kubernetes.default.svc
      namespace: workloads
`)

	result, err := Orchestrator{}.BuildApp(context.Background(), BuildAppRequest{
		Name:         "selected",
		BuildRequest: BuildRequest{Path: root, Strict: true},
	})
	if err != nil {
		t.Fatalf("BuildApp() error = %v", err)
	}
	if hasDiagnosticMessage(result.Diagnostics, "unrelated") || hasDiagnosticMessage(result.Diagnostics, "denied") {
		t.Fatalf("Diagnostics = %#v, want no unrelated project violation", result.Diagnostics)
	}
}

func TestOrchestratorListApplicationsPreservesMatrixGeneratorInputPaths(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{
		filepath.Join(root, "apps", "alpha"),
		filepath.Join(root, "clusters", "dev"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
	}
	writeTestFile(t, filepath.Join(root, "appsets", "appset.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: matrix-paths
  namespace: argocd
spec:
  goTemplate: true
  generators:
    - matrix:
        generators:
          - git:
              pathParamPrefix: app
              directories:
                - path: apps/*
          - git:
              pathParamPrefix: cluster
              directories:
                - path: clusters/*
  template:
    metadata:
      name: '{{.app.path.basename}}-{{.cluster.path.basename}}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        path: '{{.app.path.path}}'
        targetRevision: main
      destination:
        name: in-cluster
        namespace: '{{.cluster.path.basename}}'
`)

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}
	if len(result.ApplicationInputs) != 1 {
		t.Fatalf("len(ApplicationInputs) = %d, want 1: %#v", len(result.ApplicationInputs), result.ApplicationInputs)
	}
	input := result.ApplicationInputs[0]
	for _, want := range []string{"appsets/appset.yaml", "apps/alpha", "clusters/dev"} {
		if !slices.Contains(input.Paths, want) {
			t.Fatalf("ApplicationInputs[0].Paths = %#v, missing %q", input.Paths, want)
		}
	}

	selected, unowned := SelectChangedApplicationInputs(result.ApplicationInputs, []string{"clusters/dev/config.yaml"})
	if len(unowned) != 0 {
		t.Fatalf("unowned = %#v, want none", unowned)
	}
	if len(selected) != 1 || selected[0].Name != "alpha-dev" {
		t.Fatalf("selected = %#v, want alpha-dev", selected)
	}
}

func TestOrchestratorBuildAppReportsMissingApplication(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "demo", "demo")

	_, err := Orchestrator{}.BuildApp(context.Background(), BuildAppRequest{
		Name:         "missing",
		BuildRequest: BuildRequest{Path: root},
	})
	if err == nil {
		t.Fatal("BuildApp() error = nil, want missing application error")
	}
	if !strings.Contains(err.Error(), `application "missing" not found`) {
		t.Fatalf("BuildApp() error = %v, want missing application message", err)
	}
}

func TestOrchestratorBuildAppRequiresName(t *testing.T) {
	_, err := Orchestrator{}.BuildApp(context.Background(), BuildAppRequest{
		Name:         " ",
		BuildRequest: BuildRequest{Path: t.TempDir()},
	})
	if err == nil {
		t.Fatal("BuildApp() error = nil, want required name error")
	}
	if !strings.Contains(err.Error(), "application name is required") {
		t.Fatalf("BuildApp() error = %v, want required name message", err)
	}
}

func TestOrchestratorBuildAppPreservesBuildOptionsForSelectedApplication(t *testing.T) {
	root := t.TempDir()
	chartDir := filepath.Join(root, "cache", "demo")
	cacheDir := filepath.Join(root, "chart-cache")
	writeAppTestValueChart(t, chartDir)
	writeBuildApplication(t, root, "plain", "plain")
	writeChartOnlyBuildApplication(t, root, "chart-only")
	acquirer := &recordingChartAcquirer{chartDir: chartDir}

	result, err := (Orchestrator{ChartAcquirer: acquirer}).BuildApp(context.Background(), BuildAppRequest{
		Name: "chart-only",
		BuildRequest: BuildRequest{
			Path:          root,
			ChartCacheDir: cacheDir,
			Offline:       true,
			RefreshCharts: true,
		},
	})
	if err != nil {
		t.Fatalf("BuildApp() error = %v", err)
	}
	if len(result.Applications) != 1 || result.Applications[0].Name != "chart-only" {
		t.Fatalf("Applications = %#v, want only chart-only", result.Applications)
	}
	if len(acquirer.requests) != 1 {
		t.Fatalf("chart acquire calls = %d, want 1", len(acquirer.requests))
	}
	if got, want := acquirer.options[0], (chart.Options{
		CacheDir: cacheDir,
		Offline:  true,
		Refresh:  true,
	}); got != want {
		t.Fatalf("chart options = %#v, want %#v", got, want)
	}
}

func TestOrchestratorBuildAppPreservesListAndRenderDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeUnsupportedApplicationSetFixture(t, root)
	writeDuplicateConfigMaps(t, filepath.Join(root, "manifests", "direct"))

	result, err := Orchestrator{}.BuildApp(context.Background(), BuildAppRequest{
		Name:         "direct",
		BuildRequest: BuildRequest{Path: root},
	})
	if err != nil {
		t.Fatalf("BuildApp() error = %v", err)
	}
	if len(result.Diagnostics) != 2 {
		t.Fatalf("len(Diagnostics) = %d, want 2: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	categories := map[string]bool{}
	for _, diag := range result.Diagnostics {
		categories[diag.Category] = true
	}
	for _, want := range []string{"appset", "repeated-resource"} {
		if !categories[want] {
			t.Fatalf("diagnostic categories = %#v, want %q", categories, want)
		}
	}
}

func TestOrchestratorBuildAppMarksSelectedApplicationSkippedWhenPreconditionFails(t *testing.T) {
	root := t.TempDir()
	writeUnsupportedApplicationSetFixture(t, root)

	result, err := Orchestrator{}.BuildApp(context.Background(), BuildAppRequest{
		Name:         "direct",
		BuildRequest: BuildRequest{Path: root, Strict: true},
	})
	if err == nil {
		t.Fatal("BuildApp() error = nil, want strict ApplicationSet precondition error")
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "direct", Status: ApplicationStatusSkipped},
	})
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

func TestOrchestratorLoadsAndMergesProviderFixtureConfig(t *testing.T) {
	root := t.TempDir()
	fixturePath := filepath.Join(root, "provider.yaml")
	writeTestFile(t, fixturePath, `
clusters:
  - name: prod
    server: https://prod.example.invalid
`)
	writeUnsupportedApplicationSetFixture(t, root)

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{
		Path:                           root,
		ApplicationSetProviderFixtures: []string{fixturePath},
		ApplicationSetProviderData: appset.ProviderData{
			Clusters: []appset.ClusterInput{{
				Name:   "prod",
				Server: "https://prod.example.invalid",
			}},
		},
	})
	if err == nil {
		t.Fatal("ListApplications() error = nil, want duplicate provider fixture identity error")
	}
	if !strings.Contains(err.Error(), "duplicate provider fixture cluster identity") {
		t.Fatalf("ListApplications() error = %v, want duplicate provider fixture identity", err)
	}
	if len(result.Diagnostics) == 0 || !strings.Contains(result.Diagnostics[0].Message, "duplicate provider fixture cluster identity") {
		t.Fatalf("Diagnostics = %#v, want provider fixture duplicate diagnostic", result.Diagnostics)
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

type blockingInternalPluginRenderer struct{}

func (blockingInternalPluginRenderer) RenderPlugin(ctx context.Context, _ render.PluginRequest) ([]render.Manifest, []diagnostic.Diagnostic, error) {
	<-ctx.Done()
	return nil, nil, ctx.Err()
}

type internalPluginRendererFunc func(context.Context, render.PluginRequest) ([]render.Manifest, []diagnostic.Diagnostic, error)

func (f internalPluginRendererFunc) RenderPlugin(ctx context.Context, request render.PluginRequest) ([]render.Manifest, []diagnostic.Diagnostic, error) {
	return f(ctx, request)
}

type failingInternalPluginRenderer struct{}

func (failingInternalPluginRenderer) RenderPlugin(_ context.Context, _ render.PluginRequest) ([]render.Manifest, []diagnostic.Diagnostic, error) {
	return nil, []diagnostic.Diagnostic{{
		Code:     "plugin.custom",
		Severity: diagnostic.SeverityError,
		Category: "plugin",
		Message:  "renderer supplied diagnostic",
	}}, errors.New("renderer failed")
}

func manifestByName(manifests []render.Manifest, name string) (render.Manifest, bool) {
	for _, manifest := range manifests {
		if manifest.Object.GetName() == name {
			return manifest, true
		}
	}
	return render.Manifest{}, false
}

func diagnosticByCategory(diags []diagnostic.Diagnostic, category string) (diagnostic.Diagnostic, bool) {
	for _, diag := range diags {
		if diag.Category == category {
			return diag, true
		}
	}
	return diagnostic.Diagnostic{}, false
}

func hasDiagnosticMessage(diags []diagnostic.Diagnostic, fragment string) bool {
	for _, diag := range diags {
		if strings.Contains(diag.Message, fragment) {
			return true
		}
	}
	return false
}

func hasDiagnosticCode(diags []diagnostic.Diagnostic, code string) bool {
	for _, diag := range diags {
		if diag.Code == code {
			return true
		}
	}
	return false
}

func hasCacheEvent(events []cacheevent.Event, source, action, targetFragment string) bool {
	for _, event := range events {
		if string(event.Source) == source && string(event.Action) == action && strings.Contains(event.Target, targetFragment) {
			return true
		}
	}
	return false
}

func assertApplicationStatuses(t *testing.T, got, want []ApplicationStatus) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len(Statuses) = %d, want %d: %#v", len(got), len(want), got)
	}
	byName := map[string]ApplicationStatus{}
	for _, status := range got {
		byName[applicationStatusDisplayName(status)] = status
	}
	for _, expected := range want {
		status, ok := byName[applicationStatusDisplayName(expected)]
		if !ok {
			t.Fatalf("Statuses = %#v, missing %s", got, applicationStatusDisplayName(expected))
		}
		if status.Status != expected.Status {
			t.Fatalf("Status for %s = %q, want %q: %#v", applicationStatusDisplayName(expected), status.Status, expected.Status, got)
		}
		if status.Status != ApplicationStatusPass && status.Message == "" {
			t.Fatalf("Status message for %s is empty, want failure/skipped message: %#v", applicationStatusDisplayName(expected), status)
		}
	}
}

func assertApplicationStatusOrder(t *testing.T, got []ApplicationStatus, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len(Statuses) = %d, want %d: %#v", len(got), len(want), got)
	}
	for i, status := range got {
		key := applicationStatusDisplayName(status) + ":" + status.Status
		if key != want[i] {
			t.Fatalf("Statuses[%d] = %q, want %q: %#v", i, key, want[i], got)
		}
	}
}

func assertManifestNames(t *testing.T, manifests []render.Manifest, want []string) {
	t.Helper()
	if len(manifests) != len(want) {
		t.Fatalf("len(Manifests) = %d, want %d: %#v", len(manifests), len(want), manifests)
	}
	for i, manifest := range manifests {
		if got := manifest.Object.GetName(); got != want[i] {
			t.Fatalf("Manifests[%d].Name = %q, want %q", i, got, want[i])
		}
	}
}

type controlledChartAcquirer struct {
	root     string
	started  chan string
	releases map[string]chan struct{}
	fail     map[string]error
	mu       sync.Mutex
	active   int
	max      int
}

func newControlledChartAcquirer(root string, names []string) *controlledChartAcquirer {
	releases := make(map[string]chan struct{}, len(names))
	for _, name := range names {
		if _, ok := releases[name]; !ok {
			releases[name] = make(chan struct{})
		}
	}
	return &controlledChartAcquirer{
		root:     root,
		started:  make(chan string, len(names)+4),
		releases: releases,
		fail:     map[string]error{},
	}
}

func (acquirer *controlledChartAcquirer) Acquire(ctx context.Context, request chart.Request, _ chart.Options) (chart.Result, error) {
	acquirer.mu.Lock()
	acquirer.active++
	if acquirer.active > acquirer.max {
		acquirer.max = acquirer.active
	}
	acquirer.mu.Unlock()
	defer func() {
		acquirer.mu.Lock()
		acquirer.active--
		acquirer.mu.Unlock()
	}()

	select {
	case acquirer.started <- request.Name:
	case <-ctx.Done():
		return chart.Result{}, ctx.Err()
	}
	release := acquirer.releases[request.Name]
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return chart.Result{}, ctx.Err()
		}
	}
	if err := acquirer.fail[request.Name]; err != nil {
		return chart.Result{}, err
	}
	return chart.Result{
		ChartDir:   filepath.Join(acquirer.root, request.Name),
		Repository: request.Repository,
		Name:       request.Name,
		Version:    request.Version,
		Kind:       request.Kind,
		FromCache:  true,
	}, nil
}

func (acquirer *controlledChartAcquirer) waitStarted(t *testing.T, want ...string) {
	t.Helper()
	remaining := map[string]int{}
	for _, name := range want {
		remaining[name]++
	}
	for _, expected := range want {
		select {
		case got := <-acquirer.started:
			if remaining[got] == 0 {
				t.Fatalf("started chart = %q, want one of %#v", got, want)
			}
			remaining[got]--
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for chart acquisition %q", expected)
		}
	}
}

func (acquirer *controlledChartAcquirer) release(name string) {
	if release := acquirer.releases[name]; release != nil {
		close(release)
	}
}

func (acquirer *controlledChartAcquirer) maxActive() int {
	acquirer.mu.Lock()
	defer acquirer.mu.Unlock()
	return acquirer.max
}

type staticGitAcquirer struct {
	path string
}

func (acquirer staticGitAcquirer) Acquire(_ context.Context, request sourcepkg.GitRequest, _ sourcepkg.GitOptions) (sourcepkg.GitResult, error) {
	return sourcepkg.GitResult{
		Path:      acquirer.path,
		Revision:  request.Revision,
		FromCache: true,
	}, nil
}

func chartOnlyApplication(appName, chartName, version string) argoappv1.Application {
	return argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: "argocd"},
		Spec: argoappv1.ApplicationSpec{
			Project:     "default",
			Source:      &argoappv1.ApplicationSource{RepoURL: "https://charts.example.com", Chart: chartName, TargetRevision: version},
			Destination: argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "default"},
		},
	}
}

func pluginApplication(name string) argoappv1.Application {
	return argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "argocd"},
		Spec: argoappv1.ApplicationSpec{
			Project: "default",
			Source: &argoappv1.ApplicationSource{
				RepoURL:        "https://github.com/example/repo",
				TargetRevision: "main",
				Path:           "manifests/" + name,
				Plugin:         &argoappv1.ApplicationSourcePlugin{Name: "cue"},
			},
			Destination: argoappv1.ApplicationDestination{Name: "in-cluster", Namespace: "default"},
		},
	}
}

func writeTestChart(t *testing.T, root, name string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, name, "Chart.yaml"), `apiVersion: v2
name: `+name+`
version: 0.1.0
`)
	writeTestFile(t, filepath.Join(root, name, "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}
data:
  value: rendered
`)
}

func writeBuildApplication(t *testing.T, root, appName, configMapName string) {
	t.Helper()
	writeBuildApplicationWithDestination(t, root, appName, configMapName, "in-cluster")
}

func writeBuildApplicationWithDestination(t *testing.T, root, appName, configMapName, destinationName string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", appName+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+appName+`
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    path: manifests/`+appName+`
    targetRevision: main
  destination:
    name: `+destinationName+`
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", appName, "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: `+configMapName+`
data:
  key: value
`)
}

func writePluginBuildApplication(t *testing.T, root, appName, pluginName string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", appName+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+appName+`
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/repo
    path: manifests/`+appName+`
    targetRevision: main
    plugin:
      name: `+pluginName+`
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", appName, ".keep"), "")
}

func writeBuildApplicationWithProject(t *testing.T, root, appName, configMapName, projectName, repoURL, destinationNamespace string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", appName+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+appName+`
  namespace: argocd
spec:
  project: `+projectName+`
  source:
    repoURL: `+repoURL+`
    path: manifests/`+appName+`
  destination:
    server: https://kubernetes.default.svc
    namespace: `+destinationNamespace+`
`)
	writeTestFile(t, filepath.Join(root, "manifests", appName, "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: `+configMapName+`
data:
  value: demo
`)
}

func writeExternalPathApplicationNamed(t *testing.T, root, appName, repoURL, sourcePath string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", appName+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+appName+`
  namespace: argocd
spec:
  source:
    repoURL: `+repoURL+`
    targetRevision: main
    path: `+sourcePath+`
  destination:
    name: in-cluster
    namespace: default
`)
}

func writeChartOnlyBuildApplication(t *testing.T, root, appName string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", appName+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+appName+`
  namespace: argocd
spec:
  source:
    repoURL: https://charts.example.test
    targetRevision: 1.2.3
    chart: demo
  destination:
    name: in-cluster
    namespace: default
`)
}

func writeExternalPathApplication(t *testing.T, root, repoURL, sourcePath string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "external.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: external
  namespace: argocd
spec:
  source:
    repoURL: `+repoURL+`
    targetRevision: main
    path: `+sourcePath+`
  destination:
    name: in-cluster
    namespace: default
`)
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
    - scmProvider: {}
  template:
    metadata:
      name: generated
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

type recordingChartAcquirer struct {
	chartDir   string
	acquireErr error
	fromCache  bool
	requests   []chart.Request
	options    []chart.Options
}

type recordingGitAcquirer struct {
	path      string
	paths     map[string]string
	revision  string
	revisions map[string]string
	err       error
	requests  []sourcepkg.GitRequest
	options   []sourcepkg.GitOptions
}

type recordingRemoteAcquirer struct {
	path     string
	err      error
	requests []remote.Request
	options  []remote.Options
}

func (acquirer *recordingGitAcquirer) Acquire(_ context.Context, request sourcepkg.GitRequest, opts sourcepkg.GitOptions) (sourcepkg.GitResult, error) {
	acquirer.requests = append(acquirer.requests, request)
	acquirer.options = append(acquirer.options, opts)
	if acquirer.err != nil {
		return sourcepkg.GitResult{}, acquirer.err
	}
	path := acquirer.path
	if acquirer.paths != nil {
		path = acquirer.paths[request.Revision]
	}
	revision := acquirer.revision
	if acquirer.revisions != nil {
		revision = acquirer.revisions[request.Revision]
	}
	return sourcepkg.GitResult{Path: path, Revision: revision}, nil
}

func (acquirer *recordingRemoteAcquirer) Acquire(_ context.Context, request remote.Request, opts remote.Options) (remote.Result, error) {
	acquirer.requests = append(acquirer.requests, request)
	acquirer.options = append(acquirer.options, opts)
	if acquirer.err != nil {
		return remote.Result{}, acquirer.err
	}
	return remote.Result{Path: acquirer.path, URL: request.URL}, nil
}

func (acquirer *recordingChartAcquirer) Acquire(_ context.Context, request chart.Request, opts chart.Options) (chart.Result, error) {
	acquirer.requests = append(acquirer.requests, request)
	acquirer.options = append(acquirer.options, opts)
	if acquirer.acquireErr != nil {
		return chart.Result{}, acquirer.acquireErr
	}
	return chart.Result{
		ChartDir:   acquirer.chartDir,
		Repository: request.Repository,
		Name:       request.Name,
		Version:    request.Version,
		Kind:       request.Kind,
		FromCache:  acquirer.fromCache,
	}, nil
}

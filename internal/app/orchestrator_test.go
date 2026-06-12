package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/acquisition"
	"github.com/sholdee/drydock/internal/cacheevent"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
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

func TestOrchestratorAppliesDiscoveredKustomizeBuildOptions(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "settings", "argocd-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  kustomize.buildOptions: --enable-helm --load-restrictor=LoadRestrictionsNone
`)
	writeTestFile(t, filepath.Join(root, "shared", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: shared
`)
	writeTestFile(t, filepath.Join(root, "apps", "demo", "app.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
spec:
  project: default
  destination:
    namespace: default
    server: https://kubernetes.default.svc
  source:
    repoURL: https://example.test/repo.git
    targetRevision: HEAD
    path: apps/demo
`)
	writeTestFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../shared/cm.yaml
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(result.Manifests) != 1 || result.Manifests[0].Object.GetName() != "shared" {
		t.Fatalf("Manifests = %#v, want shared ConfigMap", result.Manifests)
	}
}

func TestNewLocalProviderCarriesHelmValuesFileSchemes(t *testing.T) {
	settings := config.DefaultSettings()
	settings.HelmValuesFileSchemes = []config.Value[string]{
		{Value: "https"},
		{Value: "http"},
	}
	settings.HelmValuesFileSchemesSet = true

	provider, cleanup, err := newLocalProvider(context.Background(), Orchestrator{}, t.TempDir(), settings, BuildRequest{}, nil, "drydock-test-*")
	defer cleanup()
	if err != nil {
		t.Fatalf("newLocalProvider() error = %v", err)
	}
	if !provider.helmValueFileSchemesSet {
		t.Fatal("helmValueFileSchemesSet = false, want true")
	}
	if strings.Join(provider.helmValueFileSchemes, ",") != "https,http" {
		t.Fatalf("helmValueFileSchemes = %#v, want https,http", provider.helmValueFileSchemes)
	}
}

func TestNewLocalProviderPreservesGitDirInSnapshotsWhenPluginsEnabled(t *testing.T) {
	provider, cleanup, err := newLocalProvider(context.Background(), Orchestrator{}, t.TempDir(), config.DefaultSettings(), BuildRequest{
		PluginOptions: PluginOptions{EnablePlugins: true},
	}, nil, "drydock-test-*")
	defer cleanup()
	if err != nil {
		t.Fatalf("newLocalProvider() error = %v", err)
	}
	if !provider.acquisition.PreserveGitDirInSnapshots {
		t.Fatal("PreserveGitDirInSnapshots = false, want true when plugins are enabled")
	}
}

func TestNewLocalProviderReusesRequestSnapshotSession(t *testing.T) {
	session, err := acquisition.NewSnapshotSession("drydock-test-snapshots-*")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	request := BuildRequest{}
	request.snapshotSession = session

	provider, cleanup, err := newLocalProvider(context.Background(), Orchestrator{}, t.TempDir(), config.DefaultSettings(), request, cacheevent.NewRecorder(false), "unused-*")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if provider.acquisition.SnapshotRoot != session.Root {
		t.Fatalf("SnapshotRoot = %q, want shared session root %q", provider.acquisition.SnapshotRoot, session.Root)
	}
	if provider.acquisition.SnapshotCache != session.Cache {
		t.Fatal("SnapshotCache does not reuse request snapshot session cache")
	}
	cleanup()
	if _, statErr := os.Stat(session.Root); statErr != nil {
		t.Fatalf("shared session root must survive provider cleanup: %v", statErr)
	}
}

func TestOrchestratorReportsMissingSourcePathClearly(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "missing", "app.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: missing
spec:
  project: default
  destination:
    namespace: default
    server: https://kubernetes.default.svc
  source:
    targetRevision: HEAD
    path: apps/missing/source
`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err == nil {
		t.Fatal("Build() error = nil, want missing source path failure")
	}
	if len(result.Statuses) != 1 || result.Statuses[0].Status != ApplicationStatusFail {
		t.Fatalf("Statuses = %#v, want one failed status", result.Statuses)
	}
	if !strings.Contains(result.Statuses[0].Message, `source path "apps/missing/source" does not exist`) {
		t.Fatalf("status message = %q, want clear missing source path", result.Statuses[0].Message)
	}
}

func TestOrchestratorBuildStatusOnlyDoesNotCollectManifests(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "demo", "demo")

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, StatusOnly: true})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if len(result.Manifests) != 0 {
		t.Fatalf("len(Manifests) = %d, want 0", len(result.Manifests))
	}
	if len(result.ApplicationManifests) != 0 {
		t.Fatalf("len(ApplicationManifests) = %d, want 0", len(result.ApplicationManifests))
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "demo", Status: ApplicationStatusPass},
	})
}

func TestPrepareBuildResultReusesProvidedDiscovery(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "demo", "demo")

	orchestrator := Orchestrator{}
	listResult, err := orchestrator.ListApplications(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	request := BuildRequest{Path: root}
	request.Applications = listResult.Applications
	request.renderCache = listResult.renderCache
	request.renderSettingsSignature = listResult.renderSettingsSignature
	request.discovered = listResult.discovered

	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}

	result, err := orchestrator.prepareBuildResult(context.Background(), request, root)
	if err != nil {
		t.Fatalf("prepareBuildResult must reuse provided discovery without re-scanning: %v", err)
	}
	if result.renderCache != listResult.renderCache {
		t.Fatal("prepareBuildResult must reuse the provided render cache")
	}
	if len(result.Projects) != len(listResult.Projects) {
		t.Fatalf("Projects = %d, want %d", len(result.Projects), len(listResult.Projects))
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

func TestOrchestratorLoadsLaterRepositorySecretDocuments(t *testing.T) {
	root := t.TempDir()
	chartDir := filepath.Join(root, "cache", "chart")
	writeAppTestValueChart(t, chartDir)
	writeTestFile(t, filepath.Join(root, "apps", "chart.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: chart
  namespace: argocd
spec:
  project: k3s
  source:
    repoURL: ghcr.io/example/charts
    targetRevision: 0.1.0
    chart: chart
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "projects", "k3s.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: k3s
spec:
  sourceRepos:
    - ghcr.io/example/charts
  destinations:
    - name: "*"
      namespace: "*"
`)
	writeTestFile(t, filepath.Join(root, "settings", "repos.yaml"), `apiVersion: v1
kind: Secret
metadata:
  name: git
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  name: platform-repo
  type: git
  url: https://github.com/example/platform-repo
---
apiVersion: v1
kind: Secret
metadata:
  name: charts
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  name: charts
  type: helm
  url: ghcr.io/example/charts
  enableOCI: "true"
`)

	acquirer := &recordingChartAcquirer{chartDir: chartDir}
	result, err := (Orchestrator{ChartAcquirer: acquirer}).Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(acquirer.requests) != 1 {
		t.Fatalf("chart acquire calls = %d, want 1", len(acquirer.requests))
	}
	if got := acquirer.requests[0].Kind; got != chart.RepositoryOCI {
		t.Fatalf("chart request kind = %q, want %q", got, chart.RepositoryOCI)
	}
	if got := acquirer.options[0].Offline; got {
		t.Fatalf("chart acquire Offline = %t, want false without explicit offline flag", got)
	}
	for _, diag := range result.Diagnostics {
		if diag.Category == "repository" && strings.Contains(diag.Message, "missing repository metadata") {
			t.Fatalf("Diagnostics = %#v, want later repository Secret document to satisfy metadata", result.Diagnostics)
		}
	}
}

func TestOrchestratorDedupesRepeatedRepositorySecretCandidates(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "settings", "repos.yaml"), `apiVersion: v1
kind: Secret
metadata:
  name: git
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  url: https://github.com/example/platform-repo
---
apiVersion: v1
kind: Secret
metadata:
  name: charts
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  url: ghcr.io/example/charts
  enableOCI: typo
`)

	result, err := Orchestrator{}.Diag(context.Background(), DiagRequest{Path: root})
	if err == nil {
		t.Fatalf("Diag() error = nil, want invalid settings diagnostic error")
	}
	count := 0
	for _, diag := range result.Diagnostics {
		if diag.Message == "invalid repository Secret enableOCI value" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("invalid enableOCI diagnostics = %d, want 1: %#v", count, result.Diagnostics)
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
		Path: root,
		FilterOptions: FilterOptions{
			SkipSecrets: true,
		},
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
	if hasDiagnosticMessage(result.Diagnostics, "health Lua is parsed as metadata only") {
		t.Fatalf("Diagnostics = %#v, want no health Lua metadata-only warning", result.Diagnostics)
	}
	if hasDiagnosticMessage(result.Diagnostics, "useOpenLibs is parsed as metadata only") {
		t.Fatalf("Diagnostics = %#v, want no useOpenLibs metadata-only warning", result.Diagnostics)
	}
	if !hasDiagnosticMessage(result.Diagnostics, "actions are parsed as metadata only") {
		t.Fatalf("Diagnostics = %#v, want actions warning", result.Diagnostics)
	}
}

func TestOrchestratorBuildValidatesLuaHealthWhenEnabled(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "demo", "demo")
	writeHealthCustomizationSettings(t, root, "ConfigMap", `error("boom")`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, ValidateLuaHealth: true})
	assertBuildErrorContains(t, err, "1 Application failed", "argocd/demo", "diagnostic health")
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "demo", Status: ApplicationStatusFail},
	})
	diag := assertDiagnosticCategory(t, result.Diagnostics, "health")
	if diag.Code != "health.lua-failed" {
		t.Fatalf("health diagnostic code = %q, want health.lua-failed", diag.Code)
	}
	if len(result.Manifests) != 0 {
		t.Fatalf("len(Manifests) = %d, want no manifests retained for failed application", len(result.Manifests))
	}
}

func TestOrchestratorBuildLuaHealthValidStateDoesNotFail(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "demo", "demo")
	writeHealthCustomizationSettings(t, root, "ConfigMap", `return { status = "Healthy" }`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, ValidateLuaHealth: true})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "demo", Status: ApplicationStatusPass},
	})
	if _, ok := diagnosticByCategory(result.Diagnostics, "health"); ok {
		t.Fatalf("Diagnostics = %#v, want no health diagnostics", result.Diagnostics)
	}
}

func TestOrchestratorBuildDoesNotValidateLuaHealthUnlessEnabled(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "demo", "demo")
	writeHealthCustomizationSettings(t, root, "ConfigMap", `error("boom")`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "demo", Status: ApplicationStatusPass},
	})
	if _, ok := diagnosticByCategory(result.Diagnostics, "health"); ok {
		t.Fatalf("Diagnostics = %#v, want no health diagnostics without opt-in", result.Diagnostics)
	}
}

func TestOrchestratorBuildFiltersResourcesBeforeLuaHealthValidation(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "demo", "demo")
	writeTestFile(t, filepath.Join(root, "manifests", "demo", "secret.yaml"), `apiVersion: v1
kind: Secret
metadata:
  name: demo
stringData:
  password: secret
`)
	writeHealthCustomizationSettings(t, root, "Secret", `error("boom")`)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{
		Path:              root,
		StatusOnly:        true,
		ValidateLuaHealth: true,
		FilterOptions: FilterOptions{
			SkipSecrets: true,
		},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(result.Manifests) != 0 {
		t.Fatalf("len(Manifests) = %d, want 0 for status-only build", len(result.Manifests))
	}
	if len(result.ApplicationManifests) != 0 {
		t.Fatalf("len(ApplicationManifests) = %d, want 0 for status-only build", len(result.ApplicationManifests))
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "demo", Status: ApplicationStatusPass},
	})
	if _, ok := diagnosticByCategory(result.Diagnostics, "health"); ok {
		t.Fatalf("Diagnostics = %#v, want filtered Secret to skip health validation", result.Diagnostics)
	}
}

func TestOrchestratorBuildFailsOwningApplicationOnLuaHealthCompileDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "demo", "demo")
	writeHealthCustomizationSettings(t, root, "ConfigMap", `return { status = "Healthy" `)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, ValidateLuaHealth: true})
	assertBuildErrorContains(t, err, "1 Application failed", "argocd/demo", "diagnostic health", "failed to compile health Lua")
	if len(result.Manifests) != 0 {
		t.Fatalf("len(Manifests) = %d, want no manifests retained for failed application", len(result.Manifests))
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "demo", Status: ApplicationStatusFail},
	})
	diag := assertDiagnosticCategory(t, result.Diagnostics, "health")
	if diag.Code != "health.lua-compile-failed" {
		t.Fatalf("health diagnostic code = %q, want health.lua-compile-failed", diag.Code)
	}
}

func TestOrchestratorBuildIgnoresUnmatchedLuaHealthCompileDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeBuildApplication(t, root, "demo", "demo")
	writeHealthCustomizationSettings(t, root, "Secret", `return { status = "Healthy" `)

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, ValidateLuaHealth: true})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "demo", Status: ApplicationStatusPass},
	})
	if _, ok := diagnosticByCategory(result.Diagnostics, "health"); ok {
		t.Fatalf("Diagnostics = %#v, want unmatched invalid customization to skip health validation", result.Diagnostics)
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
	fixture := newAppFixture(t)
	writeTestFile(t, filepath.Join(fixture.Root, "apps", "argocd", "plain-app.yaml"), `apiVersion: argoproj.io/v1alpha1
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
	writeTestFile(t, filepath.Join(fixture.Root, "manifests", "plain", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: plain
data:
  source: directory
`)

	result := fixture.build(t, BuildRequest{})
	if len(result.Applications) != 1 {
		t.Fatalf("len(Applications) = %d, want 1", len(result.Applications))
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	manifest := assertManifestNamed(t, result.Manifests, "plain")
	if manifest.Object.GetKind() != "ConfigMap" {
		t.Fatalf("rendered kind = %q, want ConfigMap", manifest.Object.GetKind())
	}
	value, found, err := unstructured.NestedString(manifest.Object.Object, "data", "source")
	if err != nil || !found || value != "directory" {
		t.Fatalf("data.source = %q, found %v, err %v; want directory", value, found, err)
	}
}

func TestBuildSessionPreservesValidationBeforeRendering(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "app.yaml"), `
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://example.test/repo.git
    targetRevision: main
    path: missing
  destination:
    namespace: demo
    server: https://kubernetes.default.svc
`)

	_, err := (Orchestrator{}).Build(context.Background(), BuildRequest{
		Path: root,
		AcquisitionOptions: AcquisitionOptions{
			Offline:     true,
			GitCacheDir: filepath.Join(root, ".drydock", "git"),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "git cache dir") {
		t.Fatalf("Build error = %v, want git cache validation error", err)
	}
}

func TestOrchestratorBuildPreservesPartialResults(t *testing.T) {
	fixture := newAppFixture(t)
	cacheDir := t.TempDir()
	fixture.writeBuildApplication(t, "valid", "valid")
	fixture.writeExternalPathApplicationNamed(t, "invalid", "https://github.com/example/missing", "manifests/missing")

	result, err := fixture.buildAllowError(t, Orchestrator{}, BuildRequest{
		AcquisitionOptions: AcquisitionOptions{
			GitCacheDir: cacheDir,
			Offline:     true,
		},
	})
	assertBuildErrorContains(t, err, "1 Application failed", "argocd/invalid")
	if len(result.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(result.Manifests))
	}
	assertManifestNamed(t, result.Manifests, "valid")
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "valid", Status: ApplicationStatusPass},
		{Namespace: "argocd", Name: "invalid", Status: ApplicationStatusFail},
	})
	diag := assertDiagnosticCategory(t, result.Diagnostics, "render")
	if diag.Severity != diagnostic.SeverityError {
		t.Fatalf("render diagnostic severity = %s, want error", diag.Severity)
	}
	if !strings.Contains(diag.Message, "invalid") || !strings.Contains(diag.Message, "offline cache miss") {
		t.Fatalf("render diagnostic message = %q, want app context and render error", diag.Message)
	}
}

func TestOrchestratorGeneratesApplicationSetFromLaterDocument(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "pineapp", "demo", "manifest.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: direct-demo
  namespace: argocd
spec:
  project: homelab
  source:
    repoURL: https://github.com/example/repo.git
    path: pineapp/demo
    targetRevision: HEAD
    directory:
      include: "{manifest.yaml}"
  destination:
    server: https://kubernetes.default.svc
    namespace: demo
`)
	writeTestFile(t, filepath.Join(root, "pineapp", "argocd", "manifest.yaml"), `apiVersion: v1
kind: Namespace
metadata:
  name: argocd
---
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: pineapp-homelab
  namespace: argocd
spec:
  goTemplate: true
  generators:
    - git:
        repoURL: https://github.com/example/repo.git
        revision: HEAD
        files:
          - path: pineapp/demo/manifest.yaml
  template:
    metadata:
      name: '{{.path.basename}}.manifest'
      namespace: argocd
    spec:
      project: homelab
      source:
        repoURL: https://github.com/example/repo.git
        path: '{{.path.path}}'
        targetRevision: HEAD
        directory:
          include: "{manifest.yaml}"
      destination:
        server: https://kubernetes.default.svc
        namespace: '{{.path.basename}}'
`)

	result, err := Orchestrator{}.ListApplications(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("ListApplications() error = %v", err)
	}
	for _, diag := range result.Diagnostics {
		if diag.Category == "appset" && strings.Contains(diag.Message, "unsupported ApplicationSet generator") {
			t.Fatalf("Diagnostics = %#v, want no unsupported ApplicationSet generator warning", result.Diagnostics)
		}
	}
	for _, app := range result.Applications {
		if app.Name == "demo.manifest" {
			return
		}
	}
	t.Fatalf("Applications = %#v, want generated demo.manifest", result.Applications)
}

func TestOrchestratorBuildStatusesIncludePassAndFailForPlanningErrors(t *testing.T) {
	fixture := newAppFixture(t)
	fixture.writeBuildApplication(t, "valid", "valid")
	writeTestFile(t, filepath.Join(fixture.Root, "apps", "bad-ref.yaml"), `apiVersion: argoproj.io/v1alpha1
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

	result, err := fixture.buildAllowError(t, Orchestrator{}, BuildRequest{})
	assertBuildErrorContains(t, err, "1 Application failed", "bad/ref")
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "valid", Status: ApplicationStatusPass},
		{Namespace: "argocd", Name: "bad-ref", Status: ApplicationStatusFail},
	})
}

func TestOrchestratorBuildMarksApplicationsSkippedWhenPreconditionFails(t *testing.T) {
	fixture := newAppFixture(t)
	fixture.writeUnsupportedApplicationSet(t)

	result, err := fixture.buildAllowError(t, Orchestrator{}, BuildRequest{Strict: true})
	assertBuildErrorContains(t, err, "unsupported ApplicationSet generator")
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

func TestOrchestratorBuildStatusOnlyStrictFailsOnRenderDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "direct.yaml"), directApplicationYAML())
	writeDuplicateConfigMaps(t, filepath.Join(root, "manifests", "direct"))

	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root, Strict: true, StatusOnly: true})
	if err == nil {
		t.Fatalf("Build() error = nil, want strict diagnostic error")
	}
	if len(result.Manifests) != 0 {
		t.Fatalf("len(Manifests) = %d, want 0", len(result.Manifests))
	}
	if len(result.ApplicationManifests) != 0 {
		t.Fatalf("len(ApplicationManifests) = %d, want 0", len(result.ApplicationManifests))
	}
	if len(result.Diagnostics) != 1 {
		t.Fatalf("len(Diagnostics) = %d, want 1: %#v", len(result.Diagnostics), result.Diagnostics)
	}
	if result.Diagnostics[0].Category != "repeated-resource" {
		t.Fatalf("diagnostic category = %q, want repeated-resource", result.Diagnostics[0].Category)
	}
	if !strings.Contains(err.Error(), "repeated-resource") {
		t.Fatalf("Build() error = %q, want repeated-resource", err.Error())
	}
	assertApplicationStatuses(t, result.Statuses, []ApplicationStatus{
		{Namespace: "argocd", Name: "direct", Status: ApplicationStatusFail},
	})
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

func writeHealthCustomizationSettings(t *testing.T, root, key, lua string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "settings", "argocd-cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
data:
  resource.customizations.health.`+key+`: |
`+indentLua(lua))
}

func indentLua(lua string) string {
	var out strings.Builder
	for line := range strings.SplitSeq(lua, "\n") {
		out.WriteString("    ")
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.String()
}

func TestApplicationInputPathsByKeyDuplicateAppsAreCacheIneligible(t *testing.T) {
	app := func(name string) argoappv1.Application {
		return argoappv1.Application{ObjectMeta: metav1.ObjectMeta{Namespace: "argocd", Name: name}}
	}
	inputs := []ApplicationSelectionInput{
		{Application: app("web"), Paths: []string{"apps/web/app.yaml"}},
		{Application: app("api"), Paths: []string{"apps/api/app.yaml"}},
		{Application: app("web"), Paths: []string{"overlays/prod/web.yaml"}},
	}

	byKey := applicationInputPathsByKey(inputs)

	if got := byKey[applicationKey(app("api"))]; !reflect.DeepEqual(got, []string{"apps/api/app.yaml"}) {
		t.Fatalf("unique app paths = %#v, want preserved", got)
	}
	if got := byKey[applicationKey(app("web"))]; got != nil {
		t.Fatalf("duplicate app paths = %#v, want nil (cache-ineligible)", got)
	}
	if got := applicationInputPathsForRender(byKey, app("web")); got != nil {
		t.Fatalf("applicationInputPathsForRender for duplicate = %#v, want nil", got)
	}
}

func TestApplicationInputsByKeyDuplicateAppsAreCacheIneligible(t *testing.T) {
	app := func(name string) argoappv1.Application {
		return argoappv1.Application{ObjectMeta: metav1.ObjectMeta{Namespace: "argocd", Name: name}}
	}
	appFile := func(application argoappv1.Application, paths []string) discovery.ApplicationFile {
		return discovery.ApplicationFile{
			Path:          "test.yaml",
			DocumentIndex: 0,
			Application:   application,
			InputPaths:    paths,
		}
	}
	discovered := discovery.Result{
		Applications: []discovery.ApplicationFile{
			appFile(app("web"), []string{"apps/web/app.yaml"}),
			appFile(app("api"), []string{"apps/api/app.yaml"}),
			appFile(app("web"), []string{"overlays/prod/web.yaml"}),
		},
	}

	byKey := applicationInputsByKey(discovered)

	if got := byKey[applicationDiscoveryKey(app("api"))]; !reflect.DeepEqual(got, []string{"apps/api/app.yaml"}) {
		t.Fatalf("unique app paths = %#v, want preserved", got)
	}
	if got := byKey[applicationDiscoveryKey(app("web"))]; got != nil {
		t.Fatalf("duplicate app paths = %#v, want nil (cache-ineligible)", got)
	}
}

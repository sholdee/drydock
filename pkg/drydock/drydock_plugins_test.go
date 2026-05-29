package drydock

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	renderpkg "github.com/sholdee/drydock/internal/render"
)

func TestRenderUsesInjectedPluginRenderer(t *testing.T) {
	root := t.TempDir()
	writeAPIPluginAppTree(t, root)

	renderer := publicPluginRendererFunc(func(_ context.Context, request PluginRequest) (PluginResult, error) {
		assertPublicPluginRequest(t, request)
		return publicPluginConfigMapResult(), nil
	})

	result, err := Render(context.Background(), Config{Path: root, PluginRenderer: renderer})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	assertRenderedPluginConfigMap(t, result)
}

func TestRenderUsesNamedPluginRegistry(t *testing.T) {
	root := t.TempDir()
	writeAPIPluginAppTree(t, root)

	renderer := publicPluginRendererFunc(func(_ context.Context, request PluginRequest) (PluginResult, error) {
		assertPublicPluginRequest(t, request)
		return publicPluginConfigMapResult(), nil
	})
	registry := NewPluginRegistry(map[string]PluginRenderer{" cue ": renderer})

	result, err := Render(context.Background(), Config{Path: root, PluginRenderer: registry})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	assertRenderedPluginConfigMap(t, result)
}

func TestInjectedPublicPluginRendererReceivesRefsAndCapabilities(t *testing.T) {
	called := false
	renderer := publicPluginRendererFunc(func(_ context.Context, request PluginRequest) (PluginResult, error) {
		called = true
		if request.RefRoots["$values"] != "/repo/values" {
			t.Fatalf("RefRoots[$values] = %q, want /repo/values", request.RefRoots["$values"])
		}
		refSource := request.RefSources["$values"]
		if refSource.RepoRoot != "/repo" || refSource.Path != "values" || refSource.RepoURL != "https://github.com/example/values" || refSource.TargetRevision != "main" {
			t.Fatalf("RefSources[$values] = %#v, want public ref source metadata", refSource)
		}
		if request.KubeVersion != "1.30.1" {
			t.Fatalf("KubeVersion = %q, want 1.30.1", request.KubeVersion)
		}
		if len(request.APIVersions) != 1 || request.APIVersions[0] != "example.com/v1/Foo" {
			t.Fatalf("APIVersions = %#v, want example.com/v1/Foo", request.APIVersions)
		}
		return PluginResult{}, nil
	})
	adapter := pluginRendererAdapter{renderer: renderer}

	_, _, err := adapter.RenderPlugin(context.Background(), renderpkg.PluginRequest{
		Source: renderpkg.ResolvedSource{
			RepoRoot:       "/repo",
			Path:           "apps/plugin",
			RepoURL:        "https://github.com/example/repo",
			TargetRevision: "main",
		},
		Plugin:      renderpkg.PluginConfig{Name: "cue"},
		RefRoots:    map[string]string{"$values": "/repo/values"},
		RefSources:  map[string]renderpkg.ResolvedSource{"$values": {RepoRoot: "/repo", Path: "values", RepoURL: "https://github.com/example/values", TargetRevision: "main"}},
		KubeVersion: "1.30.1",
		APIVersions: []string{"example.com/v1/Foo"},
	})
	if err != nil {
		t.Fatalf("RenderPlugin() error = %v", err)
	}
	if !called {
		t.Fatal("public plugin renderer was not called")
	}
}

func TestRenderNamedPluginRegistryReportsMissingRenderer(t *testing.T) {
	root := t.TempDir()
	writeAPIPluginAppTree(t, root)

	result, err := Render(context.Background(), Config{
		Path:           root,
		PluginRenderer: NewPluginRegistry(map[string]PluginRenderer{"jsonnet": nil}),
	})
	if err == nil {
		t.Fatal("Render() error = nil, want missing plugin renderer error")
	}
	if len(result.Manifests) != 0 {
		t.Fatalf("Manifests = %d, want no fallback manifests", len(result.Manifests))
	}
	if !hasDiagnosticCode(result.Diagnostics, "plugin.unsupported") {
		t.Fatalf("Diagnostics = %#v, want plugin.unsupported", result.Diagnostics)
	}
	if hasDiagnosticCode(result.Diagnostics, "plugin.failed") {
		t.Fatalf("Diagnostics = %#v, did not want plugin.failed for registry miss", result.Diagnostics)
	}
	if !hasStatus(result.Statuses, "plugin-app", "FAIL") {
		t.Fatalf("Statuses = %#v, want plugin-app FAIL", result.Statuses)
	}
}

func TestRenderPluginRendererHonorsConfiguredTimeout(t *testing.T) {
	root := t.TempDir()
	writeAPIAppTree(t, root, "ok", configMapBody("ok", "v1"))
	writeAPIPluginAppTree(t, root)

	result, err := Render(context.Background(), Config{
		Path:           root,
		PluginRenderer: blockingPublicPluginRenderer{},
		PluginTimeout:  time.Nanosecond,
	})
	if err == nil {
		t.Fatal("Render() error = nil, want plugin timeout")
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("Manifests = %d, want successful non-plugin manifest", len(result.Manifests))
	}
	if !hasStatus(result.Statuses, "ok", "PASS") || !hasStatus(result.Statuses, "plugin-app", "FAIL") {
		t.Fatalf("Statuses = %#v, want ok PASS and plugin-app FAIL", result.Statuses)
	}
	if !hasDiagnosticCode(result.Diagnostics, "plugin.failed") {
		t.Fatalf("Diagnostics = %#v, want plugin.failed", result.Diagnostics)
	}
}

func TestDiffApplicationsPluginRendererHonorsConfiguredTimeout(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()
	writeAPIPluginAppTree(t, left)
	writeAPIPluginAppTree(t, right)

	result, err := DiffApplications(context.Background(), Config{
		PathOrig:       left,
		Path:           right,
		ChangedOnly:    boolPtr(false),
		PluginRenderer: blockingPublicPluginRenderer{},
		PluginTimeout:  time.Nanosecond,
	})
	if err == nil {
		t.Fatal("DiffApplications() error = nil, want plugin timeout")
	}
	if !hasDiagnosticCode(result.Diagnostics, "plugin.failed") {
		t.Fatalf("Diagnostics = %#v, want plugin.failed", result.Diagnostics)
	}
}

func TestDiffImagesPluginRendererHonorsConfiguredTimeout(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()
	writeAPIPluginAppTree(t, left)
	writeAPIPluginAppTree(t, right)

	result, err := DiffImages(context.Background(), Config{
		PathOrig:       left,
		Path:           right,
		ChangedOnly:    boolPtr(false),
		PluginRenderer: blockingPublicPluginRenderer{},
		PluginTimeout:  time.Nanosecond,
	})
	if err == nil {
		t.Fatal("DiffImages() error = nil, want plugin timeout")
	}
	if !hasDiagnosticCode(result.Diagnostics, "plugin.failed") {
		t.Fatalf("Diagnostics = %#v, want plugin.failed", result.Diagnostics)
	}
}

func TestDiffApplicationsReturnsResultsFromSuccessfulAppsWithPluginFailure(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()
	writeAPIAppTree(t, left, "demo", configMapBody("demo", "old"))
	writeAPIAppTree(t, right, "demo", configMapBody("demo", "new"))
	writeAPIPluginAppTree(t, left)
	writeAPIPluginAppTree(t, right)

	result, err := DiffApplications(context.Background(), Config{
		PathOrig:       left,
		Path:           right,
		ChangedOnly:    boolPtr(false),
		PluginRenderer: failingPublicPluginRenderer{},
	})
	if err == nil {
		t.Fatal("DiffApplications() error = nil, want partial plugin render error")
	}
	if len(result.Results) != 1 {
		t.Fatalf("Results = %d, want successful app diff despite plugin error: %#v", len(result.Results), result.Results)
	}
	if result.Results[0].Parent.Name != "demo" || result.Results[0].Change != "modified" {
		t.Fatalf("Results[0] = %#v, want modified demo diff", result.Results[0])
	}
	if !hasDiagnosticCode(result.Diagnostics, "plugin.failed") {
		t.Fatalf("Diagnostics = %#v, want plugin.failed", result.Diagnostics)
	}
}

func TestDiffImagesReturnsResultsFromSuccessfulAppsWithPluginFailure(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()
	writeAPIAppTree(t, left, "demo", deploymentBody("demo", "repo/demo:v1"))
	writeAPIAppTree(t, right, "demo", deploymentBody("demo", "repo/demo:v2"))
	writeAPIPluginAppTree(t, left)
	writeAPIPluginAppTree(t, right)

	result, err := DiffImages(context.Background(), Config{
		PathOrig:       left,
		Path:           right,
		ChangedOnly:    boolPtr(false),
		PluginRenderer: failingPublicPluginRenderer{},
	})
	if err == nil {
		t.Fatal("DiffImages() error = nil, want partial plugin render error")
	}
	if !containsString(result.Removed, "repo/demo:v1") {
		t.Fatalf("Removed = %#v, want repo/demo:v1 despite plugin error", result.Removed)
	}
	if !containsString(result.Added, "repo/demo:v2") {
		t.Fatalf("Added = %#v, want repo/demo:v2 despite plugin error", result.Added)
	}
	if !hasDiagnosticCode(result.Diagnostics, "plugin.failed") {
		t.Fatalf("Diagnostics = %#v, want plugin.failed", result.Diagnostics)
	}
}

func TestRenderPluginRendererPreservesCallerCancellation(t *testing.T) {
	root := t.TempDir()
	writeAPIPluginAppTree(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := Render(ctx, Config{
		Path:           root,
		PluginRenderer: blockingPublicPluginRenderer{},
		PluginTimeout:  time.Hour,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Render() error = %v, want context.Canceled", err)
	}
	if hasDiagnosticCode(result.Diagnostics, "plugin.failed") {
		t.Fatalf("Diagnostics = %#v, did not want plugin.failed for caller cancellation", result.Diagnostics)
	}
}

func TestInjectedPluginRendererDiagnosticsKeepStableCodes(t *testing.T) {
	root := t.TempDir()
	writeAPIPluginAppTree(t, root)

	renderer := publicPluginRendererFunc(func(_ context.Context, _ PluginRequest) (PluginResult, error) {
		return PluginResult{
			Manifests: publicPluginConfigMapResult().Manifests,
			Diagnostics: []Diagnostic{{
				Severity: "warning",
				Category: "plugin",
				Message:  "plugin emitted a warning",
			}},
		}, nil
	})

	result, err := Render(context.Background(), Config{Path: root, PluginRenderer: renderer})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !hasDiagnosticCode(result.Diagnostics, "plugin.unspecified") {
		t.Fatalf("Diagnostics = %#v, want stable neutral plugin diagnostic code", result.Diagnostics)
	}
}

func TestInjectedPluginRendererErrorPreservesDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeAPIAppTree(t, root, "ok", configMapBody("ok", "v1"))
	writeAPIPluginAppTree(t, root)

	renderer := publicPluginRendererFunc(func(_ context.Context, _ PluginRequest) (PluginResult, error) {
		return PluginResult{Diagnostics: []Diagnostic{{
			Code:     "plugin.custom",
			Severity: "error",
			Category: "plugin",
			Message:  "renderer supplied diagnostic",
		}}}, errors.New("renderer failed")
	})

	result, err := Render(context.Background(), Config{Path: root, PluginRenderer: renderer})
	if err == nil {
		t.Fatal("Render() error = nil, want plugin renderer error")
	}
	if len(result.Manifests) != 1 {
		t.Fatalf("Manifests = %d, want successful non-plugin manifest", len(result.Manifests))
	}
	if !hasStatus(result.Statuses, "ok", "PASS") || !hasStatus(result.Statuses, "plugin-app", "FAIL") {
		t.Fatalf("Statuses = %#v, want ok PASS and plugin-app FAIL", result.Statuses)
	}
	if !hasDiagnosticCode(result.Diagnostics, "plugin.custom") {
		t.Fatalf("Diagnostics = %#v, want renderer diagnostic", result.Diagnostics)
	}
	if !hasDiagnosticCode(result.Diagnostics, "plugin.failed") {
		t.Fatalf("Diagnostics = %#v, want plugin.failed", result.Diagnostics)
	}
	for _, diag := range result.Diagnostics {
		if strings.Contains(diag.Message, "FEATURE=enabled") {
			t.Fatalf("Diagnostics = %#v, leaked plugin env value", result.Diagnostics)
		}
	}
}

func TestDiffApplicationsUsesInjectedPluginRenderer(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()
	writeAPIPluginAppTree(t, left)
	writeAPIPluginAppTree(t, right)

	renderCount := 0
	renderer := publicPluginRendererFunc(func(_ context.Context, request PluginRequest) (PluginResult, error) {
		renderCount++
		value := "left"
		if renderCount == 2 {
			value = "right"
		}
		return PluginResult{Manifests: []PluginManifest{{
			Path: "plugin/cm.yaml",
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      "from-plugin",
					"namespace": "rendered",
				},
				"data": map[string]any{"value": value},
			},
		}}}, nil
	})

	result, err := DiffApplications(context.Background(), Config{
		PathOrig:       left,
		Path:           right,
		ChangedOnly:    boolPtr(false),
		PluginRenderer: renderer,
	})
	if err != nil {
		t.Fatalf("DiffApplications() error = %v", err)
	}
	if renderCount != 2 {
		t.Fatalf("plugin render calls = %d, want 2", renderCount)
	}
	if len(result.Results) != 1 {
		t.Fatalf("Results = %d, want 1", len(result.Results))
	}
	if result.Results[0].Change != "modified" {
		t.Fatalf("Change = %q, want modified", result.Results[0].Change)
	}
}

func TestDiffImagesUsesInjectedPluginRenderer(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()
	writeAPIPluginAppTree(t, left)
	writeAPIPluginAppTree(t, right)

	renderCount := 0
	renderer := publicPluginRendererFunc(func(_ context.Context, request PluginRequest) (PluginResult, error) {
		renderCount++
		image := "repo/demo:v1"
		if renderCount == 2 {
			image = "repo/demo:v2"
		}
		return PluginResult{Manifests: []PluginManifest{{
			Path:   "plugin/deployment.yaml",
			Object: deploymentObject("from-plugin", image),
		}}}, nil
	})

	result, err := DiffImages(context.Background(), Config{
		PathOrig:       left,
		Path:           right,
		ChangedOnly:    boolPtr(false),
		PluginRenderer: renderer,
	})
	if err != nil {
		t.Fatalf("DiffImages() error = %v", err)
	}
	if renderCount != 2 {
		t.Fatalf("plugin render calls = %d, want 2", renderCount)
	}
	if !containsString(result.Removed, "repo/demo:v1") {
		t.Fatalf("Removed = %#v, want repo/demo:v1", result.Removed)
	}
	if !containsString(result.Added, "repo/demo:v2") {
		t.Fatalf("Added = %#v, want repo/demo:v2", result.Added)
	}
}

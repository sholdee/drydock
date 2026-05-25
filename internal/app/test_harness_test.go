package app

import (
	"context"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/render"
)

type appFixture struct {
	Root string
}

func newAppFixture(t *testing.T) appFixture {
	t.Helper()
	return appFixture{Root: t.TempDir()}
}

func (fixture appFixture) writeBuildApplication(t *testing.T, appName, configMapName string) {
	t.Helper()
	writeBuildApplication(t, fixture.Root, appName, configMapName)
}

func (fixture appFixture) writeExternalPathApplicationNamed(t *testing.T, appName, repoURL, sourcePath string) {
	t.Helper()
	writeExternalPathApplicationNamed(t, fixture.Root, appName, repoURL, sourcePath)
}

func (fixture appFixture) writeUnsupportedApplicationSet(t *testing.T) {
	t.Helper()
	writeUnsupportedApplicationSetFixture(t, fixture.Root)
}

func (fixture appFixture) build(t *testing.T, request BuildRequest) BuildResult {
	t.Helper()
	result, err := fixture.buildAllowError(t, Orchestrator{}, request)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	return result
}

func (fixture appFixture) buildAllowError(t *testing.T, orchestrator Orchestrator, request BuildRequest) (BuildResult, error) {
	t.Helper()
	if request.Path == "" {
		request.Path = fixture.Root
	}
	return orchestrator.Build(context.Background(), request)
}

func assertBuildErrorContains(t *testing.T, err error, wants ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("Build() error = nil, want error")
	}
	for _, want := range wants {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Build() error = %q, want %q", err.Error(), want)
		}
	}
}

func assertDiagnosticCategory(t *testing.T, diagnostics []diagnostic.Diagnostic, category string) diagnostic.Diagnostic {
	t.Helper()
	diag, ok := diagnosticByCategory(diagnostics, category)
	if !ok {
		t.Fatalf("Diagnostics = %#v, want category %q", diagnostics, category)
	}
	return diag
}

func assertManifestNamed(t *testing.T, manifests []render.Manifest, name string) render.Manifest {
	t.Helper()
	manifest, ok := manifestByName(manifests, name)
	if !ok {
		t.Fatalf("Manifests = %#v, want manifest named %q", manifests, name)
	}
	return manifest
}

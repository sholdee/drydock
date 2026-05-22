package config

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadHelmValuesSettings(t *testing.T) {
	settings, diags, err := LoadFromHelmValues(filepath.Join("..", "..", "testdata", "settings", "argocd-values.yaml"))
	if err != nil {
		t.Fatalf("LoadFromHelmValues() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := settings.KustomizeBuildOptions; len(got) != 3 || got[0].Value != "--enable-helm" {
		t.Fatalf("KustomizeBuildOptions = %#v", got)
	}
	if settings.InstanceLabelKey.Value != "argocd.argoproj.io/instance" {
		t.Fatalf("InstanceLabelKey = %#v", settings.InstanceLabelKey)
	}
	if settings.InstanceLabelKey.Provenance.Path == "" {
		t.Fatalf("expected provenance")
	}
}

func TestLoadConfigMapSettings(t *testing.T) {
	settings, diags, err := LoadFromConfigMap(filepath.Join("..", "..", "testdata", "settings", "argocd-cm.yaml"))
	if err != nil {
		t.Fatalf("LoadFromConfigMap() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if got := settings.KustomizeBuildOptions; len(got) != 1 || got[0].Value != "--enable-helm" {
		t.Fatalf("KustomizeBuildOptions = %#v", got)
	}
	if settings.InstanceLabelKey.Value != "app.kubernetes.io/instance" {
		t.Fatalf("InstanceLabelKey = %#v", settings.InstanceLabelKey)
	}
}

func TestLoadRepositorySecretIgnoresCredentials(t *testing.T) {
	settings, diags, err := LoadRepositorySecret(filepath.Join("..", "..", "testdata", "settings", "repo-secret.yaml"))
	if err != nil {
		t.Fatalf("LoadRepositorySecret() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	repo := settings.HelmRepositories["ghcr.io/example/charts"]
	if !repo.EnableOCI {
		t.Fatalf("EnableOCI = false, want true")
	}
	if repo.Name != "charts" || repo.Type != "helm" {
		t.Fatalf("repo = %#v", repo)
	}
}

func TestRepositorySecretDoesNotRetainRawSensitiveData(t *testing.T) {
	settings, diags, err := LoadRepositorySecret(filepath.Join("..", "..", "testdata", "settings", "repo-secret.yaml"))
	if err != nil {
		t.Fatalf("LoadRepositorySecret() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	repo := settings.HelmRepositories["ghcr.io/example/charts"]
	if repo.URL != "ghcr.io/example/charts" || repo.Project != "" {
		t.Fatalf("repo metadata = %#v", repo)
	}
	if repo.Provenance.Path == "" {
		t.Fatalf("expected provenance")
	}
	serialized, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if strings.Contains(string(serialized), "should-not-be-read") {
		t.Fatalf("sensitive Secret data was retained: %s", serialized)
	}
}

func TestMergeSettingsDetectsConflict(t *testing.T) {
	left := DefaultSettings()
	left.KustomizeBuildOptions = []Value[string]{{Value: "--enable-helm", Provenance: Provenance{Path: "a.yaml"}}}
	right := DefaultSettings()
	right.KustomizeBuildOptions = []Value[string]{{Value: "--load-restrictor=LoadRestrictionsNone", Provenance: Provenance{Path: "b.yaml"}}}

	_, diags := MergeDiscovered([]ArgoSettings{left, right})
	if len(diags) != 1 {
		t.Fatalf("len(diags) = %d, want 1", len(diags))
	}
	if diags[0].Severity != "error" {
		t.Fatalf("severity = %s, want error", diags[0].Severity)
	}
}

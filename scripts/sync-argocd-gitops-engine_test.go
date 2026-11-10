package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCompatibilityReplacementsPreferUpstreamReplace(t *testing.T) {
	source := goModFile{
		Require: []goModRequire{
			{Path: "sigs.k8s.io/controller-runtime", Version: "v0.21.0"},
		},
		Replace: []goModReplace{
			{
				Old: goModModule{Path: "sigs.k8s.io/controller-runtime"},
				New: goModModule{Path: "example.com/controller-runtime", Version: "v0.22.0"},
			},
		},
	}

	replacements, err := compatibilityReplacements(source, []string{"sigs.k8s.io/controller-runtime"})
	if err != nil {
		t.Fatalf("compatibilityReplacements() error = %v", err)
	}
	if got := replacements[0].New.Path; got != "example.com/controller-runtime" {
		t.Fatalf("replacement path = %q, want upstream replace target", got)
	}
	if got := replacements[0].New.Version; got != "v0.22.0" {
		t.Fatalf("replacement version = %q, want upstream replace target version", got)
	}
}

func TestCompatibilityReplacementsFallsBackToControllerRuntimeRequire(t *testing.T) {
	source := goModFile{
		Require: []goModRequire{
			{Path: "sigs.k8s.io/controller-runtime", Version: "v0.21.0"},
		},
	}

	replacements, err := compatibilityReplacements(source, []string{"sigs.k8s.io/controller-runtime"})
	if err != nil {
		t.Fatalf("compatibilityReplacements() error = %v", err)
	}
	if got := replacements[0].New.Path; got != "sigs.k8s.io/controller-runtime" {
		t.Fatalf("replacement path = %q, want controller-runtime", got)
	}
	if got := replacements[0].New.Version; got != "v0.21.0" {
		t.Fatalf("replacement version = %q, want require version", got)
	}
}

func TestCompatibilityReplacementsRequiresConfiguredSource(t *testing.T) {
	_, err := compatibilityReplacements(goModFile{}, []string{"k8s.io/api"})
	if err == nil {
		t.Fatal("compatibilityReplacements() error = nil, want missing source error")
	}
	if !strings.Contains(err.Error(), "k8s.io/api") {
		t.Fatalf("compatibilityReplacements() error = %q, want module name", err)
	}
}

func TestCompatibilityReplacementsRejectsLocalReplace(t *testing.T) {
	source := goModFile{
		Replace: []goModReplace{
			{
				Old: goModModule{Path: "k8s.io/api"},
				New: goModModule{Path: "../api"},
			},
		},
	}

	_, err := compatibilityReplacements(source, []string{"k8s.io/api"})
	if err == nil {
		t.Fatal("compatibilityReplacements() error = nil, want local replace error")
	}
	if !strings.Contains(err.Error(), "versioned module") {
		t.Fatalf("compatibilityReplacements() error = %q, want versioned module error", err)
	}
}

func TestReadGoModUsesStructuredGoModJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "go.mod")
	body := `module example.com/source

go 1.26

require sigs.k8s.io/controller-runtime v0.21.0

replace k8s.io/api => k8s.io/api v0.34.0
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	mod, err := readGoMod(path)
	if err != nil {
		t.Fatalf("readGoMod() error = %v", err)
	}
	if len(mod.Require) != 1 || mod.Require[0].Path != "sigs.k8s.io/controller-runtime" || mod.Require[0].Version != "v0.21.0" {
		t.Fatalf("Require = %#v, want controller-runtime v0.21.0", mod.Require)
	}
	if len(mod.Replace) != 1 || mod.Replace[0].Old.Path != "k8s.io/api" || mod.Replace[0].New.Version != "v0.34.0" {
		t.Fatalf("Replace = %#v, want k8s.io/api v0.34.0", mod.Replace)
	}
}

func TestGitopsEngineRequirementUsesArgoRequire(t *testing.T) {
	source := goModFile{
		Require: []goModRequire{
			{Path: legacyGitopsEngineModule, Version: "v0.7.0"},
			{Path: gitopsEngineModule, Version: "v3.6.1"},
		},
	}
	version, err := gitopsEngineRequirement(source)
	if err != nil {
		t.Fatalf("gitopsEngineRequirement() error = %v", err)
	}
	if version != "v3.6.1" {
		t.Fatalf("gitopsEngineRequirement() = %q, want v3.6.1", version)
	}
}

func TestGitopsEngineRequirementRejectsMissingRequire(t *testing.T) {
	source := goModFile{
		Require: []goModRequire{{Path: legacyGitopsEngineModule, Version: "v0.7.0"}},
	}
	if _, err := gitopsEngineRequirement(source); err == nil || !strings.Contains(err.Error(), gitopsEngineModule) {
		t.Fatalf("gitopsEngineRequirement() error = %v, want missing %s error", err, gitopsEngineModule)
	}
}

// TestGojqMatchesSelectedArgoCD keeps gojq, and every module the gojq
// package compiles into drydock, at exactly the version the selected Argo CD
// requires, so that drydock parses and runs jq ignore rules as Argo CD does.
// Renovate ignores these modules, and the post-upgrade sync helper sets them
// after an Argo CD upgrade.
func TestGojqMatchesSelectedArgoCD(t *testing.T) {
	argo, err := goListModule(argoCDModule)
	if err != nil {
		t.Fatalf("goListModule(%s) error = %v", argoCDModule, err)
	}
	argoGoMod, err := readGoMod(argo.GoMod)
	if err != nil {
		t.Fatalf("readGoMod(%s) error = %v", argo.GoMod, err)
	}
	modules, err := gojqModules("readonly")
	if err != nil {
		t.Fatalf("gojqModules() error = %v", err)
	}
	if !slices.Contains(modules, gojqModule) {
		t.Fatalf("gojqModules() = %v, want gojq and the modules it compiles in", modules)
	}
	for _, module := range modules {
		want, err := argoRequirement(argoGoMod, module)
		if err != nil {
			t.Fatalf("argoRequirement(%s) error = %v", module, err)
		}
		got, err := goListModule(module)
		if err != nil {
			t.Fatalf("goListModule(%s) error = %v", module, err)
		}
		if got.Version != want {
			t.Fatalf("%s is %s, but Argo CD %s requires %s; run go run ./scripts/sync-argocd-gitops-engine.go", module, got.Version, argo.Version, want)
		}
	}
}

// TestDoublestarMatchesSelectedArgoCD keeps doublestar at exactly the
// version the selected Argo CD requires, so that drydock parses glob
// patterns as Argo CD does. Renovate ignores the module, and the
// post-upgrade sync helper sets it after an Argo CD upgrade.
func TestDoublestarMatchesSelectedArgoCD(t *testing.T) {
	argo, err := goListModule(argoCDModule)
	if err != nil {
		t.Fatalf("goListModule(%s) error = %v", argoCDModule, err)
	}
	argoGoMod, err := readGoMod(argo.GoMod)
	if err != nil {
		t.Fatalf("readGoMod(%s) error = %v", argo.GoMod, err)
	}
	want, err := argoRequirement(argoGoMod, doublestarModule)
	if err != nil {
		t.Fatalf("argoRequirement(%s) error = %v", doublestarModule, err)
	}
	got, err := goListModule(doublestarModule)
	if err != nil {
		t.Fatalf("goListModule(%s) error = %v", doublestarModule, err)
	}
	if got.Version != want {
		t.Fatalf("%s is %s, but Argo CD %s requires %s; run go run ./scripts/sync-argocd-gitops-engine.go", doublestarModule, got.Version, argo.Version, want)
	}
}

// TestArgoToolModulesMatchSelectedArgoCD keeps the kustomize and helm
// modules at the versions of the kustomize and helm binaries in the Argo CD
// image, and the modules they share with drydock (pflag, cobra, kustomize
// api and kyaml) at the versions those binaries build with. drydock reads
// kustomize and helm arguments as those binaries do, and a newer pflag
// changes the error text. Renovate ignores these modules, and the
// post-upgrade sync helper sets them after an Argo CD upgrade.
func TestArgoToolModulesMatchSelectedArgoCD(t *testing.T) {
	argo, err := goListModule(argoCDModule)
	if err != nil {
		t.Fatalf("goListModule(%s) error = %v", argoCDModule, err)
	}
	requirements, err := argoToolRequirements(argo.Dir)
	if err != nil {
		t.Fatalf("argoToolRequirements() error = %v", err)
	}
	for _, tool := range argoToolModules {
		for _, module := range append([]string{tool.module}, tool.pinned...) {
			got, err := goListModule(module)
			if err != nil {
				t.Fatalf("goListModule(%s) error = %v", module, err)
			}
			if got.Version == requirements[module] {
				continue
			}
			source := fmt.Sprintf("%s %s requires it", tool.module, requirements[tool.module])
			if module == tool.module {
				source = fmt.Sprintf("Argo CD %s sets %s in %s", argo.Version, tool.variable, argoToolVersionsFile)
			}
			t.Errorf("%s is %s, but %s at %s; run go run ./scripts/sync-argocd-gitops-engine.go", module, got.Version, source, requirements[module])
		}
	}
}

func TestArgoToolVersionsReadsToolVersionsFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "hack"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "#!/bin/sh\n# renovate: datasource=github-releases\nHELM_VERSION=4.3.1\nKUSTOMIZE_VERSION=5.8.3\nprotoc_version=29.3\n"
	if err := os.WriteFile(filepath.Join(dir, argoToolVersionsFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	versions, err := argoToolVersions(dir)
	if err != nil {
		t.Fatalf("argoToolVersions() error = %v", err)
	}
	if versions["HELM_VERSION"] != "v4.3.1" || versions["KUSTOMIZE_VERSION"] != "v5.8.3" {
		t.Fatalf("argoToolVersions() = %v, want HELM_VERSION v4.3.1 and KUSTOMIZE_VERSION v5.8.3", versions)
	}

	if err := os.WriteFile(filepath.Join(dir, argoToolVersionsFile), []byte("HELM_VERSION=4.3.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := argoToolVersions(dir); err == nil || !strings.Contains(err.Error(), "KUSTOMIZE_VERSION") {
		t.Fatalf("argoToolVersions() error = %v, want missing KUSTOMIZE_VERSION error", err)
	}
}

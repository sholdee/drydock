package main

import (
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

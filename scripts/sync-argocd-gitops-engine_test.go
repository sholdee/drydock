package main

import (
	"os"
	"path/filepath"
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

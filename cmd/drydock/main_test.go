package main

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestModuleLabelIncludesRuntimeModuleVersions(t *testing.T) {
	for _, modulePath := range []string{
		argoCDModulePath,
		gitOpsEngineModulePath,
		helmModulePath,
		kustomizeModulePath,
		jsonnetModulePath,
		kubernetesModulePath,
	} {
		t.Run(modulePath, func(t *testing.T) {
			label := moduleLabel(modulePath)
			if !strings.HasPrefix(label, modulePath+"@") {
				t.Fatalf("moduleLabel(%q) = %q, want module path with version", modulePath, label)
			}
			if strings.TrimPrefix(label, modulePath+"@") == "" {
				t.Fatalf("moduleLabel(%q) = %q, want non-empty version", modulePath, label)
			}
		})
	}
}

func TestFormatModuleLabelIncludesReplacement(t *testing.T) {
	label := formatModuleLabel(debug.Module{
		Path:    "example.test/module",
		Version: "v1.2.3",
		Replace: &debug.Module{
			Path:    "../module",
			Version: "v1.2.4-local",
		},
	})
	want := "example.test/module@v1.2.3 => ../module@v1.2.4-local"
	if label != want {
		t.Fatalf("formatModuleLabel() = %q, want %q", label, want)
	}
}

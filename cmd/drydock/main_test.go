package main

import (
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/rendercache"
)

func TestModuleLabelIncludesRuntimeModuleVersions(t *testing.T) {
	for _, modulePath := range []string{
		rendercache.ArgoCDModulePath,
		rendercache.GitOpsEngineModulePath,
		rendercache.HelmModulePath,
		rendercache.KustomizeModulePath,
		rendercache.JsonnetModulePath,
		rendercache.KubernetesModulePath,
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

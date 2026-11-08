package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
)

func loadKustomizeSettingsConfigMap(t *testing.T, data string) (ArgoSettings, []diagnostic.Diagnostic) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "argocd-cm.yaml")
	if err := os.WriteFile(path, []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: argocd-cm\ndata:\n"+data), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, diags, err := LoadFromConfigMap(path)
	if err != nil {
		t.Fatalf("LoadFromConfigMap() error = %v", err)
	}
	return settings, diags
}

// Argo CD registers a kustomize binary for kustomize.version.<name> and
// for kustomize.path.<name>, so both keys register the name and both warn.
func TestKustomizeVersionKeysRegisterAndWarn(t *testing.T) {
	settings, diags := loadKustomizeSettingsConfigMap(t, "  kustomize.version.v4.5.7: /tools/kustomize_4\n  kustomize.path.v5.0.0: /tools/kustomize_5\n")
	if got := settings.KustomizeVersionNames(); !slices.Equal(got, []string{"v4.5.7", "v5.0.0"}) {
		t.Fatalf("KustomizeVersionNames() = %v", got)
	}
	if !settings.KustomizeVersionRegistered("v4.5.7") || settings.KustomizeVersionRegistered("v9.9.9") {
		t.Fatal("KustomizeVersionRegistered() does not follow the keys")
	}
	assertSettingWarning(t, diags, "kustomize.version.v4.5.7 parsed but not applied", "data.kustomize.version.v4.5.7")
	assertSettingWarning(t, diags, "kustomize.path.v5.0.0 parsed but not applied", "data.kustomize.path.v5.0.0")
	if (ArgoSettings{}).KustomizeVersionRegistered("v9.9.9") != true {
		t.Fatal("without an argocd-cm, every kustomize version must count as registered")
	}
}

// One name in two keys fails the settings as in Argo CD.
func TestKustomizeVersionDuplicateFails(t *testing.T) {
	_, diags := loadKustomizeSettingsConfigMap(t, "  kustomize.version.v5.0.0: /a\n  kustomize.path.v5.0.0: /a\n")
	for _, diag := range diags {
		if diag.Severity == diagnostic.SeverityError && strings.Contains(diag.Message, `failed to add kustomize version from "kustomize.version.v5.0.0": found duplicate kustomize version: v5.0.0`) {
			return
		}
	}
	t.Fatalf("diagnostics = %#v, want the duplicate version error", diags)
}

// Two settings files that give one name two binary paths conflict. The
// same path is no conflict.
func TestMergeKustomizeVersions(t *testing.T) {
	first, _ := loadKustomizeSettingsConfigMap(t, "  kustomize.path.v5.0.0: /a\n")
	same, _ := loadKustomizeSettingsConfigMap(t, "  kustomize.version.v5.0.0: /a\n  kustomize.path.v4.0.0: /b\n")
	other, _ := loadKustomizeSettingsConfigMap(t, "  kustomize.path.v5.0.0: /c\n")

	merged, diags := MergeDiscovered([]ArgoSettings{first, same})
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v, want none for the same path", diags)
	}
	if got := merged.KustomizeVersionNames(); !slices.Equal(got, []string{"v4.0.0", "v5.0.0"}) {
		t.Fatalf("KustomizeVersionNames() = %v", got)
	}
	if !merged.ArgoCDConfigMapFound {
		t.Fatal("ArgoCDConfigMapFound = false after merging found ConfigMaps")
	}

	merged, diags = MergeDiscovered([]ArgoSettings{first, other})
	if len(diags) != 1 || diags[0].Severity != diagnostic.SeverityError || !strings.Contains(diags[0].Message, `conflicting kustomize version v5.0.0 paths "/a" and "/c"`) {
		t.Fatalf("diagnostics = %#v, want the path conflict", diags)
	}
	if got := merged.KustomizeVersions["v5.0.0"].Value; got != "/a" {
		t.Fatalf("v5.0.0 path = %q, want the first one kept", got)
	}
}

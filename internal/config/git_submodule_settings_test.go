package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadCmdParamsAppliesGitSubmoduleKeys pins that drydock reads
// reposerver.enable.git.submodule and
// applicationsetcontroller.enable.git.submodule as rendering keys, with
// the default true of ARGOCD_GIT_MODULES_ENABLED (env.ParseBoolFromEnv): no
// metadata-only warning, the value kept as written, and a conflict only
// when Argo CD would read two values differently. The repo-server key used
// to be metadata only.
func TestLoadCmdParamsAppliesGitSubmoduleKeys(t *testing.T) {
	settings, diags := commandParametersSettings("params.yaml", "data", map[string]string{
		enableGitSubmoduleKey:       "false",
		appSetEnableGitSubmoduleKey: "FALSE",
	})
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diags)
	}
	assertCommandParameter(t, settings, enableGitSubmoduleKey, "false", false, CommandParameterRendered)
	assertCommandParameter(t, settings, appSetEnableGitSubmoduleKey, "FALSE", false, CommandParameterRendered)
	if ParseEnableGitSubmodules(settings.EnableGitSubmodules.Value) || ParseEnableGitSubmodules(settings.AppSetEnableGitSubmodules.Value) {
		t.Fatalf("settings = %#v / %#v, want both off", settings.EnableGitSubmodules, settings.AppSetEnableGitSubmodules)
	}
	for value, want := range map[string]bool{"": true, "true": true, "no": true, "1": true, "false": false, "False": false} {
		if got := ParseEnableGitSubmodules(value); got != want {
			t.Errorf("ParseEnableGitSubmodules(%q) = %t, want %t", value, got, want)
		}
	}
	for _, key := range []string{enableGitSubmoduleKey, appSetEnableGitSubmoduleKey} {
		on, _ := commandParametersSettings("on.yaml", "data", map[string]string{key: "yes"})
		defaulted, _ := commandParametersSettings("same.yaml", "data", map[string]string{key: "true"})
		if _, diags := MergeDiscovered([]ArgoSettings{on, defaulted}); len(diags) != 0 {
			t.Fatalf("%s: diagnostics = %#v, want none for values Argo CD reads as true", key, diags)
		}
		off, _ := commandParametersSettings("off.yaml", "data", map[string]string{key: "false"})
		if _, conflict := MergeDiscovered([]ArgoSettings{on, off}); len(conflict) != 1 {
			t.Fatalf("%s: conflict diagnostics = %#v, want one", key, conflict)
		}
	}
}

// TestLoadRepositorySecretReadsEnableLfs pins that drydock reads the
// enableLfs field of a repository Secret, as Argo CD does
// (util/db/repository_secrets.go:362-366), and reports an invalid value
// under its own name.
func TestLoadRepositorySecretReadsEnableLfs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo-secret.yaml")
	write := func(value string) {
		if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: Secret
metadata:
  name: git
  labels:
    argocd.argoproj.io/secret-type: repository
stringData:
  url: https://git.example.test/org/repo.git
  enableLfs: "`+value+`"
`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("true")
	settings, diags, err := LoadRepositorySecret(path)
	if err != nil || len(diags) != 0 {
		t.Fatalf("LoadRepositorySecret() error = %v, diagnostics = %#v", err, diags)
	}
	if repo := settings.HelmRepositories["https://git.example.test/org/repo.git"]; !repo.EnableLFS {
		t.Fatalf("repo = %#v, want enableLfs", repo)
	}
	write("maybe")
	_, diags, err = LoadRepositorySecret(path)
	if err != nil || len(diags) != 1 || !strings.Contains(diags[0].Message, "invalid repository Secret enableLfs value") {
		t.Fatalf("LoadRepositorySecret(invalid) error = %v, diagnostics = %#v", err, diags)
	}
}

package app

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
)

// writeExecPluginSharedFileApps writes owner, a Kustomize Application whose
// overlay includes shared as a base, and reader, an exec plugin Application
// whose command reads shared/config.yaml from outside its spec.source.path
// (the policy copies shared/** into the plugin workspace). Changed-only
// cannot see what a plugin reads, and owner's graph owns shared/config.yaml,
// so a change there selects reader only through its manifest-generate-paths
// annotation (none when annotation is empty). An unrelated Application
// proves selection stays narrow.
func writeExecPluginSharedFileApps(t *testing.T, root, value, annotation string) {
	t.Helper()
	annotations := ""
	if annotation != "" {
		annotations = "  annotations:\n    argocd.argoproj.io/manifest-generate-paths: " + yamlSingleQuoted(annotation) + "\n"
	}
	writeTestFile(t, filepath.Join(root, "apps", "reader.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: reader
  namespace: argocd
`+annotations+`spec:
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: workloads/reader
    plugin:
      name: exec-renderer
  destination:
    name: in-cluster
    namespace: reader
`)
	writeTestFile(t, filepath.Join(root, "workloads", "reader", ".keep"), "")
	command := strings.Join(yamlSingleQuotedList(append(appExecCommand(t, "repo-param"), "../../shared/config.yaml")), ", ")
	writeTestFile(t, filepath.Join(root, ".drydock", "plugins.yaml"), `apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  exec-renderer:
    engine: exec
    generate:
      command: [`+command+`]
      timeout: `+testExecPolicyCommandTimeout+`
    copy:
      scope: repository
      include: ["shared/**"]
    env:
      allow: ["DRYDOCK_APP_EXEC_HELPER"]
`)
	writeTestFile(t, filepath.Join(root, "apps", "owner.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: owner
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: workloads/owner
  destination:
    name: in-cluster
    namespace: owner
`)
	writeTestFile(t, filepath.Join(root, "workloads", "owner", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../shared
`)
	writeTestFile(t, filepath.Join(root, "shared", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - config.yaml
`)
	writeTestFile(t, filepath.Join(root, "shared", "config.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: shared
data:
  value: `+value+`
`)
	writeDiffApplication(t, root, "other", "other", "same")
}

// writeExecPluginDeclaredSharedFileApps declares the shared directory.
func writeExecPluginDeclaredSharedFileApps(t *testing.T, root, value string) {
	t.Helper()
	writeExecPluginSharedFileApps(t, root, value, "/shared")
}

// writeExecPluginGlobSharedFileApps declares the shared file through a glob.
func writeExecPluginGlobSharedFileApps(t *testing.T, root, value string) {
	t.Helper()
	writeExecPluginSharedFileApps(t, root, value, "/shared/*.yaml")
}

// TestChangedOnlyDropsUndeclaredExecPluginInput is the negative control for
// the manifest-generate-paths rows of TestChangedOnlyMatchesFullDiff: without
// the annotation, changed-only selects only the Application whose graph owns
// the shared file and silently drops the plugin Application that reads it,
// even under --strict-changed-only. That is the documented limitation the
// annotation exists for; if changed-only learns to see plugin inputs, this
// fixture becomes a parity row.
func TestChangedOnlyDropsUndeclaredExecPluginInput(t *testing.T) {
	o, request := withExecPlugins(pathPairOracleSetup(func(t *testing.T, root, value string) {
		t.Helper()
		writeExecPluginSharedFileApps(t, root, value, "")
	}, "old", "new", DiscoveryOptions{}))(t)

	full, err := o.DiffApps(context.Background(), request)
	if err != nil {
		t.Fatalf("full DiffApps() error = %v, diagnostics = %#v", err, full.Diagnostics)
	}
	request.ChangedOnly = true
	request.StrictChangedOnly = true
	changedOnly, err := o.DiffApps(context.Background(), request)
	if err != nil {
		t.Fatalf("strict changed-only DiffApps() error = %v, diagnostics = %#v", err, changedOnly.Diagnostics)
	}
	for _, diag := range diagnostic.WithStableCodes(changedOnly.Diagnostics) {
		if diag.Category == "changed-only" {
			t.Fatalf("changed-only diagnostic %s, want a silent drop: %#v", diag.Code, changedOnly.Diagnostics)
		}
	}
	if got, want := diffedApplications(full), []string{"owner", "reader"}; !slices.Equal(got, want) {
		t.Fatalf("full diff Applications = %v, want %v", got, want)
	}
	if got, want := diffedApplications(changedOnly), []string{"owner"}; !slices.Equal(got, want) {
		t.Fatalf("changed-only diff Applications = %v, want %v", got, want)
	}
}

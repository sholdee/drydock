package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/pluginexec"
	"github.com/sholdee/drydock/internal/pluginpolicy"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

func appRevisionSelectionApp(name string, source argoappv1.ApplicationSource) argoappv1.Application {
	return argoappv1.Application{
		Name: name, Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{Project: "default", Source: &source},
	}
}

// A config management plugin source counts as a reader when the run renders
// it with a command, also a plugin that an .argocd-source override adds. A
// plain directory does not count.
func TestApplicationReadsAppRevisionCountsPluginSources(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "override", ".argocd-source.yaml"), "plugin:\n  name: added-by-override\n")
	writeTestFile(t, filepath.Join(root, "plain", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: plain\n")
	always := func(argoappv1.ApplicationSource) bool { return true }
	never := func(argoappv1.ApplicationSource) bool { return false }
	for _, tc := range []struct {
		name       string
		source     argoappv1.ApplicationSource
		pluginRuns func(argoappv1.ApplicationSource) bool
		want       bool
	}{
		{name: "plugin that runs", source: argoappv1.ApplicationSource{RepoURL: appRevisionTestRepoURL, Path: "plain", Plugin: &argoappv1.ApplicationSourcePlugin{Name: "cmp"}}, pluginRuns: always, want: true},
		{name: "plugin that does not run", source: argoappv1.ApplicationSource{RepoURL: appRevisionTestRepoURL, Path: "plain", Plugin: &argoappv1.ApplicationSourcePlugin{Name: "cmp"}}, pluginRuns: never, want: false},
		{name: "plugin from override that runs", source: argoappv1.ApplicationSource{RepoURL: appRevisionTestRepoURL, Path: "override"}, pluginRuns: always, want: true},
		{name: "plain directory", source: argoappv1.ApplicationSource{RepoURL: appRevisionTestRepoURL, Path: "plain"}, pluginRuns: always, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := applicationReadsAppRevision(root, defaultArgoCDControllerNamespace, appRevisionSelectionApp(tc.name, tc.source), tc.pluginRuns); got != tc.want {
				t.Fatalf("applicationReadsAppRevision() = %t, want %t", got, tc.want)
			}
			// The cache key test stays narrower: plugin sources never persist.
			if sourceReadsAppRevision(tc.source) {
				t.Fatalf("sourceReadsAppRevision() = true, want false: the key test must not count plugin sources")
			}
		})
	}
}

// pluginRunsWithBuildEnv asks what renderPluginSource asks: only a trusted
// exec or container policy plugin with --enable-plugins, whose checks before
// the command pass, or an injected PluginRenderer, runs a command with the
// build environment. Native engines and plugins that drydock cannot render
// do not.
func TestPluginRunsWithBuildEnvFollowsTheRenderer(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "marker.txt"), "from-source")
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "match.txt"), "match")
	pluginApp := func(name string, plugin argoappv1.ApplicationSourcePlugin) argoappv1.Application {
		return appRevisionSelectionApp(name, argoappv1.ApplicationSource{RepoURL: appRevisionTestRepoURL, Path: "manifests/plugin", Plugin: &plugin})
	}
	request := func(policy pluginpolicy.Policy) BuildRequest {
		return BuildRequest{Path: root, EnablePlugins: true, pluginPolicy: policy, pluginPolicyExecTrusted: true}
	}
	execPolicy, _ := testExecPluginPolicy(t, "exec-renderer", []string{"renderer"})
	containerPolicy, _ := testContainerPluginPolicy(t, "container-renderer")
	discoveryPolicy, _ := testExecPluginPolicyWithMatch(t, "exec-renderer", []string{"renderer"}, `    match:
      discover:
        fileName: match.txt
`)
	avpRoot := t.TempDir()
	writePluginPolicy(t, avpRoot, "avp-renderer", "avp-compat")
	avpPolicy, _ := readTestPluginPolicy(t, avpRoot)
	kustomizeRoot := t.TempDir()
	writeNativeKustomizePluginPolicySeed(t, kustomizeRoot, "kustomize-renderer", `"kustomize"`, `"build", "."`)
	kustomizePolicy, _ := readTestPluginPolicy(t, kustomizeRoot)

	disabled := request(execPolicy)
	disabled.EnablePlugins = false
	untrusted := request(execPolicy)
	untrusted.pluginPolicyExecTrusted = false
	for _, tc := range []struct {
		name        string
		request     BuildRequest
		application argoappv1.Application
		injected    bool
		want        bool
	}{
		{name: "trusted exec policy with --enable-plugins", request: request(execPolicy), application: pluginApp("exec", argoappv1.ApplicationSourcePlugin{Name: "exec-renderer"}), want: true},
		{name: "trusted container policy with --enable-plugins", request: request(containerPolicy), application: pluginApp("container", argoappv1.ApplicationSourcePlugin{Name: "container-renderer"}), want: true},
		{name: "unnamed plugin found by discovery", request: request(discoveryPolicy), application: pluginApp("unnamed", argoappv1.ApplicationSourcePlugin{}), want: true},
		{name: "avp-compat policy runs no command", request: request(avpPolicy), application: pluginApp("avp", argoappv1.ApplicationSourcePlugin{Name: "avp-renderer"}), want: false},
		{name: "native-kustomize policy runs no command", request: request(kustomizePolicy), application: pluginApp("kustomize", argoappv1.ApplicationSourcePlugin{Name: "kustomize-renderer"}), want: false},
		{name: "plugin env not allowed by policy", request: request(execPolicy), application: pluginApp("env", argoappv1.ApplicationSourcePlugin{Name: "exec-renderer", Env: argoappv1.Env{{Name: "SECRET", Value: "x"}}}), want: false},
		{name: "plugins not enabled", request: disabled, application: pluginApp("disabled", argoappv1.ApplicationSourcePlugin{Name: "exec-renderer"}), want: false},
		{name: "untrusted policy", request: untrusted, application: pluginApp("untrusted", argoappv1.ApplicationSourcePlugin{Name: "exec-renderer"}), want: false},
		{name: "no policy for the plugin", request: request(execPolicy), application: pluginApp("unknown", argoappv1.ApplicationSourcePlugin{Name: "no-such-plugin"}), want: false},
		{name: "injected plugin renderer", request: disabled, application: pluginApp("injected", argoappv1.ApplicationSourcePlugin{Name: "no-such-plugin"}), injected: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pluginRunsWithBuildEnv(tc.request, tc.injected, tc.application, *tc.application.Spec.Source); got != tc.want {
				t.Fatalf("pluginRunsWithBuildEnv() = %t, want %t", got, tc.want)
			}
		})
	}
}

// Discovery of an unnamed plugin runs against the root that the render
// uses: a --repo-map directory, not the side root.
func TestPluginRunsWithBuildEnvUsesTheRenderRoot(t *testing.T) {
	const mappedURL = "https://git.example.test/mapped/repo.git"
	root := t.TempDir()
	mapped := t.TempDir()
	writeTestFile(t, filepath.Join(mapped, "manifests", "plugin", "match.txt"), "match")
	policy, _ := testExecPluginPolicyWithMatch(t, "exec-renderer", []string{"renderer"}, `    match:
      discover:
        fileName: match.txt
`)
	request := BuildRequest{Path: root, EnablePlugins: true, pluginPolicy: policy, pluginPolicyExecTrusted: true, RepoMaps: []sourcepkg.RepoMap{{URL: mappedURL, Path: mapped}}}
	application := appRevisionSelectionApp("mapped", argoappv1.ApplicationSource{RepoURL: mappedURL, Path: "manifests/plugin", Plugin: &argoappv1.ApplicationSourcePlugin{}})
	if !pluginRunsWithBuildEnv(request, false, application, *application.Spec.Source) {
		t.Fatal("pluginRunsWithBuildEnv() = false, want discovery in the --repo-map directory to match")
	}
}

// A trusted exec plugin Application is selected in a changed-only diff when
// the two sides render different commits, and its command runs on both
// sides with the commit of each side.
func TestDiffChangedOnlySelectsTrustedExecPluginApp(t *testing.T) {
	root := t.TempDir()
	writePluginBuildApplication(t, root, "plugin", "exec-renderer")
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "marker.txt"), "from-source")
	writePersistentDirectoryCacheApp(t, root, "other")
	base := gitCommitAll(t, root, "base")
	writePersistentDirectorySource(t, root, "other", "changed")
	head := gitCommitAll(t, root, "change other")
	policy, fingerprint := testExecPluginPolicy(t, "exec-renderer", []string{"renderer"})
	runner := &revisionEchoExecRunner{}

	result, err := (Orchestrator{PluginExecRunner: runner}).DiffApps(context.Background(), DiffRequest{
		RightPath: root, Repo: root, Ref: "HEAD", RefOrig: base, ChangedOnly: true,
		EnablePlugins: true, pluginPolicyLoaded: true, pluginPolicy: policy, pluginPolicyFingerprint: fingerprint, pluginPolicyExecTrusted: true,
	})
	if err != nil {
		t.Fatalf("DiffApps() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	var found bool
	for _, item := range result.Results {
		if item.Resource.Name == "plugin-revision" {
			found = strings.Contains(item.Before, base) && strings.Contains(item.After, head)
		}
	}
	if !found {
		t.Fatalf("Results = %#v, want the plugin ConfigMap at %s and %s", result.Results, base, head)
	}
}

// revisionEchoExecRunner renders a ConfigMap that holds the
// ARGOCD_APP_REVISION the plugin got.
type revisionEchoExecRunner struct{}

func (revisionEchoExecRunner) Run(_ context.Context, request pluginexec.Request) (pluginexec.Result, error) {
	var revision string
	for _, entry := range request.ExtraEnv {
		if value, ok := strings.CutPrefix(entry, "ARGOCD_APP_REVISION="); ok {
			revision = value
		}
	}
	return pluginexec.Result{Stdout: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: plugin-revision\ndata:\n  revision: \"" + revision + "\"\n")}, nil
}

// A plugin Application that drydock cannot render, because no trusted policy
// matches it or plugins are off, stays out of the changed-only selection, so
// a change elsewhere does not make the diff fail with plugin.unsupported.
func TestDiffChangedOnlyLeavesUnrenderablePluginSourcesOut(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "apps", "plugin.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: plugin
  namespace: argocd
spec:
  project: default
  source:
    repoURL: `+appRevisionTestRepoURL+`
    targetRevision: main
    path: manifests/plugin
    plugin:
      name: no-such-plugin
  destination:
    name: in-cluster
    namespace: default
`)
	writeTestFile(t, filepath.Join(root, "manifests", "plugin", "marker.txt"), "from-source")
	writePersistentDirectoryCacheApp(t, root, "other")
	base := gitCommitAll(t, root, "base")
	writePersistentDirectorySource(t, root, "other", "changed")
	gitCommitAll(t, root, "change other")

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{RightPath: root, Repo: root, Ref: "HEAD", RefOrig: base, ChangedOnly: true})
	if err != nil {
		t.Fatalf("DiffApps() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	for _, diag := range result.Diagnostics {
		if diag.Code == diagnostic.CodePluginUnsupported || diag.Code == changedOnlyAppRevisionCode {
			t.Fatalf("Diagnostics = %#v, want the unrenderable plugin Application left out", result.Diagnostics)
		}
	}
}

// The Applications that a reader renders are selected with it, on both
// sides, because the reader's output can rewrite their specs.
func TestWithAppRevisionSelectionSelectsRenderedDescendants(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	leftHead := gitCommitAllWithFile(t, left, "left")
	rightHead := gitCommitAllWithFile(t, right, "right")
	if leftHead == rightHead {
		t.Fatal("the two sides must render different commits")
	}
	reader := appRevisionSelectionApp("parent", argoappv1.ApplicationSource{RepoURL: appRevisionTestRepoURL, Path: "apps", Helm: &argoappv1.ApplicationSourceHelm{Parameters: []argoappv1.HelmParameter{{Name: "rev", Value: "$ARGOCD_APP_REVISION"}}}})
	child := appRevisionSelectionApp("child", argoappv1.ApplicationSource{RepoURL: appRevisionTestRepoURL, Path: "child"})
	grandchild := appRevisionSelectionApp("grandchild", argoappv1.ApplicationSource{RepoURL: appRevisionTestRepoURL, Path: "grandchild"})
	unrelated := appRevisionSelectionApp("unrelated", argoappv1.ApplicationSource{RepoURL: appRevisionTestRepoURL, Path: "unrelated"})
	inputs := []ApplicationSelectionInput{
		{Application: reader},
		{Application: child, ParentKey: applicationKey(reader)},
		{Application: grandchild, ParentKey: applicationKey(child)},
		{Application: unrelated},
	}
	side := selectionSide{inputs: inputs}
	leftSelected, rightSelected, readers := withAppRevisionSelection(BuildRequest{Path: left}, BuildRequest{Path: right}, side, side, config.ArgoSettings{}, config.ArgoSettings{}, nil, nil, false)
	if readers != 1 {
		t.Fatalf("readers = %d, want 1", readers)
	}
	for _, selected := range [][]argoappv1.Application{leftSelected, rightSelected} {
		var names []string
		for _, application := range selected {
			names = append(names, application.Name)
		}
		if len(names) != 3 || names[0] != "parent" || names[1] != "child" || names[2] != "grandchild" {
			t.Fatalf("selected = %v, want parent, child, grandchild", names)
		}
	}
}

func gitCommitAllWithFile(t *testing.T, root, content string) string {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "README.md"), content+"\n")
	return gitCommitAll(t, root, content)
}

// --strict does not fail a changed-only diff on diff.changed-only-app-revision:
// the selection is complete, not an ownership gap.
func TestDiffChangedOnlyAppRevisionWarningIsStrictExempt(t *testing.T) {
	root := t.TempDir()
	writeAppRevisionChart(t, filepath.Join(root, "charts", "revision"))
	writeAppRevisionHelmApp(t, root, "reader", appRevisionTestRepoURL, "main", "charts/revision")
	writePersistentDirectoryCacheApp(t, root, "other")
	base := gitCommitAll(t, root, "base")
	writePersistentDirectorySource(t, root, "other", "changed")
	gitCommitAll(t, root, "change other")

	result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{RightPath: root, Repo: root, Ref: "HEAD", RefOrig: base, ChangedOnly: true, Strict: true, StrictChangedOnly: true})
	if err != nil {
		t.Fatalf("DiffApps(strict) error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	var found bool
	for _, diag := range result.Diagnostics {
		if diag.Code != changedOnlyAppRevisionCode {
			continue
		}
		found = true
		if diag.Severity != diagnostic.SeverityWarning {
			t.Fatalf("severity = %q, want warning under --strict", diag.Severity)
		}
	}
	if !found {
		t.Fatalf("Diagnostics = %#v, want %s", result.Diagnostics, changedOnlyAppRevisionCode)
	}
}

// An .argocd-source override cannot change targetRevision: the repo-server
// keeps the spec value when it merges (mergeSourceParameters,
// reposerver/repository/repository.go:1946-1949), and it resolves the
// revision from the spec before that. So an abbreviated SHA in an override
// does not reach ARGOCD_APP_REVISION.
func TestSourceOverrideTargetRevisionDoesNotReachAppRevision(t *testing.T) {
	root := t.TempDir()
	writeAppRevisionChart(t, filepath.Join(root, "charts", "revision"))
	head := gitCommitAll(t, root, "chart")
	writeTestFile(t, filepath.Join(root, "charts", "revision", ".argocd-source.yaml"), "targetRevision: \""+head[:9]+"\"\n")
	writeAppRevisionSourcesApp(t, root, "multi", appRevisionTestRepoURL, "main", "charts/revision", true)
	result, err := Orchestrator{}.Build(context.Background(), BuildRequest{Path: root})
	if err != nil {
		t.Fatalf("Build() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	assertAppRevisionChartData(t, result.Manifests, "multi-revision", head)
}

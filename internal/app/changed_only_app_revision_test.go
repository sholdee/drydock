package app

import (
	"context"
	"path/filepath"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
)

func appRevisionSelectionApp(name string, source argoappv1.ApplicationSource) argoappv1.Application {
	return argoappv1.Application{
		Name: name, Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{Project: "default", Source: &source},
	}
}

// A config management plugin source counts as a reader: the repo-server
// always puts ARGOCD_APP_REVISION in the plugin environment. A plugin that
// an .argocd-source override adds counts too. A plain directory does not.
func TestApplicationReadsAppRevisionCountsPluginSources(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "override", ".argocd-source.yaml"), "plugin:\n  name: added-by-override\n")
	writeTestFile(t, filepath.Join(root, "plain", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: plain\n")
	for _, tc := range []struct {
		name   string
		source argoappv1.ApplicationSource
		want   bool
	}{
		{name: "plugin", source: argoappv1.ApplicationSource{RepoURL: appRevisionTestRepoURL, Path: "plain", Plugin: &argoappv1.ApplicationSourcePlugin{Name: "cmp"}}, want: true},
		{name: "plugin from override", source: argoappv1.ApplicationSource{RepoURL: appRevisionTestRepoURL, Path: "override"}, want: true},
		{name: "plain directory", source: argoappv1.ApplicationSource{RepoURL: appRevisionTestRepoURL, Path: "plain"}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := applicationReadsAppRevision(root, defaultArgoCDControllerNamespace, appRevisionSelectionApp(tc.name, tc.source)); got != tc.want {
				t.Fatalf("applicationReadsAppRevision() = %t, want %t", got, tc.want)
			}
			// The cache key test stays narrower: plugin sources never persist.
			if sourceReadsAppRevision(tc.source) {
				t.Fatalf("sourceReadsAppRevision() = true, want false: the key test must not count plugin sources")
			}
		})
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
	leftSelected, rightSelected, readers := withAppRevisionSelection(BuildRequest{Path: left}, BuildRequest{Path: right}, side, side, config.ArgoSettings{}, config.ArgoSettings{}, nil, nil)
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

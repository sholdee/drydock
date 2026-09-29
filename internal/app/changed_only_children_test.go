package app

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/pluginexec"
)

// childApplicationYAML is the child Application a parent renders:
// spec.source.path is the only field the tests change, through the parent's
// input, never under the child's own path.
func childApplicationYAML(sourcePath string) string {
	return `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: child
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: ` + sourcePath + `
  destination:
    name: in-cluster
    namespace: child
`
}

// writeChildWorkloads writes both directories the child can point at, with
// different content but identical on both sides of a diff, and an unrelated
// Application that proves selection stays narrow.
func writeChildWorkloads(t *testing.T, root string) {
	t.Helper()
	for _, version := range []string{"v1", "v2"} {
		writeTestFile(t, filepath.Join(root, "workloads", "child-"+version, "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: child
data:
  value: `+version+`
`)
	}
	writeDiffApplication(t, root, "other", "other", "same")
}

// writeDiscoverKustomizeChildApps writes the child Application in
// argocd/base, rendered through the overlay clusters/prod-apps, which patches
// every Application so the rendered copy differs from the static file.
// version picks workloads/child-<version>. Discover it with
// DiscoverKustomizePaths [clusters/prod-apps].
func writeDiscoverKustomizeChildApps(t *testing.T, root, version string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "clusters", "prod-apps", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../argocd/base
patches:
  - target:
      group: argoproj.io
      kind: Application
    patch: |-
      - op: replace
        path: /spec/destination/namespace
        value: child-prod
`)
	writeTestFile(t, filepath.Join(root, "argocd", "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - child.yaml
`)
	writeTestFile(t, filepath.Join(root, "argocd", "base", "child.yaml"), childApplicationYAML("workloads/child-"+version))
	writeChildWorkloads(t, root)
}

// writeRenderedFleetKustomizeChildApps adds parent, an app-of-apps
// Application over clusters/prod-apps, to writeDiscoverKustomizeChildApps.
// Discover it with DiscoverIgnoreGlobs [argocd/base/**] so the child exists
// only as parent's rendered output.
func writeRenderedFleetKustomizeChildApps(t *testing.T, root, version string) {
	t.Helper()
	writeDiscoverKustomizeChildApps(t, root, version)
	writeTestFile(t, filepath.Join(root, "apps", "parent.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: parent
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: clusters/prod-apps
  destination:
    name: in-cluster
    namespace: argocd
`)
}

// writeAppOfAppsChart writes charts/apps, a local chart rendering the child
// Application with spec.source.path from .Values.childPath.
func writeAppOfAppsChart(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "charts", "apps", "Chart.yaml"), `apiVersion: v2
name: apps
version: 0.1.0
`)
	writeTestFile(t, filepath.Join(root, "charts", "apps", "templates", "child.yaml"), childApplicationYAML("{{ .Values.childPath }}"))
}

// writeHelmValuesAppOfAppsApps writes apps, a Helm app-of-apps Application
// over charts/apps whose child-defining value file sits in envs/prod,
// outside spec.source.path, and cluster-prod, a Kustomize Application whose
// overlay includes envs/prod as a base, so the sibling's graph owns the value
// file too. version picks workloads/child-<version>.
func writeHelmValuesAppOfAppsApps(t *testing.T, root, version string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "apps.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: apps
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: charts/apps
    helm:
      valueFiles:
        - ../../envs/prod/apps-values.yaml
  destination:
    name: in-cluster
    namespace: argocd
`)
	writeAppOfAppsChart(t, root)
	writeTestFile(t, filepath.Join(root, "envs", "prod", "apps-values.yaml"), "childPath: workloads/child-"+version+"\n")
	writeKustomizeSiblingOver(t, root, "envs/prod")
	writeChildWorkloads(t, root)
}

// writeNestedHelmAppOfAppsApps writes a two-level app-of-apps: apps, a Helm
// Application over charts/apps whose value file sits in envs/prod (outside
// spec.source.path), renders mid, a Helm Application over charts/mid that
// receives childPath as a parameter, and mid renders the child. cluster-prod's
// Kustomize graph owns envs/prod too. version picks workloads/child-<version>
// through all three levels: only the grandchild's workload moves, so
// selection must follow ParentKey past the first level.
func writeNestedHelmAppOfAppsApps(t *testing.T, root, version string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "apps.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: apps
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: charts/apps
    helm:
      valueFiles:
        - ../../envs/prod/apps-values.yaml
  destination:
    name: in-cluster
    namespace: argocd
`)
	writeTestFile(t, filepath.Join(root, "charts", "apps", "Chart.yaml"), `apiVersion: v2
name: apps
version: 0.1.0
`)
	writeTestFile(t, filepath.Join(root, "charts", "apps", "templates", "mid.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: mid
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: charts/mid
    helm:
      parameters:
        - name: childPath
          value: {{ .Values.childPath }}
  destination:
    name: in-cluster
    namespace: argocd
`)
	writeTestFile(t, filepath.Join(root, "charts", "mid", "Chart.yaml"), `apiVersion: v2
name: mid
version: 0.1.0
`)
	writeTestFile(t, filepath.Join(root, "charts", "mid", "templates", "child.yaml"), childApplicationYAML("{{ .Values.childPath }}"))
	writeTestFile(t, filepath.Join(root, "envs", "prod", "apps-values.yaml"), "childPath: workloads/child-"+version+"\n")
	writeKustomizeSiblingOver(t, root, "envs/prod")
	writeChildWorkloads(t, root)
}

// writeKustomizeSiblingOver writes cluster-prod, a Kustomize Application
// whose overlay clusters/prod includes dir as a base.
func writeKustomizeSiblingOver(t *testing.T, root, dir string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "cluster-prod.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: cluster-prod
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/example/repo
    targetRevision: main
    path: clusters/prod
  destination:
    name: in-cluster
    namespace: cluster
`)
	writeTestFile(t, filepath.Join(root, "clusters", "prod", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../`+dir+`
`)
	writeTestFile(t, filepath.Join(root, filepath.FromSlash(dir), "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - configmap.yaml
`)
	writeTestFile(t, filepath.Join(root, filepath.FromSlash(dir), "configmap.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: cluster
data:
  value: same
`)
}

// selfRepoAppOfAppsConsumerURL names the repository under diff in scp form:
// the provider matches it as the self repository, but it normalizes
// differently from the $repo ref's https URL, so select.go's same-repoURL
// rule does not own the ref value file and only the $ref augmentation does.
const selfRepoAppOfAppsConsumerURL = "git@github.com:example/repo.git"

// initSelfRepoRefAppOfAppsRepo commits, on master, apps (a local-chart
// app-of-apps Application whose child-defining value file is
// $repo/values/apps.yaml from the repository under diff) and cluster-prod,
// whose Kustomize graph includes values/. Branch feature changes only
// values/apps.yaml, moving the child from workloads/child-v1 to child-v2.
// Diff it in Repo mode with RefOrig master and Ref feature.
func initSelfRepoRefAppOfAppsRepo(t *testing.T) string {
	t.Helper()
	root, fixture := initSelfRepoDiffGitRepo(t, selfRepoRemoteURL)
	writeTestFile(t, filepath.Join(root, "apps", "apps.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: apps
  namespace: argocd
spec:
  sources:
    - repoURL: `+selfRepoSpecURL+`
      targetRevision: HEAD
      ref: repo
    - repoURL: `+selfRepoAppOfAppsConsumerURL+`
      targetRevision: HEAD
      path: charts/apps
      helm:
        valueFiles:
          - $repo/values/apps.yaml
  destination:
    name: in-cluster
    namespace: argocd
`)
	writeAppOfAppsChart(t, root)
	writeTestFile(t, filepath.Join(root, "values", "apps.yaml"), "childPath: workloads/child-v1\n")
	writeKustomizeSiblingOver(t, root, "values")
	writeChildWorkloads(t, root)
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "baseline")
	checkoutDiffGitBranch(t, fixture.wt, "feature")
	writeTestFile(t, filepath.Join(root, "values", "apps.yaml"), "childPath: workloads/child-v2\n")
	commitDiffGitRepo(t, fixture.repo, fixture.wt, "move child")
	return root
}

// assertChildWorkloadDiff requires that the diff selected the rendered child
// (its workload moved from child-v1 to child-v2) and exactly the wanted
// Applications, and that changed-only ownership was complete.
func assertChildWorkloadDiff(t *testing.T, result DiffResult, err error, wantApplications ...string) {
	t.Helper()
	if err != nil {
		t.Fatalf("DiffApps() error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	if diag, ok := diagnosticByCategory(result.Diagnostics, "changed-only"); ok {
		t.Fatalf("changed-only diagnostic = %#v, want complete ownership", diag)
	}
	diffs := map[string][]string{}
	for _, diffResult := range result.Results {
		diffs[diffResult.Parent.Name] = append(diffs[diffResult.Parent.Name], diffResult.Diff)
	}
	if got := slices.Sorted(maps.Keys(diffs)); !slices.Equal(got, wantApplications) {
		t.Fatalf("diffed Applications = %v, want %v", got, wantApplications)
	}
	childDiff := strings.Join(diffs["child"], "\n")
	for _, want := range []string{"argocd/child", "-  value: v1", "+  value: v2"} {
		if !strings.Contains(childDiff, want) {
			t.Fatalf("child diff missing %q:\n%s", want, childDiff)
		}
	}
}

// Each case changes only an input the child's origin owns through a
// selection-only channel — never a file under the child's own
// spec.source.path — and rewrites the child's spec.source.path.
func TestOrchestratorDiffAppsStrictChangedOnlySelectsRenderedChildren(t *testing.T) {
	for _, tc := range []struct {
		name             string
		write            func(*testing.T, string, string)
		discovery        DiscoveryOptions
		wantApplications []string
	}{
		{
			// No parent Application: the child owns the discover-kustomize
			// directory's graph.
			name:             "discover-kustomize base",
			write:            writeDiscoverKustomizeChildApps,
			discovery:        DiscoveryOptions{DiscoverKustomizePaths: []string{"clusters/prod-apps"}},
			wantApplications: []string{"child"},
		},
		{
			name:             "rendered-fleet Kustomize parent",
			write:            writeRenderedFleetKustomizeChildApps,
			discovery:        DiscoveryOptions{DiscoverIgnoreGlobs: []string{"argocd/base/**"}},
			wantApplications: []string{"child", "parent"},
		},
		{
			name:             "Helm parent escaped value file",
			write:            writeHelmValuesAppOfAppsApps,
			wantApplications: []string{"apps", "child"},
		},
		{
			// The value file reaches the grandchild's spec only through mid.
			name:             "nested Helm app-of-apps grandchild",
			write:            writeNestedHelmAppOfAppsApps,
			wantApplications: []string{"apps", "child", "mid"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			left := filepath.Join(root, "left")
			right := filepath.Join(root, "right")
			tc.write(t, left, "v1")
			tc.write(t, right, "v2")

			result, err := Orchestrator{}.DiffApps(context.Background(), DiffRequest{
				LeftPath:          left,
				RightPath:         right,
				DiscoveryOptions:  tc.discovery,
				ChangedOnly:       true,
				StrictChangedOnly: true,
				Unified:           3,
			})
			assertChildWorkloadDiff(t, result, err, tc.wantApplications...)
		})
	}
}

func TestOrchestratorDiffAppsStrictChangedOnlySelectsSelfRepoRefChildren(t *testing.T) {
	root := initSelfRepoRefAppOfAppsRepo(t)

	gitAcquirer := &countingGitAcquirer{err: errors.New("self-repo ref must not be acquired")}
	result, err := (Orchestrator{GitAcquirer: gitAcquirer}).DiffApps(context.Background(), DiffRequest{
		Repo:              root,
		RefOrig:           "master",
		Ref:               "feature",
		ChangedOnly:       true,
		StrictChangedOnly: true,
		Unified:           3,
		Parallelism:       1,
	})
	assertChildWorkloadDiff(t, result, err, "apps", "child")
	if got := gitAcquirer.calls(); got != 0 {
		t.Fatalf("git acquire calls = %d, want 0: %#v", got, gitAcquirer.requests)
	}
}

// TestListApplicationsRecordsRenderedOrigin pins the data flow inheritance
// relies on: discovery records each rendered Application's origin, and
// ApplicationInputs carry it, including Applications generated by a
// rendered ApplicationSet.
func TestListApplicationsRecordsRenderedOrigin(t *testing.T) {
	root := t.TempDir()
	writeRenderedFleetKustomizeChildApps(t, root, "v1")
	writeTestFile(t, filepath.Join(root, "argocd", "base", "kustomization.yaml"), `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - child.yaml
  - appset.yaml
`)
	writeTestFile(t, filepath.Join(root, "argocd", "base", "appset.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: generated
  namespace: argocd
spec:
  generators:
    - list:
        elements:
          - version: v2
  template:
    metadata:
      name: generated-{{version}}
      namespace: argocd
    spec:
      project: default
      source:
        repoURL: https://github.com/example/repo
        targetRevision: main
        path: workloads/child-{{version}}
      destination:
        name: in-cluster
        namespace: generated
`)

	origins := func(request BuildRequest) map[string][2]string {
		t.Helper()
		result, err := Orchestrator{}.ListApplications(context.Background(), request)
		if err != nil {
			t.Fatalf("ListApplications() error = %v", err)
		}
		out := map[string][2]string{}
		for _, input := range result.ApplicationInputs {
			out[input.Application.Name] = [2]string{input.ParentKey, input.RenderedDir}
		}
		return out
	}

	// Hide the static copies: an identical static object wins the merge.
	ignoreBase := []string{"argocd/base/**"}
	parentKey := applicationKey(argoappv1.Application{Namespace: "argocd", Name: "parent"})
	fleet := origins(BuildRequest{Path: root, DiscoverIgnoreGlobs: ignoreBase})
	for name, want := range map[string][2]string{
		"parent":       {},
		"other":        {},
		"child":        {parentKey, ""},
		"generated-v2": {parentKey, ""},
	} {
		if got, ok := fleet[name]; !ok || got != want {
			t.Errorf("rendered-fleet origin of %s = %q (found %v), want %q", name, got, ok, want)
		}
	}

	explicit := origins(BuildRequest{
		Path:                   root,
		DiscoveryMode:          DiscoveryModeStatic,
		DiscoverKustomizePaths: []string{"./clusters/prod-apps/"},
		DiscoverIgnoreGlobs:    ignoreBase,
	})
	for name, want := range map[string][2]string{
		"parent":       {},
		"child":        {"", "clusters/prod-apps"},
		"generated-v2": {"", "clusters/prod-apps"},
	} {
		if got, ok := explicit[name]; !ok || got != want {
			t.Errorf("discover-kustomize origin of %s = %q (found %v), want %q", name, got, ok, want)
		}
	}
}

// TestListApplicationsRecordsPolicyBootstrapOrigin pins that plugin-policy
// bootstrap output, which no Application renders, records its entrypoint
// directory, and that generated Applications inherit it.
func TestListApplicationsRecordsPolicyBootstrapOrigin(t *testing.T) {
	root := t.TempDir()
	writeBootstrapExecPluginPolicy(t, root, "bootstrap", "PklProject")
	writeTestFile(t, filepath.Join(root, "bootstrap", "PklProject"), "")
	runner := &recordingExecRunner{
		result: pluginexec.Result{Stdout: []byte(bootstrapApplicationYAML("bootstrap-child", "workloads/bootstrap-child") + "---\n" + bootstrapApplicationSetYAML())},
	}

	result, err := (Orchestrator{PluginExecRunner: runner}).ListApplications(context.Background(), BuildRequest{
		Path:                 root,
		MaxDiscoveryDepth:    0,
		MaxDiscoveryDepthSet: true,
		PluginOptions:        trustedBootstrapPluginOptions(t, root),
	})
	if err != nil {
		t.Fatalf("ListApplications() error = %v\nDiagnostics: %#v", err, result.Diagnostics)
	}
	got := map[string][2]string{}
	for _, input := range result.ApplicationInputs {
		got[input.Application.Name] = [2]string{input.ParentKey, input.RenderedDir}
	}
	want := map[string][2]string{
		"bootstrap-child": {"", "bootstrap"},
		"generated":       {"", "bootstrap"},
	}
	if !maps.Equal(got, want) {
		t.Fatalf("origins = %q, want %q", got, want)
	}
}

// Rendered Applications keep only their own inputs; selection, not path
// copies, reaches them through their recorded parents or rendered
// directories.
func TestSelectChangedDiffSidesSelectsRenderedDescendants(t *testing.T) {
	root := t.TempDir()
	// web-prod owns envs/prod/web-values.yaml only through its Helm value
	// file; clusters/prod's graph owns envs/prod/configmap.yaml.
	writeHelmEscapedValueFileApps(t, root, "v")
	const valueFile = "envs/prod/web-values.yaml"
	const graphFile = "envs/prod/configmap.yaml"

	key := func(name string) string {
		return applicationKey(argoappv1.Application{Namespace: "argocd", Name: name})
	}
	input := func(name, parent, renderedDir string, sources ...argoappv1.ApplicationSource) ApplicationSelectionInput {
		return ApplicationSelectionInput{
			Application: argoappv1.Application{Namespace: "argocd", Name: name, Spec: argoappv1.ApplicationSpec{Sources: sources}},
			Paths:       []string{"rendered/" + name},
			ParentKey:   parent,
			RenderedDir: renderedDir,
		}
	}
	helmParent := argoappv1.ApplicationSource{
		RepoURL: "https://github.com/example/repo",
		Path:    "charts/web",
		Helm:    &argoappv1.ApplicationSourceHelm{ValueFiles: []string{"../../" + valueFile}},
	}
	inputs := []ApplicationSelectionInput{
		input("grandchild", key("child"), ""),
		input("child", key("parent"), ""),
		input("parent", "", "", helmParent),
		input("cycle-a", key("cycle-b"), ""),
		input("cycle-b", key("cycle-a"), ""),
		input("self-parent", key("self-parent"), ""),
		input("orphan", key("missing"), ""),
		input("explicit-child", key("explicit"), ""),
		// One rendered directory declares both.
		input("explicit", "", "clusters/prod"),
		input("explicit-sibling", "", "clusters/prod"),
		// A listed directory is not a render root and is never walked.
		{Application: argoappv1.Application{Namespace: "argocd", Name: "listed-dir"}, Paths: []string{"clusters/prod"}},
	}

	augmented := withSelectionOnlyPaths(context.Background(), root, nil, selfRepoRefs{}, inputs)

	for i, output := range augmented.inputs {
		name := output.Application.Name
		switch {
		case name == "parent":
			if !slices.Contains(output.Paths, valueFile) {
				t.Errorf("parent paths %v missing %s", output.Paths, valueFile)
			}
		// The rendered directory's graph is never copied into the Paths of
		// the Applications it declares.
		case !slices.Equal(output.Paths, inputs[i].Paths):
			t.Errorf("%s paths = %v, want only its own %v", name, output.Paths, inputs[i].Paths)
		}
		if output.ParentKey != inputs[i].ParentKey || output.RenderedDir != inputs[i].RenderedDir {
			t.Errorf("%s origin = %q/%q, want %q/%q", name, output.ParentKey, output.RenderedDir, inputs[i].ParentKey, inputs[i].RenderedDir)
		}
		if len(inputs[i].Paths) != 1 {
			t.Fatalf("input %s Paths mutated to %v; listed inputs also key the render cache", name, inputs[i].Paths)
		}
	}
	if got := slices.Sorted(maps.Keys(augmented.renderedDirs)); !slices.Equal(got, []string{"clusters/prod"}) {
		t.Fatalf("rendered dirs = %v, want [clusters/prod]", got)
	}
	if graph := augmented.renderedDirs["clusters/prod"]; !slices.Contains(graph, graphFile) {
		t.Fatalf("clusters/prod graph %v missing %s", graph, graphFile)
	}

	// The right side adds a child of parent and an Application of the
	// rendered directory that the left side lacks.
	rightSide := selectionSide{
		inputs:       append(slices.Clone(augmented.inputs), input("added-child", key("parent"), ""), input("added-explicit", "", "clusters/prod")),
		renderedDirs: augmented.renderedDirs,
	}
	for _, tc := range []struct {
		changed   string
		wantLeft  []string
		wantRight []string
	}{
		{changed: "rendered/parent/cm.yaml", wantLeft: []string{"child", "grandchild", "parent"}, wantRight: []string{"added-child", "child", "grandchild", "parent"}},
		// clusters/prod's graph owns the envs/prod directory too.
		{changed: valueFile, wantLeft: []string{"child", "explicit", "explicit-child", "explicit-sibling", "grandchild", "parent"}, wantRight: []string{"added-child", "added-explicit", "child", "explicit", "explicit-child", "explicit-sibling", "grandchild", "parent"}},
		{changed: "rendered/child/cm.yaml", wantLeft: []string{"child", "grandchild"}},
		{changed: "rendered/cycle-a/cm.yaml", wantLeft: []string{"cycle-a", "cycle-b"}},
		{changed: "rendered/self-parent/cm.yaml", wantLeft: []string{"self-parent"}},
		{changed: "rendered/orphan/cm.yaml", wantLeft: []string{"orphan"}},
		{changed: "rendered/explicit/cm.yaml", wantLeft: []string{"explicit", "explicit-child"}},
		// Only the rendered directory's graph owns graphFile.
		{changed: graphFile, wantLeft: []string{"explicit", "explicit-child", "explicit-sibling"}, wantRight: []string{"added-explicit", "explicit", "explicit-child", "explicit-sibling"}},
		{changed: "clusters/prod/kustomization.yaml", wantLeft: []string{"explicit", "explicit-child", "explicit-sibling", "listed-dir"}, wantRight: []string{"added-explicit", "explicit", "explicit-child", "explicit-sibling", "listed-dir"}},
	} {
		if tc.wantRight == nil {
			tc.wantRight = tc.wantLeft
		}
		left, right, unowned := selectChangedDiffSides(augmented, rightSide, []string{tc.changed})
		if len(unowned) != 0 {
			t.Errorf("%s unowned = %v", tc.changed, unowned)
		}
		if got := applicationNames(left); !slices.Equal(got, tc.wantLeft) {
			t.Errorf("%s left selected %v, want %v", tc.changed, got, tc.wantLeft)
		}
		if got := applicationNames(right); !slices.Equal(got, tc.wantRight) {
			t.Errorf("%s right selected %v, want %v", tc.changed, got, tc.wantRight)
		}
	}
}

// A rendered directory's graph owns changed paths on its own side only, but
// once either side's graph selects the directory, every Application it
// declared is selected on both sides, with its rendered descendants.
func TestSelectChangedDiffSidesSelectsRenderedDirApplications(t *testing.T) {
	const graphFile = "envs/prod/configmap.yaml"
	input := func(name, parent, renderedDir string) ApplicationSelectionInput {
		return ApplicationSelectionInput{
			Application: argoappv1.Application{Namespace: "argocd", Name: name},
			Paths:       []string{"rendered/" + name},
			ParentKey:   parent,
			RenderedDir: renderedDir,
		}
	}
	left := []ApplicationSelectionInput{
		input("explicit", "", "clusters/prod"),
		input("explicit-sibling", "", "clusters/prod"),
		input("explicit-child", applicationKey(argoappv1.Application{Namespace: "argocd", Name: "explicit"}), ""),
		input("other", "", "clusters/other"),
	}
	// The right side adds an Application the directory declares.
	right := append(slices.Clone(left), input("added-explicit", "", "clusters/prod"))
	graphs := map[string][]string{
		"clusters/prod":  {"clusters/prod/kustomization.yaml", graphFile},
		"clusters/other": {"clusters/other/kustomization.yaml"},
	}
	wantLeft := []string{"explicit", "explicit-child", "explicit-sibling"}
	wantRight := []string{"added-explicit", "explicit", "explicit-child", "explicit-sibling"}
	for _, tc := range []struct {
		name                string
		leftDirs, rightDirs map[string][]string
		wantUnowned         []string
	}{
		{name: "both graphs", leftDirs: graphs, rightDirs: graphs},
		{name: "left graph only", leftDirs: graphs},
		{name: "right graph only", rightDirs: graphs},
		{name: "no graph", wantUnowned: []string{graphFile}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			leftSelected, rightSelected, unowned := selectChangedDiffSides(
				selectionSide{inputs: left, renderedDirs: tc.leftDirs},
				selectionSide{inputs: right, renderedDirs: tc.rightDirs},
				[]string{graphFile},
			)
			if !slices.Equal(unowned, tc.wantUnowned) {
				t.Fatalf("unowned = %v, want %v", unowned, tc.wantUnowned)
			}
			if tc.wantUnowned != nil {
				return
			}
			if got := applicationNames(leftSelected); !slices.Equal(got, wantLeft) {
				t.Errorf("left selected %v, want %v", got, wantLeft)
			}
			if got := applicationNames(rightSelected); !slices.Equal(got, wantRight) {
				t.Errorf("right selected %v, want %v", got, wantRight)
			}
		})
	}
}

// A discovery merge can replace a parent with its namespace-defaulted twin
// (namespaceDefaultedConflict), so a recorded ParentKey that names no input
// falls back to same-name Applications where one namespace is empty.
func TestParentResolverFallsBackToNamespaceDefaultedTwin(t *testing.T) {
	app := func(namespace, name string) ApplicationSelectionInput {
		return ApplicationSelectionInput{Application: argoappv1.Application{Namespace: namespace, Name: name}}
	}
	key := func(namespace, name string) string {
		return applicationKey(argoappv1.Application{Namespace: namespace, Name: name})
	}
	resolver := newParentResolver([]ApplicationSelectionInput{
		app("argocd", "defaulted"),
		app("", "bare"),
		app("team-a", "shared"),
		app("team-b", "shared"),
		app("argocd", "exact"),
		app("", "exact"),
		app("argocd", "namespaced"),
	})
	for _, tc := range []struct {
		name      string
		parentKey string
		want      []string
	}{
		{name: "exact key", parentKey: key("argocd", "exact"), want: []string{key("argocd", "exact")}},
		{name: "namespace defaulted after render", parentKey: key("", "defaulted"), want: []string{key("argocd", "defaulted")}},
		{name: "namespace dropped after render", parentKey: key("argocd", "bare"), want: []string{key("", "bare")}},
		{name: "ambiguous selects every twin", parentKey: key("", "shared"), want: []string{key("team-a", "shared"), key("team-b", "shared")}},
		{name: "two set namespaces never match", parentKey: key("other", "namespaced")},
		{name: "unknown name", parentKey: key("argocd", "missing")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolver.resolve(tc.parentKey); !slices.Equal(got, tc.want) {
				t.Fatalf("resolve(%q) = %q, want %q", tc.parentKey, got, tc.want)
			}
		})
	}

	// End to end through selection: the child still follows the twin.
	inputs := []ApplicationSelectionInput{
		{Application: argoappv1.Application{Namespace: "argocd", Name: "parent"}, Paths: []string{"parent"}},
		{Application: argoappv1.Application{Namespace: "argocd", Name: "child"}, Paths: []string{"child"}, ParentKey: key("", "parent")},
	}
	side := selectionSide{inputs: inputs}
	left, right, _ := selectChangedDiffSides(side, side, []string{"parent/values.yaml"})
	for _, selected := range [][]argoappv1.Application{left, right} {
		if got := applicationNames(selected); !slices.Equal(got, []string{"child", "parent"}) {
			t.Fatalf("selected %v, want [child parent]", got)
		}
	}
}

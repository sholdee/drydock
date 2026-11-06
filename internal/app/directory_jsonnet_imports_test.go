package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/render"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

// writeJsonnetImportFixture writes a directory source whose Jsonnet file
// imports files outside its path and its libraries: a relative import, an
// importstr, a nested import, and an import through a symlink.
func writeJsonnetImportFixture(t *testing.T, root string) argoappv1.Application {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "app", "main.jsonnet"), `local shared = import '../shared/lib.libsonnet';
local text = importstr '../shared/text.txt';
local linked = import 'linked.libsonnet';
{
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: { name: 'demo' },
  data: { name: shared.name, nested: shared.nested, text: text, linked: linked.name },
}
`)
	writeTestFile(t, filepath.Join(root, "shared", "lib.libsonnet"), "{ name: 'shared', nested: (import 'nested.libsonnet').value }\n")
	writeTestFile(t, filepath.Join(root, "shared", "nested.libsonnet"), "{ value: 'nested' }\n")
	writeTestFile(t, filepath.Join(root, "shared", "text.txt"), "text")
	writeTestFile(t, filepath.Join(root, "other", "target.libsonnet"), "{ name: 'linked' }\n")
	symlinkBoundsLink(t, "../other/target.libsonnet", filepath.Join(root, "app", "linked.libsonnet"))
	writeTestFile(t, filepath.Join(root, "unrelated", "README.md"), "not a render input\n")
	return argoappv1.Application{
		Name:      "demo",
		Namespace: "argocd",
		Spec: argoappv1.ApplicationSpec{
			Source: &argoappv1.ApplicationSource{
				RepoURL: "https://git.example.test/org/repo.git", Path: "app", TargetRevision: "main",
				Directory: &argoappv1.ApplicationSourceDirectory{},
			},
			Destination: argoappv1.ApplicationDestination{Namespace: "default"},
		},
	}
}

// TestRenderInputCoverageDirectoryJsonnetImports pins that the persistent
// render cache digest covers the files that a Jsonnet file imports from
// outside the source path and its libraries.
func TestRenderInputCoverageDirectoryJsonnetImports(t *testing.T) {
	root := t.TempDir()
	application := writeJsonnetImportFixture(t, root)
	assertRenderInputCoverage(t, root, application)
}

// TestWithSelectionOnlyPathsOwnsJsonnetImports pins that changed-only
// selection owns the files that a Jsonnet file imports from outside the
// source path, and the candidates of an import that a new file would win.
func TestWithSelectionOnlyPathsOwnsJsonnetImports(t *testing.T) {
	root := t.TempDir()
	application := writeJsonnetImportFixture(t, root)
	got := withSelectionOnlyPaths(context.Background(), root, nil, selfRepoRefs{}, []ApplicationSelectionInput{{Application: application}}).inputs
	for _, changed := range []string{"shared/lib.libsonnet", "shared/nested.libsonnet", "shared/text.txt", "other/target.libsonnet"} {
		if selected, _ := SelectChangedApplicationInputs(got, []string{changed}); len(selected) != 1 {
			t.Errorf("change to %s selected %d Applications, want the Jsonnet source (paths %v)", changed, len(selected), got[0].Paths)
		}
	}
	if selected, _ := SelectChangedApplicationInputs(got, []string{"unrelated/README.md"}); len(selected) != 0 {
		t.Errorf("an unrelated change selected the Jsonnet source")
	}
}

// writeJsonnetFanoutApps writes fanout, a directory source whose Jsonnet
// import graph grows without end through two symlinks to its own directory,
// and other, a plain directory source.
func writeJsonnetFanoutApps(t *testing.T, root, otherValue string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "apps", "fanout.yaml"), symlinkedRootApplication("fanout", "https://git.example.test/repo.git", "fanout"))
	writeTestFile(t, filepath.Join(root, "apps", "other.yaml"), symlinkedRootApplication("other", "https://git.example.test/repo.git", "other"))
	writeTestFile(t, filepath.Join(root, "fanout", "x.jsonnet"), `local a = import 's1/x.jsonnet';
local b = import 's2/x.jsonnet';
{ apiVersion: 'v1', kind: 'ConfigMap', metadata: { name: 'fanout' } }
`)
	symlinkBoundsLink(t, ".", filepath.Join(root, "fanout", "s1"))
	symlinkBoundsLink(t, ".", filepath.Join(root, "fanout", "s2"))
	writeTestFile(t, filepath.Join(root, "other", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: other\ndata:\n  value: "+otherValue+"\n")
}

// TestJsonnetImportScanBudgetKeepsCallersBounded pins both callers of the
// Jsonnet import scan on an import graph that grows without end. The
// persistent cache digest fails, so the source is rendered without a cache
// entry, and changed-only selection renders the source on every change,
// with a strict-exempt warning. Both finish well inside a short deadline.
func TestJsonnetImportScanBudgetKeepsCallersBounded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	root := t.TempDir()
	writeJsonnetFanoutApps(t, root, "one")
	application := argoappv1.Application{
		Name: "fanout",
		Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{RepoURL: "https://git.example.test/repo.git", TargetRevision: "main", Path: "fanout"}},
	}
	provider := localProvider{repoRoot: root, sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{})}
	plan := mustPlan(t, application)
	if _, _, err := localInputDigestPathsForSource(ctx, plan, plan.Sources[0], provider); !errors.Is(err, render.ErrJsonnetImportScanBudget) {
		t.Fatalf("localInputDigestPathsForSource() error = %v, want the budget error", err)
	}

	parent := t.TempDir()
	left := filepath.Join(parent, "left")
	right := filepath.Join(parent, "right")
	writeJsonnetFanoutApps(t, left, "one")
	writeJsonnetFanoutApps(t, right, "two")
	result, err := Orchestrator{}.DiffApps(ctx, DiffRequest{LeftPath: left, RightPath: right, ChangedOnly: true, StrictChangedOnly: true, Unified: 3})
	if err != nil {
		t.Fatalf("DiffApps() error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	found := false
	for _, diag := range result.Diagnostics {
		if diag.Code == changedOnlyJsonnetScanCode && strings.Contains(diag.Message, "argocd/fanout") {
			found = true
		}
	}
	if !found {
		t.Fatalf("diagnostics = %#v, want the Jsonnet scan warning for fanout", result.Diagnostics)
	}
	side := withSelectionOnlyPaths(ctx, left, nil, selfRepoRefs{}, []ApplicationSelectionInput{{Application: application}})
	leftSelected, _, unowned := selectChangedDiffSides(side, side, []string{"unrelated/README.md"}, nil)
	if len(leftSelected) != 1 {
		t.Fatal("an unrelated change did not select the source whose inputs are not known")
	}
	if len(unowned) != 1 {
		t.Fatalf("unowned = %v, want the unrelated path still unowned", unowned)
	}
	if ctx.Err() != nil {
		t.Fatal("the callers ran past their deadline")
	}
}

// TestChangedOnlyKeepsTheUnownedFallbackWithAnUnscannedSource pins the
// review repro: a source whose Jsonnet imports could not be scanned is
// selected on any change, but a changed path that no Application owns
// still renders every Application with diff.changed-only-incomplete.
func TestChangedOnlyKeepsTheUnownedFallbackWithAnUnscannedSource(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	parent := t.TempDir()
	left := filepath.Join(parent, "left")
	right := filepath.Join(parent, "right")
	writeJsonnetFanoutApps(t, left, "one")
	writeJsonnetFanoutApps(t, right, "one")
	writeTestFile(t, filepath.Join(left, "shared", "x.yaml"), "a: 1\n")
	writeTestFile(t, filepath.Join(right, "shared", "x.yaml"), "a: 2\n")
	result, err := Orchestrator{}.DiffApps(ctx, DiffRequest{LeftPath: left, RightPath: right, ChangedOnly: true, Unified: 3})
	if err != nil {
		t.Fatalf("DiffApps() error = %v, diagnostics = %#v", err, result.Diagnostics)
	}
	found := false
	for _, diag := range result.Diagnostics {
		if strings.Contains(diag.Message, "could not map 1 changed path(s); rendering all Applications: shared/x.yaml") {
			found = true
		}
	}
	if !found {
		t.Fatalf("diagnostics = %#v, want the unowned render-all", result.Diagnostics)
	}
}

// TestWithSelectionOnlyPathsScansOnlyDirectorySources pins that symlink
// targets and Jsonnet imports are owned only by a directory source. The
// Helm and Kustomize renderers reject symlinks and evaluate no Jsonnet.
func TestWithSelectionOnlyPathsScansOnlyDirectorySources(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "chart", "Chart.yaml"), "apiVersion: v2\nname: demo\nversion: 1.0.0\n")
	writeTestFile(t, filepath.Join(root, "chart", "x.jsonnet"), "import '../shared/lib.libsonnet'\n")
	writeTestFile(t, filepath.Join(root, "shared", "lib.libsonnet"), "{}\n")
	application := argoappv1.Application{
		Name: "chart",
		Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{RepoURL: "https://git.example.test/repo.git", TargetRevision: "main", Path: "chart"}},
	}
	got := withSelectionOnlyPaths(context.Background(), root, nil, selfRepoRefs{}, []ApplicationSelectionInput{{Application: application}}).inputs
	if selected, _ := SelectChangedApplicationInputs(got, []string{"shared/lib.libsonnet"}); len(selected) != 0 {
		t.Fatalf("a Helm source owned a Jsonnet import (paths %v)", got[0].Paths)
	}
}

// TestWithSelectionOnlyPathsMarksAFailedScanUnknown pins that a Jsonnet
// import scan that fails, here on an unreadable directory, leaves the
// source's inputs unknown: changed-only selects it on any change.
func TestWithSelectionOnlyPathsMarksAFailedScanUnknown(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a directory without read permission")
	}
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "app", "main.jsonnet"), "import '../shared/lib.libsonnet'\n")
	writeTestFile(t, filepath.Join(root, "shared", "lib.libsonnet"), "{}\n")
	locked := filepath.Join(root, "app", "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	application := argoappv1.Application{
		Name: "demo",
		Spec: argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{RepoURL: "https://git.example.test/repo.git", TargetRevision: "main", Path: "app"}},
	}
	side := withSelectionOnlyPaths(context.Background(), root, nil, selfRepoRefs{}, []ApplicationSelectionInput{{Application: application}})
	if len(side.unscanned) != 1 {
		t.Fatalf("unscanned = %v, want the source whose scan failed", side.unscanned)
	}
}
